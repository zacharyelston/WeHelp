package server

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/seed"
	"github.com/zacharyelston/wehelp/internal/tunnel"
)

// TestSupplyOrders is #97: patients order from the catalogue through the
// Relay; staff see and fulfil orders on the LAN only; every change is
// audited and the patient gets the generic order_update push.
func TestSupplyOrders(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	fp := &fakePusher{result: tunnel.PushSent}
	s.SetPusher(fp)
	staffToken := login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)
	staff := &client{t: t, h: h, token: staffToken}
	staffRemote := &client{t: t, h: h, token: staffToken, relay: true}
	relay := &client{t: t, h: h, relay: true}
	jordan := patientID(t, pool, seed.Patient1Email)
	_, sess := pairPhone(t, staff, relay, jordan)
	phone := &client{t: t, h: h, token: sess["access_token"].(string), relay: true}
	phone.do("PUT", "/devices/current/push", map[string]string{"token": strings.Repeat("ab", 32), "environment": "sandbox"})

	// The seed loads the demo catalogue; patients see active products.
	var cat struct {
		Products []productView `json:"products"`
		Payment  string        `json:"payment"`
	}
	if code := phone.doInto("GET", "/store/products", &cat); code != 200 || len(cat.Products) != 10 || cat.Payment == "" {
		t.Fatalf("catalogue: %d %d", code, len(cat.Products))
	}
	greens, tea := cat.Products[0], cat.Products[1]

	// Staff manage the catalogue on the LAN only.
	if code, _ := staffRemote.do("POST", "/store/products", map[string]any{"sku": "X", "name": "X", "price_cents": 1}); code != http.StatusNotFound {
		t.Errorf("product create through the relay: %d", code)
	}
	if code, _ := phone.do("POST", "/store/products/demo", nil); code != http.StatusNotFound {
		t.Errorf("patient loading demo products: %d", code)
	}
	code, retired := staff.do("POST", "/store/products", map[string]any{"sku": "OLD-1", "name": "Retired tea", "price_cents": 900, "active": false})
	if code != http.StatusCreated {
		t.Fatalf("create product: %d %v", code, retired)
	}
	if code, _ := staff.do("POST", "/store/products", map[string]any{"sku": "OLD-1", "name": "Dup", "price_cents": 1}); code != http.StatusConflict {
		t.Errorf("duplicate sku: %d", code)
	}
	var all struct {
		Products []productView `json:"products"`
	}
	staff.doInto("GET", "/store/products", &all)
	if len(all.Products) != 11 {
		t.Errorf("staff see %d products", len(all.Products))
	}

	order := func(c *client, items ...map[string]any) (int, map[string]any) {
		t.Helper()
		return c.do("POST", "/orders", map[string]any{"items": items, "note": "Pick up Friday"})
	}
	line := func(p any, q int) map[string]any { return map[string]any{"product_id": p, "quantity": q} }

	// Who and what may be ordered.
	if code, _ := order(staff, line(greens.ID, 1)); code != http.StatusForbidden {
		t.Errorf("staff ordering: %d", code)
	}
	for name, items := range map[string][]map[string]any{
		"empty":        {},
		"zero":         {line(greens.ID, 0)},
		"too many":     {line(greens.ID, 21)},
		"duplicate":    {line(greens.ID, 1), line(greens.ID, 2)},
		"unknown":      {line(uuid.New(), 1)},
		"inactive":     {line(retired["id"], 1)},
		"half unknown": {line(greens.ID, 1), line(uuid.New(), 1)},
	} {
		if code, out := order(phone, items...); code != http.StatusBadRequest {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}

	code, placed := order(phone, line(greens.ID, 2), line(tea.ID, 1))
	if code != http.StatusCreated {
		t.Fatalf("place order: %d %v", code, placed)
	}
	want := 2*greens.PriceCents + tea.PriceCents
	if placed["status"] != "placed" || int(placed["total_cents"].(float64)) != want || len(placed["items"].([]any)) != 2 {
		t.Fatalf("order %v (want total %d)", placed, want)
	}
	orderID := placed["id"].(string)

	// A later price change doesn't touch the placed order.
	upd := map[string]any{"sku": greens.SKU, "name": greens.Name, "price_cents": greens.PriceCents + 500, "category": greens.Category}
	if code, out := staff.do("PUT", "/store/products/"+greens.ID.String(), upd); code != 200 {
		t.Fatalf("update product: %d %v", code, out)
	}

	// The patient sees their own orders; staff only on the LAN, with names.
	var mine struct {
		Orders []orderView `json:"orders"`
	}
	phone.doInto("GET", "/orders", &mine)
	if len(mine.Orders) != 1 || mine.Orders[0].TotalCents != want || mine.Orders[0].PatientName != "" || len(mine.Orders[0].Items) != 2 {
		t.Fatalf("patient orders %+v", mine.Orders)
	}
	if code := staffRemote.doInto("GET", "/orders", &mine); code != http.StatusNotFound {
		t.Errorf("staff listing orders through the relay: %d", code)
	}
	var desk struct {
		Orders []orderView `json:"orders"`
	}
	staff.doInto("GET", "/orders?status=placed", &desk)
	if len(desk.Orders) != 1 || desk.Orders[0].PatientName != seed.Patient1Name {
		t.Fatalf("staff orders %+v", desk.Orders)
	}

	// Staff move it on, LAN only; each change pushes order_update.
	if code, _ := staffRemote.do("POST", "/orders/"+orderID+"/status", map[string]string{"status": "ready"}); code != http.StatusNotFound {
		t.Errorf("status through the relay: %d", code)
	}
	if code, _ := phone.do("POST", "/orders/"+orderID+"/status", map[string]string{"status": "fulfilled"}); code != http.StatusNotFound {
		t.Errorf("patient setting status: %d", code)
	}
	if code, _ := staff.do("POST", "/orders/"+orderID+"/status", map[string]string{"status": "shipped"}); code != http.StatusConflict {
		t.Errorf("unknown status: %d", code)
	}
	code, ready := staff.do("POST", "/orders/"+orderID+"/status", map[string]string{"status": "ready", "note": "At the front desk"})
	if code != 200 || ready["status"] != "ready" || ready["staff_note"] != "At the front desk" {
		t.Fatalf("ready: %d %v", code, ready)
	}
	eventually(t, "ready push", func() bool { return fp.count() == 1 })
	if code, _ := phone.do("POST", "/orders/"+orderID+"/cancel", nil); code != http.StatusConflict {
		t.Errorf("patient cancelling a ready order: %d", code)
	}
	code, done := staff.do("POST", "/orders/"+orderID+"/status", map[string]string{"status": "fulfilled"})
	if code != 200 || done["closed_at"] == nil {
		t.Fatalf("fulfil: %d %v", code, done)
	}
	eventually(t, "fulfilled push", func() bool { return fp.count() == 2 })
	if code, _ := staff.do("POST", "/orders/"+orderID+"/status", map[string]string{"status": "cancelled"}); code != http.StatusConflict {
		t.Errorf("cancelling a fulfilled order: %d", code)
	}
	fp.mu.Lock()
	for _, p := range fp.sent {
		if p.Category != "order_update" {
			t.Errorf("push category %q", p.Category)
		}
	}
	fp.mu.Unlock()

	// A patient cancels a placed order of their own (no push), not another's.
	_, second := order(phone, line(tea.ID, 3))
	otherPatient := &client{t: t, h: h, relay: true}
	_, sess2 := pairPhone(t, staff, otherPatient, patientID(t, pool, seed.Patient2Email))
	sam := &client{t: t, h: h, token: sess2["access_token"].(string), relay: true}
	if code, _ := sam.do("POST", "/orders/"+second["id"].(string)+"/cancel", nil); code != http.StatusNotFound {
		t.Errorf("cancelling someone else's order: %d", code)
	}
	var samOrders struct {
		Orders []orderView `json:"orders"`
	}
	sam.doInto("GET", "/orders", &samOrders)
	if len(samOrders.Orders) != 0 {
		t.Errorf("another patient sees %d orders", len(samOrders.Orders))
	}
	if code, out := phone.do("POST", "/orders/"+second["id"].(string)+"/cancel", nil); code != 200 || out["status"] != "cancelled" {
		t.Errorf("cancel: %d %v", code, out)
	}
	if fp.count() != 2 {
		t.Errorf("patient's own cancel pushed")
	}

	// At most five open orders.
	for i := 0; i < 5; i++ {
		if code, out := order(phone, line(tea.ID, 1)); code != http.StatusCreated {
			t.Fatalf("order %d: %d %v", i, code, out)
		}
	}
	if code, _ := order(phone, line(tea.ID, 1)); code != http.StatusConflict {
		t.Errorf("sixth open order: %d", code)
	}

	// Every change is audited.
	rows, _ := pool.Query(context.Background(), `select action from audit_events where resource_type = 'order' order by seq`)
	var actions []string
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	got := strings.Join(actions, ",")
	if !strings.HasPrefix(got, "order.create,order.ready,order.fulfilled,order.create,order.cancelled,order.create") {
		t.Errorf("audit actions %s", got)
	}
}

