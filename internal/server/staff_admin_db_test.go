package server

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zacharyelston/wehelp/internal/auth"
	"github.com/zacharyelston/wehelp/internal/seed"
)

const testAdminEmail, testAdminPassword = "admin@demo.wehelp", "wehelp-demo-admin-pw"

// addAdmin creates an admin account in the demo practice.
func addAdmin(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	hash, err := auth.HashPassword(testAdminPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `
		insert into users (tenant_id, email, password_hash, role, display_name)
		select id, $1, $2, 'admin', 'Office Admin' from tenants where name = $3`, testAdminEmail, hash, seed.DemoTenant); err != nil {
		t.Fatal(err)
	}
}

// webSignIn signs a staff member in to the staff web (cookie + CSRF).
func webSignIn(t *testing.T, h http.Handler, pool *pgxpool.Pool, email, password string) *client {
	t.Helper()
	secret := enableTOTP(t, pool, email)
	c := &client{t: t, h: h}
	code, out := c.do("POST", "/staff/login", map[string]string{"tenant": seed.DemoTenant, "email": email,
		"password": password, "code": totpNow(t, secret)})
	if code != http.StatusOK || out["status"] != "active" {
		t.Fatalf("web sign-in %s: %d %v", email, code, out)
	}
	c.csrf = out["csrf_token"].(string)
	return c
}

func setupToken(t *testing.T, out map[string]any) string {
	t.Helper()
	u, _ := out["setup_url"].(string)
	_, token, ok := strings.Cut(u, "/#setup=")
	if !ok || len(token) < 40 {
		t.Fatalf("setup_url %q", u)
	}
	return token
}

