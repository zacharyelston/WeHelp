package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pquerna/otp/totp"
	"github.com/zacharyelston/wehelp/internal/auth"
	"github.com/zacharyelston/wehelp/internal/config"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/seed"
	"github.com/zacharyelston/wehelp/internal/tunnel"
)

// Back Office database tests. They run when WEHELP_TEST_DATABASE_URL is set
// (CI sets it): each gets a fresh database with db/migrations applied and
// the demo seed loaded.

func testBackOffice(t *testing.T) (*Server, *pgxpool.Pool) {
	t.Helper()
	admin := os.Getenv("WEHELP_TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("WEHELP_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	name := "wehelp_test_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	if _, err := conn.Exec(ctx, "create database "+name); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = conn.Exec(context.Background(), "drop database "+name)
		conn.Close(context.Background())
	})
	files, _ := filepath.Glob(filepath.Join("..", "..", "db", "migrations", "V*.sql"))
	sort.Slice(files, func(i, j int) bool { return migrationNum(files[i]) < migrationNum(files[j]) })
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	if _, err := seed.Run(ctx, pool, io.Discard); err != nil {
		t.Fatal(err)
	}
	s := New(config.Config{AccessTTL: 15 * time.Minute, RefreshTTL: time.Hour, JWTSecret: "test-secret-test-secret-test-secret"}, pool)
	return s, pool
}