type fakeSitePublisher struct {
	mu      sync.Mutex
	bundles [][]byte
}

func (f *fakeSitePublisher) PublishSite(_ context.Context, b []byte) (tunnel.SiteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bundles = append(f.bundles, b)
	return tunnel.SiteResult{OK: true, Files: 1, Path: "/s/x/", URL: "https://relay.test:8444/s/x/"}, nil
}

// TestPracticeSite is #97: staff edit and publish the website on the LAN;
// the bundle holds the practice content, catalogue and library, and no
// patient data; phones read the library through the tunnel.
func TestPracticeSite(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	staffToken := login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)
	staff := &client{t: t, h: h, token: staffToken}
	staffRemote := &client{t: t, h: h, token: staffToken, relay: true}
	relay := &client{t: t, h: h, relay: true}
	_, sess := pairPhone(t, staff, relay, patientID(t, pool, seed.Patient1Email))
	phone := &client{t: t, h: h, token: sess["access_token"].(string), relay: true}

	// Starter content names the practice.
	code, got := staff.do("GET", "/site", nil)
	if code != 200 || got["content"].(map[string]any)["practice_name"] != seed.DemoTenant {
		t.Fatalf("get site: %d %v", code, got)
	}
	for _, c := range []*client{staffRemote, phone} {
		if code, _ := c.do("GET", "/site", nil); code != http.StatusNotFound {
			t.Errorf("site admin through the relay: %d", code)
		}
	}

	content := map[string]any{
		"practice_name": "Shaw Naturopathy", "about": "We listen.", "app_url": "https://apps.apple.com/app/wehelp",
		"providers": []map[string]string{{"name": "Dr. Ada Shaw", "bio": "Twelve years in practice.", "photo_file": "ada.png"}},
		"services":  []map[string]string{{"name": "Herbal medicine"}},
		"podcasts":  []map[string]string{{"title": "Sleep well", "url": "https://example.com/sleep"}},
	}
	if code, out := staff.do("PUT", "/site", content); code != 200 {
		t.Fatalf("save: %d %v", code, out)
	}
	content["podcasts"] = []map[string]string{{"title": "Bad", "url": "javascript:alert(1)"}}
	if code, _ := staff.do("PUT", "/site", content); code != http.StatusBadRequest {
		t.Errorf("javascript podcast link: %d", code)
	}

	upload := func(c *client, kind, filename string, data []byte) (int, map[string]any) {
		t.Helper()
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		_ = mw.WriteField("kind", kind)
		_ = mw.WriteField("title", "Sleep guide")
		fw, _ := mw.CreateFormFile("file", filename)
		_, _ = fw.Write(data)
		_ = mw.Close()
		req := httptest.NewRequest("POST", "/api/v1/site/files", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+c.token)
		if c.relay {
			req = req.WithContext(tunnel.WithViaRelay(req.Context()))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	pdf := []byte("%PDF-1.4\n% sleep guide\n")
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	if code, _ := upload(staffRemote, "library", "guide.pdf", pdf); code != http.StatusNotFound {
		t.Errorf("upload through the relay: %d", code)
	}
	for name, try := range map[string][2]string{
		"html":         {"library", "page.html"},
		"fake pdf":     {"library", "evil.pdf"},
		"pdf as image": {"image", "guide.pdf"},
		"svg":          {"image", "logo.svg"},
	} {
		data := pdf
		if name == "fake pdf" {
			data = []byte("<html><script>alert(1)</script>")
		}
		if code, out := upload(staff, try[0], try[1], data); code != http.StatusBadRequest {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	code, guide := upload(staff, "library", "Sleep Guide.pdf", pdf)
	if code != http.StatusCreated || guide["filename"] != "sleep-guide.pdf" {
		t.Fatalf("upload pdf: %d %v", code, guide)
	}
	if code, _ := upload(staff, "library", "sleep-guide.pdf", pdf); code != http.StatusConflict {
		t.Errorf("duplicate file name: %d", code)
	}
	if code, out := upload(staff, "image", "ada.png", png); code != http.StatusCreated {
		t.Fatalf("upload image: %d %v", code, out)
	}

	// Publishing needs a Relay connection.
	if code, _ := staff.do("POST", "/site/publish", nil); code != http.StatusServiceUnavailable {
		t.Errorf("publish with no relay: %d", code)
	}
	fsp := &fakeSitePublisher{}
	s.SetSitePublisher(fsp)
	if code, _ := staffRemote.do("POST", "/site/publish", nil); code != http.StatusNotFound {
		t.Errorf("publish through the relay: %d", code)
	}
	code, pub := staff.do("POST", "/site/publish", nil)
	if code != 200 || pub["url"] != "https://relay.test:8444/s/x/" {
		t.Fatalf("publish: %d %v", code, pub)
	}
	files := untar(t, fsp.bundles[0])
	for _, want := range []string{"index.html", "team.html", "store.html", "library.html", "join.html", "files/sleep-guide.pdf", "files/ada.png"} {
		if files[want] == "" {
			t.Errorf("bundle lacks %s", want)
		}
	}
	if !strings.Contains(files["index.html"], "Shaw Naturopathy") || !strings.Contains(files["store.html"], "Morning Greens Blend") ||
		!strings.Contains(files["library.html"], "https://example.com/sleep") || !strings.Contains(files["team.html"], `src="files/ada.png"`) {
		t.Error("bundle is missing content")
	}
	// No patient data in the public site.
	for name, body := range files {
		for _, phi := range []string{seed.Patient1Name, seed.Patient1Email, seed.Patient2Name} {
			if strings.Contains(body, phi) {
				t.Errorf("%s contains patient data %q", name, phi)
			}
		}
	}
	var audited int
	_ = pool.QueryRow(context.Background(), `select count(*) from audit_events where action = 'site.publish'`).Scan(&audited)
	if audited != 1 {
		t.Errorf("publish audit events: %d", audited)
	}

	// Phones read the library through the tunnel.
	code, lib := phone.do("GET", "/library", nil)
	if code != 200 || len(lib["files"].([]any)) != 1 || len(lib["podcasts"].([]any)) != 1 || lib["site_url"] != "https://relay.test:8444/s/x/" {
		t.Fatalf("library: %d %v", code, lib)
	}
	req := httptest.NewRequest("GET", "/api/v1/library/files/"+guide["id"].(string), nil)
	req.Header.Set("Authorization", "Bearer "+phone.token)
	req = req.WithContext(tunnel.WithViaRelay(req.Context()))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != string(pdf) || rec.Header().Get("Content-Type") != "application/pdf" {
		t.Errorf("download: %d %q", rec.Code, rec.Header())
	}
	var imgID string
	_ = pool.QueryRow(context.Background(), `select id::text from site_files where kind = 'image'`).Scan(&imgID)
	if code, _ := phone.do("GET", "/library/files/"+imgID, nil); code != http.StatusNotFound {
		t.Errorf("image through the library route: %d", code)
	}
	if code, _ := staff.do("DELETE", "/site/files/"+guide["id"].(string), nil); code != http.StatusNoContent {
		t.Errorf("delete: %d", code)
	}
}

func untar(t *testing.T, b []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		out[hdr.Name] = string(data)
	}
}
