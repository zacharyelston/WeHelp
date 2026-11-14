package registry

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/ssh"
)

// browser is a console user's browser: a cookie jar over TLS (the session
// cookie is __Host-, so Secure) and the CSRF token from sign-in.
type browser struct {
	t      *testing.T
	srv    *httptest.Server
	client *http.Client
	csrf   string
}

func newBrowser(t *testing.T, srv *httptest.Server) *browser {
	jar, _ := cookiejar.New(nil)
	c := *srv.Client() // a copy: srv.Client() is shared
	c.Jar = jar
	return &browser{t: t, srv: srv, client: &c}
}

func (b *browser) do(method, path string, body any) (int, map[string]any) {
	b.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, b.srv.URL+"/v1/console"+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.csrf != "" {
		req.Header.Set(csrfHeader, b.csrf)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if tok, ok := out["csrf_token"].(string); ok {
		b.csrf = tok
	}
	return resp.StatusCode, out
}

// enrol signs in for the first time, sets up the authenticator and returns
// its secret.
func (b *browser) enrol(email, password string) string {
	b.t.Helper()
	code, out := b.do("POST", "/login", map[string]string{"email": email, "password": password})
	if code != http.StatusOK || out["status"] != "totp_setup" {
		b.t.Fatalf("first sign-in: %d %v", code, out)
	}
	if code, _ := b.do("GET", "/practices", nil); code != http.StatusUnauthorized {
		b.t.Fatalf("setup session reached the console: %d", code)
	}
	_, setup := b.do("POST", "/totp/setup", nil)
	secret := setup["secret"].(string)
	now, _ := totp.GenerateCode(secret, time.Now())
	if code, out := b.do("POST", "/totp/confirm", map[string]string{"code": now}); code != http.StatusOK || out["status"] != "active" {
		b.t.Fatalf("confirm: %d %v", code, out)
	}
	return secret
}

func TestConsole(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	st := New(pool, newSigner(t))
	if err := st.EnsureList(ctx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(NewServer(st).Router())
	t.Cleanup(srv.Close)

	// Two relays: "laptop" with a host key we hold, so we can sign reports.
	hostPub, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := ssh.NewSignerFromKey(hostPriv)
	sshPub, _ := ssh.NewPublicKey(hostPub)
	laptop, err := st.AddRelay(ctx, "cli:test", Relay{Name: "laptop", SSHAddress: "relay.test:2222",
		ConnectAddress: "relay.test:8443", SSHHostKey: string(ssh.MarshalAuthorizedKey(sshPub)), TLSPin: testPin})
	if err != nil {
		t.Fatal(err)
	}
	spare, err := st.AddRelay(ctx, "cli:test", Relay{Name: "spare", SSHAddress: "spare.test:2222",
		ConnectAddress: "spare.test:8443", SSHHostKey: sshKeyLine(t), TLSPin: testPin})
	if err != nil {
		t.Fatal(err)
	}

	const pw = "correct horse battery staple"
	if _, err := st.AddStaff(ctx, "cli:test", "sam@wehelp.test", "Sam Support", RoleSupport, "short"); err == nil {
		t.Error("short password accepted")
	}
	if _, err := st.AddStaff(ctx, "cli:test", "sam@wehelp.test", "Sam Support", RoleSupport, pw); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddStaff(ctx, "cli:test", "ada@wehelp.test", "Ada Admin", RoleAdmin, pw); err != nil {
		t.Fatal(err)
	}

	// Sign-in: password, then an authenticator; codes work once.
	sam := newBrowser(t, srv)
	if code, _ := sam.do("GET", "/practices", nil); code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", code)
	}
	secret := sam.enrol("sam@wehelp.test", pw)
	sam.do("POST", "/logout", nil)
	again := newBrowser(t, srv)
	if code, _ := again.do("POST", "/login", map[string]string{"email": "sam@wehelp.test", "password": pw}); code != http.StatusUnauthorized {
		t.Errorf("no code after enrolment: %d", code)
	}
	next, _ := totp.GenerateCode(secret, time.Now().Add(30*time.Second))
	if code, out := sam.do("POST", "/login", map[string]string{"email": "sam@wehelp.test", "password": pw, "code": next}); code != http.StatusOK || out["status"] != "active" {
		t.Fatalf("sign in: %d %v", code, out)
	}
	if code, _ := again.do("POST", "/login", map[string]string{"email": "sam@wehelp.test", "password": pw, "code": next}); code != http.StatusUnauthorized {
		t.Errorf("code reused: %d", code)
	}

	// Writes need the CSRF header.
	csrf := sam.csrf
	sam.csrf = ""
	if code, _ := sam.do("POST", "/practices", map[string]string{"name": "Nope", "relay": "laptop"}); code != http.StatusForbidden {
		t.Errorf("no CSRF: %d", code)
	}
	sam.csrf = csrf

	// Support: practices, contacts, subscription, providers, install tokens.
	code, p := sam.do("POST", "/practices", map[string]string{"name": "Riverside Family Care", "relay": "laptop",
		"contact_name": "Rita Owens", "contact_email": "rita@riverside.test", "contact_phone": "555-0100"})
	if code != http.StatusCreated || p["contact_email"] != "rita@riverside.test" || p["subscription"] != "trial" {
		t.Fatalf("create: %d %v", code, p)
	}
	pid := p["id"].(string)
	if code, out := sam.do("PATCH", "/practices/"+pid, map[string]string{"subscription": "active"}); code != http.StatusOK || out["subscription"] != "active" {
		t.Errorf("subscription: %d %v", code, out)
	}
	if code, _ := sam.do("PATCH", "/practices/"+pid, map[string]string{"subscription": "free"}); code != http.StatusBadRequest {
		t.Errorf("bad subscription: %d", code)
	}
	prov := map[string]string{"name": "Dr. Ada Shaw", "email": "ada@riverside.test", "role": "provider", "credentials": "MD", "licence_number": "CO-12345"}
	code, added := sam.do("POST", "/practices/"+pid+"/providers", prov)
	if code != http.StatusCreated || added["licence_number"] != "CO-12345" {
		t.Fatalf("add provider: %d %v", code, added)
	}
	if code, _ := sam.do("POST", "/practices/"+pid+"/providers", prov); code != http.StatusConflict {
		t.Errorf("duplicate provider: %d", code)
	}
	prov["role"] = "admin"
	if code, _ := sam.do("PUT", "/providers/"+added["id"].(string), prov); code != http.StatusOK {
		t.Errorf("update provider: %d", code)
	}
	sam.do("POST", "/practices/"+pid+"/providers", map[string]string{"name": "Front Desk", "email": "desk@riverside.test", "role": "admin"})
	_, detail := sam.do("GET", "/practices/"+pid, nil)
	if n := len(detail["providers"].([]any)); n != 2 {
		t.Errorf("%d providers", n)
	}
	if code, _ := sam.do("DELETE", "/providers/"+added["id"].(string), nil); code != http.StatusNoContent {
		t.Errorf("remove provider: %d", code)
	}

	// Suspend and Relay changes need registry-admin.
	if code, _ := sam.do("POST", "/practices/"+pid+"/suspend", nil); code != http.StatusForbidden {
		t.Errorf("support suspended: %d", code)
	}
	if code, _ := sam.do("PUT", "/relays/"+laptop.ID.String()+"/capacity", map[string]int{"capacity": 10}); code != http.StatusForbidden {
		t.Errorf("support set capacity: %d", code)
	}
	ada := newBrowser(t, srv)
	ada.enrol("ada@wehelp.test", pw)
	if code, out := ada.do("POST", "/practices/"+pid+"/relay", map[string]string{"relay_id": spare.ID.String()}); code != http.StatusOK || out["relay"] != "spare" {
		t.Errorf("move before install: %d %v", code, out)
	}
	ada.do("POST", "/practices/"+pid+"/relay", map[string]string{"relay_id": laptop.ID.String()})
	if code, _ := ada.do("PUT", "/relays/"+laptop.ID.String()+"/capacity", map[string]int{"capacity": 10}); code != http.StatusNoContent {
		t.Errorf("capacity: %d", code)
	}

	// Install the office with a console-issued token; then it can't move.
	code, tok := sam.do("POST", "/practices/"+pid+"/install-token", nil)
	if code != http.StatusCreated {
		t.Fatalf("token: %d %v", code, tok)
	}
	inst, _ := json.Marshal(map[string]string{"install_token": tok["install_token"].(string), "ssh_public_key": sshKeyLine(t)})
	if resp, err := srv.Client().Post(srv.URL+"/v1/install", "application/json", bytes.NewReader(inst)); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("install: %v %v", resp, err)
	}
	if code, _ := ada.do("POST", "/practices/"+pid+"/relay", map[string]string{"relay_id": spare.ID.String()}); code != http.StatusConflict {
		t.Errorf("moved an installed office: %d", code)
	}
	_, detail = sam.do("GET", "/practices/"+pid, nil)
	officeID := detail["office_id"].(string)

	// Health: the Relay's signed report says which offices are connected.
	report := func(at time.Time, offices []string, signer ssh.Signer) int {
		payload, _ := json.Marshal(RelayReport{RelayID: laptop.ID.String(), At: at.Unix(), ListVersion: 3, ConnectedOffices: offices})
		sig, _ := signer.Sign(rand.Reader, payload)
		req, _ := http.NewRequest("POST", srv.URL+"/v1/relay-status", bytes.NewReader(payload))
		req.Header.Set(RelaySignatureHeader, base64.StdEncoding.EncodeToString(ssh.Marshal(sig)))
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(other)
	if code := report(time.Now(), []string{officeID}, otherSigner); code != http.StatusUnauthorized {
		t.Errorf("report signed by another key: %d", code)
	}
	if code := report(time.Now().Add(-10*time.Minute), []string{officeID}, hostSigner); code != http.StatusBadRequest {
		t.Errorf("stale report: %d", code)
	}
	if code := report(time.Now(), []string{officeID}, hostSigner); code != http.StatusNoContent {
		t.Fatalf("report: %d", code)
	}
	var h Health
	resp, _ := sam.client.Get(srv.URL + "/v1/console/health")
	_ = json.NewDecoder(resp.Body).Decode(&h)
	resp.Body.Close()
	if len(h.Offices) != 1 || !h.Offices[0].Connected {
		t.Errorf("office health %+v", h.Offices)
	}
	for _, r := range h.Relays {
		if r.Name == "laptop" && (!r.Up || r.Connected != 1 || r.Capacity != 10) {
			t.Errorf("laptop health %+v", r)
		}
		if r.Name == "spare" && r.Up {
			t.Errorf("spare never reported but is up")
		}
	}

	// Suspension disconnects; the console shows it.
	if code, out := ada.do("POST", "/practices/"+pid+"/suspend", nil); code != http.StatusOK || out["status"] != "suspended" {
		t.Errorf("suspend: %d %v", code, out)
	}
	if code, _ := sam.do("POST", "/practices/"+pid+"/install-token", nil); code != http.StatusConflict {
		t.Errorf("token for a suspended practice: %d", code)
	}

	// Every console action is in the chain under the staff member's name,
	// and the chain verifies.
	_, a := sam.do("GET", "/audit?limit=500", nil)
	seen := map[string]bool{}
	for _, e := range a["events"].([]any) {
		ev := e.(map[string]any)
		seen[ev["actor"].(string)+" "+ev["action"].(string)] = true
	}
	for _, want := range []string{
		"console:sam@wehelp.test practice.create", "console:sam@wehelp.test practice.update",
		"console:sam@wehelp.test provider.add", "console:sam@wehelp.test provider.remove",
		"console:sam@wehelp.test install_token.issue", "console:ada@wehelp.test practice.assign_relay",
		"console:ada@wehelp.test relay.set_capacity", "console:ada@wehelp.test practice.suspend",
		"console:sam@wehelp.test console.signin", "console:sam@wehelp.test console.totp_enabled",
	} {
		if !seen[want] {
			t.Errorf("audit lacks %q", want)
		}
	}
	if broken, _, err := st.VerifyAudit(ctx); err != nil || broken != 0 {
		t.Errorf("audit chain: broken=%d err=%v", broken, err)
	}

	// Five wrong passwords lock the account.
	locked := newBrowser(t, srv)
	for i := 0; i < lockAfter; i++ {
		locked.do("POST", "/login", map[string]string{"email": "ada@wehelp.test", "password": "wrong password!!"})
	}
	code, _ = locked.do("POST", "/login", map[string]string{"email": "ada@wehelp.test", "password": pw, "code": "000000"})
	if code != http.StatusUnauthorized {
		t.Errorf("after lockout: %d", code)
	}
	var until *time.Time
	_ = pool.QueryRow(ctx, `select locked_until from console_staff where email = 'ada@wehelp.test'`).Scan(&until)
	if until == nil || until.Before(time.Now()) {
		t.Error("not locked")
	}

	// The console UI is served with a strict CSP.
	resp, err = srv.Client().Get(srv.URL + "/console/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "WeHelp Registry") || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("console page: %d %s", resp.StatusCode, resp.Header.Get("Content-Security-Policy"))
	}
}
