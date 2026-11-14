package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zacharyelston/wehelp/internal/seed"
	"github.com/zacharyelston/wehelp/internal/tunnel"
)

func staffLogin(c *client, code string) (int, map[string]any) {
	return c.do("POST", "/staff/login", map[string]string{"tenant": seed.DemoTenant, "email": seed.ProviderEmail,
		"password": seed.ProviderPassword, "code": code})
}

// TestStaffSignIn is the #38 flow: first sign-in enrolls TOTP, then every
// sign-in needs password + code; cookie sessions with CSRF; LAN only.
func TestStaffSignIn(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	web := &client{t: t, h: h}

	// First sign-in: password accepted, TOTP not set up yet.
	code, out := staffLogin(web, "")
	if code != http.StatusOK || out["status"] != "totp_setup" {
		t.Fatalf("first login: %d %v", code, out)
	}
	ck := web.cookies[staffCookie]
	if ck == nil || !ck.Secure || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" || ck.Domain != "" {
		t.Fatalf("session cookie attributes: %+v", ck)
	}
	// A setup session can't use staff routes.
	if code, _ := web.do("GET", "/patients", nil); code != http.StatusUnauthorized {
		t.Errorf("setup session on staff route: %d", code)
	}
	// CSRF is required for changes.
	if code, _ := web.do("POST", "/staff/totp/setup", nil); code != http.StatusForbidden {
		t.Errorf("setup without CSRF: %d", code)
	}
	web.csrf = out["csrf_token"].(string)
	code, setup := web.do("POST", "/staff/totp/setup", nil)
	if code != http.StatusOK || setup["secret"] == "" || setup["qr_png"] == "" {
		t.Fatalf("totp setup: %d %v", code, setup)
	}
	secret := setup["secret"].(string)
	if code, _ := web.do("POST", "/staff/totp/confirm", map[string]string{"code": "000000"}); code != http.StatusUnauthorized {
		t.Errorf("wrong confirm code: %d", code)
	}
	code, out = web.do("POST", "/staff/totp/confirm", map[string]string{"code": totpNow(t, secret)})
	if code != http.StatusOK || out["status"] != "active" {
		t.Fatalf("confirm: %d %v", code, out)
	}
	web.csrf = out["csrf_token"].(string)

	// Signed in: reads work; changes need the CSRF header.
	if code, _ := web.do("GET", "/patients", nil); code != http.StatusOK {
		t.Errorf("patients with cookie: %d", code)
	}
	csrf := web.csrf
	web.csrf = ""
	if code, _ := web.do("POST", "/patients", map[string]string{"display_name": "A", "email": "a@demo.wehelp"}); code != http.StatusForbidden {
		t.Errorf("change without CSRF: %d", code)
	}
	web.csrf = "wrong"
	if code, _ := web.do("POST", "/patients", map[string]string{"display_name": "A", "email": "a@demo.wehelp"}); code != http.StatusForbidden {
		t.Errorf("change with a wrong CSRF token: %d", code)
	}
	web.csrf = csrf
	if code, _ := web.do("POST", "/patients", map[string]string{"display_name": "A", "email": "a@demo.wehelp"}); code != http.StatusCreated {
		t.Errorf("change with CSRF: %d", code)
	}
	if code, me := web.do("GET", "/staff/me", nil); code != http.StatusOK || me["csrf_token"] != csrf || me["status"] != "active" {
		t.Errorf("staff me: %d %v", code, me)
	}

	// Sign out ends the session.
	if code, _ := web.do("POST", "/staff/logout", nil); code != http.StatusNoContent {
		t.Errorf("logout: %d", code)
	}
	stale := &client{t: t, h: h, cookies: map[string]*http.Cookie{staffCookie: ck}, csrf: csrf}
	if code, _ := stale.do("GET", "/patients", nil); code != http.StatusUnauthorized {
		t.Errorf("old cookie after logout: %d", code)
	}

	// Later sign-ins need the code; a code works once.
	next := &client{t: t, h: h}
	if code, _ := staffLogin(next, ""); code != http.StatusUnauthorized {
		t.Errorf("login without code: %d", code)
	}
	_, _ = pool.Exec(t.Context(), `update users set failed_logins = 0, totp_last_step = null where email = $1`, seed.ProviderEmail)
	good := totpNow(t, secret)
	if code, out := staffLogin(next, good); code != http.StatusOK || out["status"] != "active" {
		t.Fatalf("login with code: %d %v", code, out)
	}
	if code, _ := staffLogin(&client{t: t, h: h}, good); code != http.StatusUnauthorized {
		t.Errorf("replayed code: %d", code)
	}
}

func TestStaffLockout(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	secret := enableTOTP(t, pool, seed.ProviderEmail)
	for i := 0; i < lockAfter; i++ {
		c := &client{t: t, h: h}
		c.do("POST", "/staff/login", map[string]string{"tenant": seed.DemoTenant, "email": seed.ProviderEmail, "password": "wrong-password-123"})
	}
	code, _ := staffLogin(&client{t: t, h: h}, totpNow(t, secret))
	if code != http.StatusUnauthorized {
		t.Fatalf("locked account signed in: %d", code)
	}
	var events int
	_ = pool.QueryRow(t.Context(), `select count(*) from audit_events where action = 'staff.signin_failed'`).Scan(&events)
	if events < lockAfter {
		t.Errorf("%d failed sign-in audit events", events)
	}
}

// TestStaffRoutesHiddenFromRelay: through the Relay, staff routes, password
// sign-in and the web UI are a 404, decided by the listener (#38).
func TestStaffRoutesHiddenFromRelay(t *testing.T) {
	s, _ := testBackOffice(t)
	h := s.Router()
	relay := &client{t: t, h: h, relay: true}
	for _, rt := range []struct{ method, path string }{
		{"POST", "/staff/login"}, {"GET", "/staff/me"}, {"POST", "/auth/login"}, {"POST", "/auth/register"},
		{"GET", "/patients"}, {"POST", "/pairing/invites"}, {"GET", "/patients/00000000-0000-0000-0000-000000000000/devices"},
	} {
		if code, _ := relay.do(rt.method, rt.path, map[string]string{}); code != http.StatusNotFound {
			t.Errorf("%s %s through the Relay: %d, want 404", rt.method, rt.path, code)
		}
	}
	// Phones' own routes still work through the Relay.
	if code, _ := relay.do("GET", "/ping", nil); code != http.StatusOK {
		t.Errorf("ping through the Relay: %d", code)
	}
	if code, _ := relay.do("POST", "/pairing/redeem", map[string]string{}); code == http.StatusNotFound {
		t.Error("pairing redeem hidden from the Relay")
	}
	// The web UI itself.
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(tunnel.WithViaRelay(req.Context()))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("web UI through the Relay: %d", rec.Code)
	}
}

func TestHealthOrRedirect(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := healthOrRedirect(next, "8443")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://barnacle.local:8080/healthz", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("healthz on plain HTTP: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://barnacle.local:8080/api/v1/patients?x=1", nil))
	if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != "https://barnacle.local:8443/api/v1/patients?x=1" {
		t.Errorf("redirect: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}