func auditCount(t *testing.T, pool *pgxpool.Pool, action, resourceID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`select count(*) from audit_events where action = $1 and resource_id = $2`, action, resourceID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestProviderInvites is #100: an admin adds a provider and gets a
// single-use set-up link; the provider sets a password, enrolls TOTP and
// sees their schedule. Providers can't do it, and none of it is reachable
// through the Relay.
func TestProviderInvites(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	ctx := context.Background()
	addAdmin(t, pool)
	adm := webSignIn(t, h, pool, testAdminEmail, testAdminPassword)
	ada := webSignIn(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)
	newProvider := map[string]string{"display_name": "Dr. Sam Lee", "email": "sam.lee@demo.wehelp"}

	// A provider is refused (403); through the Relay it is a 404.
	if code, _ := ada.do("GET", "/staff/providers", nil); code != http.StatusForbidden {
		t.Errorf("provider lists providers: %d, want 403", code)
	}
	if code, _ := ada.do("POST", "/staff/providers", newProvider); code != http.StatusForbidden {
		t.Errorf("provider adds a provider: %d, want 403", code)
	}
	relay := &client{t: t, h: h, relay: true, cookies: adm.cookies, csrf: adm.csrf}
	for _, rt := range []struct{ method, path string }{
		{"GET", "/staff/providers"}, {"POST", "/staff/providers"}, {"POST", "/staff/setup"},
		{"POST", "/staff/providers/00000000-0000-0000-0000-000000000000/invite"},
		{"POST", "/staff/providers/00000000-0000-0000-0000-000000000000/disable"},
		{"POST", "/staff/providers/00000000-0000-0000-0000-000000000000/enable"},
	} {
		if code, _ := relay.do(rt.method, rt.path, newProvider); code != http.StatusNotFound {
			t.Errorf("%s %s through the Relay: %d, want 404", rt.method, rt.path, code)
		}
	}

	// The admin adds a provider and gets a link; only its hash is stored.
	code, out := adm.do("POST", "/staff/providers", newProvider)
	if code != http.StatusCreated {
		t.Fatalf("admin adds provider: %d %v", code, out)
	}
	if adm.last.Header.Get("Cache-Control") != "no-store" {
		t.Error("set-up link response is cacheable")
	}
	prov := out["provider"].(map[string]any)
	samID := prov["id"].(string)
	if prov["status"] != "invited" {
		t.Errorf("new provider status %v", prov["status"])
	}
	first := setupToken(t, out)
	var stored []byte
	if err := pool.QueryRow(ctx, `select token_hash from staff_invites where user_id = $1`, samID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, hashToken(first)) {
		t.Error("stored invite is not the token's hash")
	}
	if n := auditCount(t, pool, "staff.invite", samID); n != 1 {
		t.Errorf("%d staff.invite events", n)
	}
	if code, _ := adm.do("POST", "/staff/providers", newProvider); code != http.StatusConflict {
		t.Errorf("duplicate email: %d, want 409", code)
	}
	// No password yet, so no sign-in.
	if code, _ := (&client{t: t, h: h}).do("POST", "/staff/login", map[string]string{"tenant": seed.DemoTenant,
		"email": newProvider["email"], "password": "anything-at-all-123"}); code != http.StatusUnauthorized {
		t.Errorf("sign-in before set-up: %d", code)
	}

	// A new link replaces the old one.
	code, out = adm.do("POST", "/staff/providers/"+samID+"/invite", nil)
	if code != http.StatusOK {
		t.Fatalf("reissue: %d %v", code, out)
	}
	second := setupToken(t, out)
	sam := &client{t: t, h: h}
	if code, _ := sam.do("POST", "/staff/setup", map[string]string{"token": first, "password": "sams-own-password-1"}); code != http.StatusBadRequest {
		t.Errorf("replaced link: %d, want 400", code)
	}
	if code, _ := sam.do("POST", "/staff/setup", map[string]string{"token": second, "password": "short"}); code != http.StatusBadRequest {
		t.Errorf("short password: %d, want 400", code)
	}

	// The provider sets a password, enrolls TOTP and sees their schedule.
	code, out = sam.do("POST", "/staff/setup", map[string]string{"token": second, "password": "sams-own-password-1"})
	if code != http.StatusOK || out["status"] != "totp_setup" || out["role"] != "provider" {
		t.Fatalf("setup: %d %v", code, out)
	}
	if n := auditCount(t, pool, "staff.setup", samID); n != 1 {
		t.Errorf("%d staff.setup events", n)
	}
	sam.csrf = out["csrf_token"].(string)
	code, totpOut := sam.do("POST", "/staff/totp/setup", nil)
	if code != http.StatusOK {
		t.Fatalf("totp setup: %d %v", code, totpOut)
	}
	secret := totpOut["secret"].(string)
	code, out = sam.do("POST", "/staff/totp/confirm", map[string]string{"code": totpNow(t, secret)})
	if code != http.StatusOK || out["status"] != "active" {
		t.Fatalf("totp confirm: %d %v", code, out)
	}
	sam.csrf = out["csrf_token"].(string)
	if code, _ := sam.do("GET", "/appointments", nil); code != http.StatusOK {
		t.Errorf("new provider's schedule: %d", code)
	}
	if code, _ := sam.do("GET", "/staff/providers", nil); code != http.StatusForbidden {
		t.Errorf("new provider lists providers: %d, want 403", code)
	}
	// The link works once.
	if code, _ := (&client{t: t, h: h}).do("POST", "/staff/setup", map[string]string{"token": second, "password": "someone-elses-pw-1"}); code != http.StatusBadRequest {
		t.Errorf("used link: %d, want 400", code)
	}
	if code, _ := adm.do("POST", "/staff/providers/"+samID+"/invite", nil); code != http.StatusConflict {
		t.Errorf("reissue after set-up: %d, want 409", code)
	}

	// An expired link is refused.
	code, out = adm.do("POST", "/staff/providers", map[string]string{"display_name": "Dr. Kim", "email": "kim@demo.wehelp"})
	if code != http.StatusCreated {
		t.Fatalf("add second provider: %d %v", code, out)
	}
	kimID := out["provider"].(map[string]any)["id"].(string)
	late := setupToken(t, out)
	if _, err := pool.Exec(ctx, `update staff_invites set expires_at = now() - interval '1 minute' where user_id = $1`, kimID); err != nil {
		t.Fatal(err)
	}
	if code, _ := (&client{t: t, h: h}).do("POST", "/staff/setup", map[string]string{"token": late, "password": "kims-own-password-1"}); code != http.StatusBadRequest {
		t.Errorf("expired link: %d, want 400", code)
	}
	code, out = adm.do("GET", "/staff/providers", nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, out)
	}
	status := map[string]string{}
	for _, p := range out["providers"].([]any) {
		p := p.(map[string]any)
		status[p["email"].(string)] = p["status"].(string)
	}
	if status["sam.lee@demo.wehelp"] != "active" || status["kim@demo.wehelp"] != "invite_expired" || status[seed.ProviderEmail] != "active" {
		t.Errorf("statuses: %v", status)
	}

	// Disable: web session, bearer token and sign-in all stop; nothing is
	// deleted. Enable restores sign-in.
	if _, err := pool.Exec(ctx, `update users set totp_last_step = null where id = $1`, samID); err != nil {
		t.Fatal(err)
	}
	bearer := &client{t: t, h: h}
	code, out = bearer.do("POST", "/auth/login", map[string]string{"tenant": seed.DemoTenant, "email": "sam.lee@demo.wehelp",
		"password": "sams-own-password-1", "code": totpNow(t, secret)})
	if code != http.StatusOK {
		t.Fatalf("bearer login: %d %v", code, out)
	}
	bearer.token = out["access_token"].(string)
	if code, _ := ada.do("POST", "/staff/providers/"+samID+"/disable", nil); code != http.StatusForbidden {
		t.Errorf("provider disables a provider: %d, want 403", code)
	}
	code, out = adm.do("POST", "/staff/providers/"+samID+"/disable", nil)
	if code != http.StatusOK || out["status"] != "disabled" {
		t.Fatalf("disable: %d %v", code, out)
	}
	if n := auditCount(t, pool, "staff.disable", samID); n != 1 {
		t.Errorf("%d staff.disable events", n)
	}
	if code, _ := sam.do("GET", "/appointments", nil); code != http.StatusUnauthorized {
		t.Errorf("disabled provider's web session: %d, want 401", code)
	}
	if code, _ := bearer.do("GET", "/appointments", nil); code != http.StatusUnauthorized {
		t.Errorf("disabled provider's bearer token: %d, want 401", code)
	}
	if _, err := pool.Exec(ctx, `update users set totp_last_step = null where id = $1`, samID); err != nil {
		t.Fatal(err)
	}
	signIn := func() int {
		code, _ := (&client{t: t, h: h}).do("POST", "/staff/login", map[string]string{"tenant": seed.DemoTenant,
			"email": "sam.lee@demo.wehelp", "password": "sams-own-password-1", "code": totpNow(t, secret)})
		return code
	}
	if code := signIn(); code != http.StatusUnauthorized {
		t.Errorf("disabled provider signs in: %d, want 401", code)
	}
	var revoked int
	_ = pool.QueryRow(ctx, `select count(*) from refresh_tokens where user_id = $1 and revoked_at is null`, samID).Scan(&revoked)
	if revoked != 0 {
		t.Errorf("%d live refresh tokens after disable", revoked)
	}
	if code, _ := adm.do("POST", "/staff/providers/"+samID+"/disable", nil); code != http.StatusConflict {
		t.Errorf("disable twice: %d, want 409", code)
	}
	if code, _ := ada.do("GET", "/appointments", nil); code != http.StatusOK {
		t.Errorf("other provider after disable: %d", code)
	}
	code, out = adm.do("POST", "/staff/providers/"+samID+"/enable", nil)
	if code != http.StatusOK || out["status"] != "active" {
		t.Fatalf("enable: %d %v", code, out)
	}
	if n := auditCount(t, pool, "staff.enable", samID); n != 1 {
		t.Errorf("%d staff.enable events", n)
	}
	if code := signIn(); code != http.StatusOK {
		t.Errorf("enabled provider signs in: %d", code)
	}
	if code, _ := adm.do("POST", "/staff/providers/00000000-0000-0000-0000-000000000000/disable", nil); code != http.StatusNotFound {
		t.Errorf("unknown provider: %d, want 404", code)
	}
}
