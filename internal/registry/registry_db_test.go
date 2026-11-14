package registry

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zacharyelston/wehelp/internal/officelist"
	"golang.org/x/crypto/ssh"
)

// These tests need Postgres. Set WEHELP_TEST_DATABASE_URL to a URL whose
// user may create databases (CI sets it); each test gets a fresh database
// with db/registry migrations applied and dropped afterwards.

func testDB(t *testing.T) *pgxpool.Pool {
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
	name := "wehelp_registry_test_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
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
	files, _ := filepath.Glob(filepath.Join("..", "..", "db", "registry", "V*.sql"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no migrations found")
	}
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	return pool
}

const testPin = "Ar1cxddT3OFraDM2dIXtqUQKiaz5jzUiiH/IBXyI8Ik="

func newSigner(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func sshKeyLine(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
}

func postInstall(t *testing.T, srv *httptest.Server, token, key string) (int, InstallResult) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"install_token": token, "ssh_public_key": key})
	resp, err := http.Post(srv.URL+"/v1/install", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var res InstallResult
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, res
}

func fetchList(t *testing.T, srv *httptest.Server, pub ed25519.PublicKey, lastGood int64) officelist.List {
	t.Helper()
	resp, err := http.Get(srv.URL + "/v1/office-list")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("office-list status %d", resp.StatusCode)
	}
	var env officelist.Envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	l, err := officelist.Open(pub, env, lastGood)
	if err != nil {
		t.Fatalf("verify list: %v", err)
	}
	return l
}

func hasOffice(l officelist.List, id uuid.UUID) bool {
	for _, o := range l.Offices {
		if o.OfficeID == id.String() {
			return true
		}
	}
	return false
}

