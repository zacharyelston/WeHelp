package server

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/seed"
)

// tinyPNG is the PNG signature plus padding: enough for content sniffing.
var tinyPNG = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

// putPhoto sends a raw photo body with the client's cookie and CSRF token.
func putPhoto(c *client, body []byte) int {
	req := httptest.NewRequest("PUT", "/api/v1/staff/profile/photo", bytes.NewReader(body))
	req.Header.Set("Content-Type", "image/png")
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	req.Header.Set("X-CSRF-Token", c.csrf)
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec.Code
}

// bundleText returns every file in a site bundle, concatenated, and the
// file names.
func bundleText(t *testing.T, s *Server, tenant uuid.UUID) (string, []string) {
	t.Helper()
	bundle, _, err := s.buildSite(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	var names []string
	tr := tar.NewReader(bytes.NewReader(bundle))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		b, _ := io.ReadAll(tr)
		all.Write(b)
	}
	return all.String(), names
}

// TestStaffProfiles is #102: a provider edits their own profile and photo,
// and the website publish uses it for their bio.
func TestStaffProfiles(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	ctx := context.Background()
	var tenant uuid.UUID
	var adaID string
	if err := pool.QueryRow(ctx, `select tenant_id, id::text from users where email = $1`, seed.ProviderEmail).Scan(&tenant, &adaID); err != nil {
		t.Fatal(err)
	}
	ada := webSignIn(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)
	adm := webSignIn(t, h, pool, seed.AdminEmail, seed.AdminPassword)

	code, out := ada.do("GET", "/staff/profile", nil)
	if code != http.StatusOK || out["display_name"] != seed.ProviderName || out["show_on_website"] != false || out["has_photo"] != false {
		t.Fatalf("empty profile: %d %v", code, out)
	}

	// Through the Relay, all of it is a 404.
	relay := &client{t: t, h: h, relay: true, cookies: ada.cookies, csrf: ada.csrf}
	for _, rt := range []struct{ method, path string }{{"GET", "/staff/profile"}, {"PUT", "/staff/profile"},
		{"GET", "/staff/profile/photo"}, {"PUT", "/staff/profile/photo"}, {"DELETE", "/staff/profile/photo"}} {
		if code, _ := relay.do(rt.method, rt.path, map[string]any{}); code != http.StatusNotFound {
			t.Errorf("%s %s through the Relay: %d, want 404", rt.method, rt.path, code)
		}
	}

	// Edits are checked, saved and audited.
	if code, _ := ada.do("PUT", "/staff/profile", map[string]any{"display_name": "Dr. Ada Shaw", "bio": strings.Repeat("x", 3001)}); code != http.StatusBadRequest {
		t.Errorf("long bio: %d, want 400", code)
	}
	if code, _ := ada.do("PUT", "/staff/profile", map[string]any{"display_name": "Dr. Ada Shaw", "user_id": adaID}); code != http.StatusBadRequest {
		t.Errorf("unknown field: %d, want 400", code)
	}
	code, out = ada.do("PUT", "/staff/profile", map[string]any{"display_name": "Dr. Ada Shaw", "credentials": "MD, Family Medicine",
		"bio": "Ada has cared for families here for ten years.", "show_on_website": true})
	if code != http.StatusOK || out["credentials"] != "MD, Family Medicine" || out["show_on_website"] != true {
		t.Fatalf("save profile: %d %v", code, out)
	}
	if n := auditCount(t, pool, "staff.profile_update", adaID); n != 1 {
		t.Errorf("%d staff.profile_update events", n)
	}

	// The photo must be a real image of at most 2 MB.
	if code := putPhoto(ada, []byte("<script>alert(1)</script>")); code != http.StatusBadRequest {
		t.Errorf("non-image photo: %d, want 400", code)
	}
	if code := putPhoto(ada, append(append([]byte{}, tinyPNG...), make([]byte, maxProfilePhoto)...)); code != http.StatusBadRequest {
		t.Errorf("oversized photo: %d, want 400", code)
	}
	if code := putPhoto(ada, tinyPNG); code != http.StatusOK {
		t.Fatalf("photo upload: %d", code)
	}
	if code, out := ada.do("GET", "/staff/profile", nil); code != http.StatusOK || out["has_photo"] != true {
		t.Errorf("profile after photo: %d %v", code, out)
	}
	ada.do("GET", "/staff/profile/photo", nil)
	if ada.last.StatusCode != http.StatusOK || ada.last.Header.Get("Content-Type") != "image/png" {
		t.Errorf("photo preview: %d %s", ada.last.StatusCode, ada.last.Header.Get("Content-Type"))
	}
	// The admin's own profile is separate: no photo.
	adm.do("GET", "/staff/profile/photo", nil)
	if adm.last.StatusCode != http.StatusNotFound {
		t.Errorf("admin sees a photo: %d", adm.last.StatusCode)
	}

	// The Website tab lists the profile; a typed bio with the same name is
	// replaced, and other typed bios stay.
	code, out = adm.do("GET", "/site", nil)
	if code != http.StatusOK {
		t.Fatalf("get site: %d %v", code, out)
	}
	profiles, _ := out["profiles"].([]any)
	if len(profiles) != 1 || profiles[0].(map[string]any)["name"] != "Dr. Ada Shaw" || profiles[0].(map[string]any)["has_photo"] != true {
		t.Errorf("site profiles: %v", out["profiles"])
	}
	content := out["content"].(map[string]any)
	content["providers"] = []any{
		map[string]any{"name": "Dr. Ada Shaw", "credentials": "", "bio": "OLD TYPED BIO", "photo_file": ""},
		map[string]any{"name": "Dr. Typed Only", "credentials": "DO", "bio": "Typed in the Website tab.", "photo_file": ""},
	}
	if code, out := adm.do("PUT", "/site", content); code != http.StatusOK {
		t.Fatalf("put site: %d %v", code, out)
	}
	text, names := bundleText(t, s, tenant)
	if !strings.Contains(text, "Ada has cared for families here") || strings.Contains(text, "OLD TYPED BIO") || !strings.Contains(text, "Dr. Typed Only") {
		t.Error("bios in the bundle are not the profile plus the other typed bio")
	}
	photoName := "files/staff-photo-" + strings.ReplaceAll(adaID, "-", "")[:12] + ".png"
	if !strings.Contains(strings.Join(names, " "), photoName) || !strings.Contains(text, `src="`+photoName+`"`) {
		t.Errorf("photo %s missing from the bundle: %v", photoName, names)
	}

	// Removing the photo, turning the website off, or disabling the
	// account takes the profile off the site.
	if code, out := ada.do("DELETE", "/staff/profile/photo", nil); code != http.StatusOK || out["has_photo"] != false {
		t.Errorf("remove photo: %d %v", code, out)
	}
	if code, _ := adm.do("POST", "/staff/providers/"+adaID+"/disable", nil); code != http.StatusOK {
		t.Fatalf("disable: %d", code)
	}
	if text, _ := bundleText(t, s, tenant); strings.Contains(text, "Ada has cared for families here") {
		t.Error("a disabled provider's profile is on the site")
	}
	if code, _ := adm.do("POST", "/staff/providers/"+adaID+"/enable", nil); code != http.StatusOK {
		t.Fatalf("enable: %d", code)
	}
	if _, err := pool.Exec(ctx, `update staff_profiles set show_on_website = false where user_id = $1`, adaID); err != nil {
		t.Fatal(err)
	}
	if text, _ := bundleText(t, s, tenant); strings.Contains(text, "Ada has cared for families here") || !strings.Contains(text, "OLD TYPED BIO") {
		t.Error("with the profile off the site, the typed bio should come back")
	}
}
