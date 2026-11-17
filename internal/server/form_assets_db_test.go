package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zacharyelston/wehelp/internal/seed"
	"github.com/zacharyelston/wehelp/internal/tunnel"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// upload posts a multipart file as the signed-in client.
func upload(t *testing.T, c *client, path, filename string, data []byte) (int, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", filename)
	_, _ = fw.Write(data)
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/api/v1"+path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	req.Header.Set("X-CSRF-Token", c.csrf)
	if c.relay {
		req = req.WithContext(tunnel.WithViaRelay(req.Context()))
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// TestFormImages is the image part of #195: PNG and JPEG only, checked by
// content and size; publishing needs every named image; published images
// are immutable; a new draft gets copies; staff read them on the LAN.
func TestFormImages(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	ctx := context.Background()
	adm := webSignIn(t, h, pool, seed.AdminEmail, seed.AdminPassword)
	desk := webSignIn(t, h, pool, seed.OfficeEmail, seed.OfficePassword)
	if code, _ := adm.do("PUT", "/practice/features", map[string]any{"features": map[string]bool{"forms": true}}); code != http.StatusOK {
		t.Fatal("turn forms on")
	}
	_, out := adm.do("POST", "/forms", map[string]any{"name": "Office map"})
	formID, vid := out["form_id"].(string), out["version_id"].(string)
	base := "/form-versions/" + vid + "/assets"

	// Checks on the file.
	img := pngBytes(t, 4, 3)
	for _, c := range []struct {
		name string
		data []byte
	}{
		{"notes.txt", []byte("hello")},
		{"fake.png", []byte("<script>alert(1)</script>")},
		{"looks.jpg", img}, // a PNG named .jpg
		{"huge.png", pngBytes(t, 5000, 5000)},
	} {
		if code, out := upload(t, adm, base, c.name, c.data); code != http.StatusBadRequest {
			t.Errorf("upload %s: %d %v, want 400", c.name, code, out)
		}
	}
	if code, _ := upload(t, desk, base, "map.png", img); code != http.StatusForbidden {
		t.Errorf("front desk upload: %d, want 403", code)
	}
	code, out := upload(t, adm, base, "Office Map!.png", img)
	if code != http.StatusCreated || out["name"] != "Office-Map.png" || out["content_type"] != "image/png" {
		t.Fatalf("upload: %d %v", code, out)
	}
	if code, _ := upload(t, adm, base, "Office Map!.png", img); code != http.StatusConflict {
		t.Errorf("same name twice: %d, want 409", code)
	}

	// A definition naming an image that isn't uploaded can be saved, but not published.
	def := map[string]any{"format": "wehelp.form.v1", "title": "Map", "sections": []any{map[string]any{"id": "s1", "pages": []any{
		map[string]any{"id": "p1", "items": []any{
			map[string]any{"id": "m1", "type": "display", "media": map[string]any{"type": "image", "asset": "Office-Map.png", "alt": "Map"}},
			map[string]any{"id": "m2", "type": "display", "media": map[string]any{"type": "image", "asset": "missing.png"}},
		}}}}}}
	if code, out := adm.do("PUT", "/form-versions/"+vid, def); code != http.StatusOK {
		t.Fatalf("save: %d %v", code, out)
	}
	code, out = adm.do("POST", "/form-versions/"+vid+"/publish", nil)
	if code != http.StatusUnprocessableEntity || out["path"] != "sections[0].pages[0].items[1].media.asset" {
		t.Errorf("publish with a missing image: %d %v", code, out)
	}
	items := def["sections"].([]any)[0].(map[string]any)["pages"].([]any)[0].(map[string]any)
	items["items"] = items["items"].([]any)[:1]
	if code, _ := adm.do("PUT", "/form-versions/"+vid, def); code != http.StatusOK {
		t.Fatal("save")
	}

	// Staff read it on the LAN, sandboxed; not through the Relay.
	req := httptest.NewRequest("GET", "/api/v1"+base+"/Office-Map.png", nil)
	for _, ck := range desk.cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), img) ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("read image: %d %v", rec.Code, rec.Header())
	}
	relay := &client{t: t, h: h, relay: true, cookies: adm.cookies, csrf: adm.csrf}
	if code, _ := relay.do("GET", base+"/Office-Map.png", nil); code != http.StatusNotFound {
		t.Errorf("image through the Relay: %d, want 404", code)
	}
	if code, _ := upload(t, relay, base, "x.png", img); code != http.StatusNotFound {
		t.Errorf("upload through the Relay: %d, want 404", code)
	}

	// Delete on a draft, then upload again and publish.
	if code, _ := adm.do("DELETE", base+"/Office-Map.png", nil); code != http.StatusOK {
		t.Error("delete")
	}
	if code, _ := upload(t, adm, base, "Office-Map.png", img); code != http.StatusCreated {
		t.Fatal("upload again")
	}
	if code, out := adm.do("POST", "/form-versions/"+vid+"/publish", nil); code != http.StatusOK {
		t.Fatalf("publish: %d %v", code, out)
	}

	// Published images are immutable, in the handlers and at the trigger.
	if code, _ := upload(t, adm, base, "another.png", img); code != http.StatusConflict {
		t.Errorf("upload to a published version: %d, want 409", code)
	}
	if code, _ := adm.do("DELETE", base+"/Office-Map.png", nil); code != http.StatusConflict {
		t.Errorf("delete from a published version: %d, want 409", code)
	}
	if _, err := pool.Exec(ctx, `delete from form_assets where form_version_id = $1`, vid); err == nil {
		t.Error("the database let a published version's image be deleted")
	}
	if _, err := pool.Exec(ctx, `insert into form_assets (form_version_id, name, content_type, data, sha256) values ($1, 'z.png', 'image/png', '\x00', 'x')`, vid); err == nil {
		t.Error("the database let an image be added to a published version")
	}

	// A new draft gets copies.
	_, out = adm.do("POST", "/forms/"+formID+"/draft", nil)
	v2 := out["id"].(string)
	_, out = adm.do("GET", "/form-versions/"+v2, nil)
	if assets, _ := out["assets"].([]any); len(assets) != 1 || assets[0].(map[string]any)["name"] != "Office-Map.png" {
		t.Errorf("new draft's images: %v", out["assets"])
	}
	if code, out := adm.do("POST", "/form-versions/"+v2+"/publish", nil); code != http.StatusOK {
		t.Errorf("publish the copy: %d %v", code, out)
	}

	var n int
	_ = pool.QueryRow(ctx, `select count(*) from audit_events where action in ('form_asset.upload', 'form_asset.delete')`).Scan(&n)
	if n != 3 {
		t.Errorf("%d image audit events, want 3", n)
	}
}

func TestFormAssetName(t *testing.T) {
	for in, want := range map[string]string{
		"map.png": "map.png", "Office Map!.PNG": "Office-Map.png", "../../etc/x.jpeg": "x.jpeg",
		`C:\photos\a b.jpg`: "a-b.jpg", ".png": "image.png",
	} {
		if got, _, err := formAssetName(in); err != nil || got != want {
			t.Errorf("formAssetName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"x.gif", "x.svg", "x", "x.png.exe"} {
		if _, _, err := formAssetName(bad); err == nil {
			t.Errorf("formAssetName(%q) accepted", bad)
		}
	}
}