// TestRegistryFlow is the #60 verification: register a practice, install
// with a token, see the office in a signed list, suspend it and see a newer
// list without it.
func TestRegistryFlow(t *testing.T) {
	ctx := context.Background()
	pool := testDB(t)
	key := newSigner(t)
	pub := key.Public().(ed25519.PublicKey)
	st := New(pool, key)
	srv := httptest.NewServer(NewServer(st).Router())
	defer srv.Close()
	const actor = "cli:test"

	if err := st.EnsureList(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureList(ctx); err != nil { // idempotent
		t.Fatal(err)
	}
	l1 := fetchList(t, srv, pub, 0)
	if l1.Version != 1 || len(l1.Offices) != 0 {
		t.Fatalf("initial list = %+v", l1)
	}

	relay, err := st.AddRelay(ctx, actor, Relay{Name: "laptop", SSHAddress: "relay.test:2222",
		ConnectAddress: "relay.test:8443", SSHHostKey: sshKeyLine(t) + " relay@host", TLSPin: testPin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRelay(ctx, actor, Relay{Name: "bad", SSHAddress: "nohostport",
		ConnectAddress: "x:1", SSHHostKey: sshKeyLine(t), TLSPin: testPin}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bad relay address: err = %v", err)
	}
	p, err := st.AddPractice(ctx, actor, "Demo Naturopathy", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPractice(ctx, actor, "X", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown relay: err = %v", err)
	}

	token, exp, err := st.IssueInstallToken(ctx, actor, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(exp); d < 23*time.Hour || d > 25*time.Hour {
		t.Errorf("token expires in %v, want ~24h", d)
	}
	var stored int
	_ = pool.QueryRow(ctx, `select count(*) from install_tokens where token_hash = $1`, []byte(token)).Scan(&stored)
	if stored != 0 {
		t.Error("plaintext token stored")
	}

	officeKey := sshKeyLine(t)
	if code, _ := postInstall(t, srv, "wht_not-a-real-token", officeKey); code != http.StatusUnauthorized {
		t.Errorf("unknown token: status %d", code)
	}
	if code, _ := postInstall(t, srv, token, "ssh-rsa AAAAB3NzaC1yc2E"); code != http.StatusBadRequest {
		t.Errorf("non-ed25519 key: status %d", code)
	}
	code, res := postInstall(t, srv, token, officeKey+" office@barnacle")
	if code != http.StatusOK {
		t.Fatalf("install: status %d", code)
	}
	if res.Relay.ID != relay.ID || res.Relay.ConnectAddress != "relay.test:8443" || res.Relay.SSHHostKey != relay.SSHHostKey || res.Relay.TLSPin != testPin {
		t.Errorf("install relay = %+v, want %+v", res.Relay, relay)
	}
	if got, err := officelist.ParsePublicKey(res.RegistrySigningKey); err != nil || !got.Equal(pub) {
		t.Errorf("registry_signing_key = %q", res.RegistrySigningKey)
	}
	if code, _ := postInstall(t, srv, token, sshKeyLine(t)); code != http.StatusUnauthorized {
		t.Errorf("reused token: status %d", code)
	}

	l2 := fetchList(t, srv, pub, l1.Version)
	if !hasOffice(l2, res.OfficeID) {
		t.Fatalf("office %s missing from list %+v", res.OfficeID, l2)
	}
	if l2.Offices[0].SSHPublicKey != officeKey || l2.Offices[0].RelayID != relay.ID.String() {
		t.Errorf("list entry = %+v", l2.Offices[0])
	}

	// Reinstall (key rotation) keeps the office ID and swaps the key.
	token2, _, err := st.IssueInstallToken(ctx, actor, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	newKey := sshKeyLine(t)
	code, res2 := postInstall(t, srv, token2, newKey)
	if code != http.StatusOK || res2.OfficeID != res.OfficeID {
		t.Fatalf("reinstall: status %d, office %s (want %s)", code, res2.OfficeID, res.OfficeID)
	}
	l3 := fetchList(t, srv, pub, l2.Version)
	if len(l3.Offices) != 1 || l3.Offices[0].SSHPublicKey != newKey {
		t.Fatalf("after reinstall list = %+v", l3)
	}

	// Another practice can't claim this office's key.
	p2, _ := st.AddPractice(ctx, actor, "Other", "laptop")
	token3, _, _ := st.IssueInstallToken(ctx, actor, p2.ID)
	if code, _ := postInstall(t, srv, token3, newKey); code != http.StatusConflict {
		t.Errorf("duplicate key: status %d", code)
	}

	// Suspend: a newer list without the office, and no new tokens.
	if err := st.SetPracticeStatus(ctx, actor, p.ID, "suspended"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPracticeStatus(ctx, actor, p.ID, "suspended"); !errors.Is(err, ErrNoChange) {
		t.Errorf("double suspend: err = %v", err)
	}
	l4 := fetchList(t, srv, pub, l3.Version)
	if hasOffice(l4, res.OfficeID) {
		t.Fatal("suspended office still listed")
	}
	if _, _, err := st.IssueInstallToken(ctx, actor, p.ID); !errors.Is(err, ErrNotActive) {
		t.Errorf("token for suspended practice: err = %v", err)
	}
	if err := st.SetPracticeStatus(ctx, actor, p.ID, "active"); err != nil {
		t.Fatal(err)
	}
	if l5 := fetchList(t, srv, pub, l4.Version); !hasOffice(l5, res.OfficeID) {
		t.Fatal("reactivated office not listed")
	}

	// Expired tokens are refused.
	st.now = func() time.Time { return time.Now().Add(-InstallTokenTTL - time.Minute) }
	old, _, err := st.IssueInstallToken(ctx, actor, p2.ID)
	st.now = time.Now
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := postInstall(t, srv, old, sshKeyLine(t)); code != http.StatusUnauthorized {
		t.Errorf("expired token: status %d", code)
	}

	// Audit: intact chain, append-only.
	broken, n, err := st.VerifyAudit(ctx)
	if err != nil || broken != 0 {
		t.Fatalf("audit verify: broken=%d err=%v", broken, err)
	}
	if n < 10 {
		t.Errorf("only %d audit events", n)
	}
	if _, err := pool.Exec(ctx, `update audit_events set actor = 'x' where seq = 1`); err == nil {
		t.Error("audit_events update allowed")
	}
	if _, err := pool.Exec(ctx, `delete from office_lists where version = 1`); err == nil {
		t.Error("office_lists delete allowed")
	}
}

// TestAuditDetectsTampering breaks the chain with the trigger disabled, as
// a database superuser could, and expects verification to catch it.
func TestAuditDetectsTampering(t *testing.T) {
	ctx := context.Background()
	pool := testDB(t)
	st := New(pool, newSigner(t))
	for i := 0; i < 3; i++ {
		if _, err := st.AddRelay(ctx, "cli:test", Relay{Name: fmt.Sprint("r", i), SSHAddress: "h:1",
			ConnectAddress: "h:2", SSHHostKey: sshKeyLine(t), TLSPin: testPin}); err != nil {
			t.Fatal(err)
		}
	}
	if broken, _, _ := st.VerifyAudit(ctx); broken != 0 {
		t.Fatalf("fresh chain broken at %d", broken)
	}
	_, err := pool.Exec(ctx, `
		alter table audit_events disable trigger audit_events_immutable;
		update audit_events set detail = '{"name": "forged"}' where seq = 2;
		alter table audit_events enable trigger audit_events_immutable;`)
	if err != nil {
		t.Fatal(err)
	}
	if broken, _, _ := st.VerifyAudit(ctx); broken != 2 {
		t.Fatalf("tampering not detected at seq 2 (got %d)", broken)
	}
}

// TestConcurrentPublishVersions: concurrent changes each get their own,
// strictly increasing list version, and the newest list reflects them all.
func TestConcurrentPublishVersions(t *testing.T) {
	ctx := context.Background()
	pool := testDB(t)
	key := newSigner(t)
	st := New(pool, key)
	if _, err := st.AddRelay(ctx, "cli:test", Relay{Name: "r", SSHAddress: "h:1", ConnectAddress: "h:2", SSHHostKey: sshKeyLine(t), TLSPin: testPin}); err != nil {
		t.Fatal(err)
	}
	const n = 8
	ids := make([]uuid.UUID, n)
	for i := range ids {
		p, err := st.AddPractice(ctx, "cli:test", fmt.Sprint("p", i), "r")
		if err != nil {
			t.Fatal(err)
		}
		tok, _, err := st.IssueInstallToken(ctx, "cli:test", p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.Install(ctx, tok, sshKeyLine(t)); err != nil {
			t.Fatal(err)
		}
		ids[i] = p.ID
	}
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- st.SetPracticeStatus(ctx, "cli:test", id, "suspended")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count, max int64
	if err := pool.QueryRow(ctx, `select count(*), max(version) from office_lists`).Scan(&count, &max); err != nil {
		t.Fatal(err)
	}
	if count != max || count != 2*n {
		t.Errorf("lists: count=%d max=%d, want %d of each", count, max, 2*n)
	}
	env, err := st.LatestList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	l, err := officelist.Open(key.Public().(ed25519.PublicKey), env, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Offices) != 0 {
		t.Errorf("latest list still has %d offices", len(l.Offices))
	}
	if broken, _, _ := st.VerifyAudit(ctx); broken != 0 {
		t.Errorf("audit broken at %d", broken)
	}
}