func migrationNum(path string) int {
	n := 0
	for _, c := range strings.TrimPrefix(filepath.Base(path), "V") {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

type client struct {
	t       *testing.T
	h       http.Handler
	token   string
	relay   bool // send as if through the Relay
	cookies map[string]*http.Cookie
	csrf    string // sent as X-CSRF-Token
	last    *http.Response
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, "/api/v1"+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.relay {
		req = req.WithContext(tunnel.WithViaRelay(req.Context()))
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	c.last = rec.Result()
	for _, ck := range c.last.Cookies() {
		if c.cookies == nil {
			c.cookies = map[string]*http.Cookie{}
		}
		if ck.MaxAge < 0 {
			delete(c.cookies, ck.Name)
		} else {
			c.cookies[ck.Name] = ck
		}
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// enableTOTP turns on TOTP for a staff account directly and returns its
// secret, as if the person had enrolled in the web UI.
func enableTOTP(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	const secret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
	if _, err := pool.Exec(context.Background(),
		`update users set totp_secret = $2, totp_enabled_at = now(), totp_last_step = null where email = $1`, email, secret); err != nil {
		t.Fatal(err)
	}
	return secret
}

func totpNow(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// login signs a staff member in for a bearer token (password + TOTP, #38).
func login(t *testing.T, h http.Handler, pool *pgxpool.Pool, email, password string) string {
	t.Helper()
	secret := enableTOTP(t, pool, email)
	c := &client{t: t, h: h}
	code, out := c.do("POST", "/auth/login", map[string]string{"tenant": seed.DemoTenant, "email": email,
		"password": password, "code": totpNow(t, secret)})
	if code != http.StatusOK {
		t.Fatalf("login %s: %d %v", email, code, out)
	}
	return out["access_token"].(string)
}

// phone is a stand-in for the app's Secure Enclave key.
type phone struct {
	key *ecdsa.PrivateKey
	der []byte
}

func newPhone(t *testing.T) phone {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	return phone{key: k, der: der}
}

func (p phone) proof(purpose, invite string) map[string]any {
	ts := time.Now().Unix()
	digest := sha256.Sum256(auth.DeviceProofMessage(purpose, invite, ts))
	sig, _ := ecdsa.SignASN1(rand.Reader, p.key, digest[:])
	return map[string]any{"timestamp": ts, "signature": base64.StdEncoding.EncodeToString(sig)}
}

func (p phone) redeem(c *client, invite string) (int, map[string]any) {
	body := p.proof("pair", invite)
	body["invite"], body["public_key"], body["name"] = invite, base64.StdEncoding.EncodeToString(p.der), "Test iPhone"
	return c.do("POST", "/pairing/redeem", body)
}

func inviteCode(t *testing.T, out map[string]any) string {
	t.Helper()
	var payload QRPayload
	if err := json.Unmarshal([]byte(out["qr_payload"].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Version != 1 || payload.RelayPin == "" || payload.KeyPin == "" || payload.OfficeID == "" {
		t.Fatalf("qr payload %+v", payload)
	}
	png, _ := base64.StdEncoding.DecodeString(out["qr_png"].(string))
	if !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Fatal("qr_png is not a PNG")
	}
	return payload.Invite
}

func patientID(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `select id::text from users where email = $1`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPairingFlow(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	staff := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	phoneSide := &client{t: t, h: h, relay: true} // phones arrive through the Relay
	jordan := patientID(t, pool, seed.Patient1Email)

	// Not installed with the Registry yet: no invites.
	if code, _ := staff.do("POST", "/pairing/invites", map[string]string{"patient_id": jordan}); code != http.StatusServiceUnavailable {
		t.Errorf("invite before install: %d", code)
	}
	s.SetPairing(office.Pairing{RelayAddress: "relay.test:8443", RelayPin: "cGlu", OfficeID: uuid.NewString(), KeyPin: "a2V5"})

	// Staff routes are refused through the Relay.
	viaRelay := &client{t: t, h: h, token: staff.token, relay: true}
	if code, _ := viaRelay.do("POST", "/pairing/invites", map[string]string{"patient_id": jordan}); code != http.StatusNotFound {
		t.Errorf("invite via relay: %d", code)
	}

	code, out := staff.do("POST", "/pairing/invites", map[string]string{"patient_id": jordan})
	if code != http.StatusCreated {
		t.Fatalf("invite: %d %v", code, out)
	}
	inviteID, invite := out["id"].(string), inviteCode(t, out)
	if out["patient_name"] != seed.Patient1Name {
		t.Errorf("patient_name %v", out["patient_name"])
	}
	var stored int
	_ = pool.QueryRow(context.Background(), `select count(*) from pairing_invites where code_hash = $1`, []byte(invite)).Scan(&stored)
	if stored != 0 {
		t.Error("plaintext invite stored")
	}
	if _, st := staff.do("GET", "/pairing/invites/"+inviteID, nil); st["status"] != "issued" {
		t.Errorf("status before scan: %v", st)
	}

	// A proof from a different key than the one submitted fails.
	p, other := newPhone(t), newPhone(t)
	forged := other.proof("pair", invite)
	forged["invite"], forged["public_key"] = invite, base64.StdEncoding.EncodeToString(p.der)
	if code, _ := phoneSide.do("POST", "/pairing/redeem", forged); code != http.StatusUnauthorized {
		t.Errorf("mismatched proof: %d", code)
	}

	code, out = p.redeem(phoneSide, invite)
	if code != http.StatusOK || out["practice"] != seed.DemoTenant || out["patient_name"] != seed.Patient1Name || out["refresh_token"] == nil {
		t.Fatalf("redeem: %d %v", code, out)
	}
	// Single use.
	if code, _ := newPhone(t).redeem(phoneSide, invite); code != http.StatusUnauthorized {
		t.Errorf("reused invite: %d", code)
	}

	patient := &client{t: t, h: h, token: out["access_token"].(string), relay: true}
	if code, me := patient.do("GET", "/me", nil); code != http.StatusOK || me["role"] != "patient" || me["user_id"] != jordan {
		t.Errorf("me: %d %v", code, me)
	}
	// The session refreshes with the device key.
	refresh := map[string]any{"refresh_token": out["refresh_token"]}
	for k, v := range p.proof(auth.PurposeRefresh, out["refresh_token"].(string)) {
		refresh[k] = v
	}
	if code, _ := phoneSide.do("POST", "/auth/refresh", refresh); code != http.StatusOK {
		t.Errorf("refresh with device key: %d", code)
	}
	if code, st := staff.do("GET", "/pairing/invites/"+inviteID, nil); st["status"] != "paired" || st["device_name"] != "Test iPhone" {
		t.Errorf("final status: %d %v", code, st)
	}
	if code, list := staff.do("GET", "/patients", nil); code != http.StatusOK || !strings.Contains(mustString(list), `"devices":1`) {
		t.Errorf("patients: %d %v", code, list)
	}
	// The same key can't pair twice.
	_, out = staff.do("POST", "/pairing/invites", map[string]string{"patient_id": jordan})
	if code, _ := p.redeem(phoneSide, inviteCode(t, out)); code != http.StatusConflict {
		t.Errorf("same key again: %d", code)
	}
}

func mustString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestPairingExpiryAndScope(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	s.SetPairing(office.Pairing{RelayAddress: "r:1", RelayPin: "cA==", OfficeID: uuid.NewString(), KeyPin: "aw=="})
	staff := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	phoneSide := &client{t: t, h: h, relay: true}
	jordan := patientID(t, pool, seed.Patient1Email)

	// Expired: past the 10-minute redeem deadline.
	_, out := staff.do("POST", "/pairing/invites", map[string]string{"patient_id": jordan})
	expired := inviteCode(t, out)
	if _, err := pool.Exec(context.Background(), `update pairing_invites set expires_at = now() - interval '1 second' where id = $1`, out["id"]); err != nil {
		t.Fatal(err)
	}
	if code, _ := newPhone(t).redeem(phoneSide, expired); code != http.StatusUnauthorized {
		t.Errorf("expired invite: %d", code)
	}
	if _, st := staff.do("GET", "/pairing/invites/"+out["id"].(string), nil); st["status"] != "expired" {
		t.Errorf("expired status: %v", st)
	}
	// Made-up invite.
	if code, _ := newPhone(t).redeem(phoneSide, "AAAAAAAAAAAAAAAAAAAAAA"); code != http.StatusUnauthorized {
		t.Errorf("unknown invite: %d", code)
	}
	var devices int
	_ = pool.QueryRow(context.Background(), `select count(*) from devices`).Scan(&devices)
	if devices != 0 {
		t.Errorf("failed pairings created %d devices", devices)
	}

	// A provider can't pair a patient who isn't theirs.
	stranger := patientID(t, pool, seed.Patient2Email)
	_, _ = pool.Exec(context.Background(), `update provider_patients set status = 'revoked' where patient_id = $1`, stranger)
	if code, _ := staff.do("POST", "/pairing/invites", map[string]string{"patient_id": stranger}); code != http.StatusNotFound {
		t.Errorf("invite for unlinked patient: %d", code)
	}
}

func TestPatientsHaveNoPassword(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	anon := &client{t: t, h: h}

	// Seeded patients have no password at all.
	var hasHash bool
	_ = pool.QueryRow(context.Background(), `select password_hash is not null from users where email = $1`, seed.Patient1Email).Scan(&hasHash)
	if hasHash {
		t.Error("seeded patient has a password")
	}
	if code, _ := anon.do("POST", "/auth/login", map[string]string{"tenant": seed.DemoTenant, "email": seed.Patient1Email, "password": "anything-at-all"}); code != http.StatusUnauthorized {
		t.Errorf("patient login: %d", code)
	}
	// A patient left with a password from before #37 is still refused.
	hash, _ := auth.HashPassword("old-patient-password")
	_, _ = pool.Exec(context.Background(), `update users set password_hash = $2 where email = $1`, seed.Patient2Email, hash)
	if code, _ := anon.do("POST", "/auth/login", map[string]string{"tenant": seed.DemoTenant, "email": seed.Patient2Email, "password": "old-patient-password"}); code != http.StatusUnauthorized {
		t.Errorf("legacy patient password login: %d", code)
	}
	if code, _ := anon.do("POST", "/auth/register", map[string]string{"tenant": seed.DemoTenant, "email": "new@demo.wehelp", "password": "long-enough-password", "role": "patient"}); code != http.StatusBadRequest {
		t.Errorf("patient self-register: %d", code)
	}

	// Staff add patients (no password) on the LAN only.
	staff := &client{t: t, h: h, token: login(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)}
	if code, out := staff.do("POST", "/patients", map[string]string{"display_name": "Pat New", "email": "pat.new@demo.wehelp"}); code != http.StatusCreated || out["link_status"] != "active" {
		t.Fatalf("create patient: %d %v", code, out)
	}
	viaRelay := &client{t: t, h: h, token: staff.token, relay: true}
	if code, _ := viaRelay.do("POST", "/patients", map[string]string{"display_name": "X", "email": "x@demo.wehelp"}); code != http.StatusNotFound {
		t.Errorf("create patient via relay: %d", code)
	}
}
