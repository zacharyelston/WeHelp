package registry

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zacharyelston/wehelp/internal/roster"
	"golang.org/x/crypto/ssh"
)

// TestRoster is #62's Registry half: an installed office signs a roster
// request with its office key and gets only its own practice's providers,
// removed ones flagged, signed with the Registry key.
func TestRoster(t *testing.T) {
	ctx := context.Background()
	pool := testDB(t)
	key := newSigner(t)
	st := New(pool, key)
	srv := httptest.NewServer(NewServer(st).Router())
	defer srv.Close()
	const actor = "cli:test"
	if err := st.EnsureList(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRelay(ctx, actor, Relay{Name: "laptop", SSHAddress: "relay.test:2222",
		ConnectAddress: "relay.test:8443", SSHHostKey: sshKeyLine(t), TLSPin: testPin}); err != nil {
		t.Fatal(err)
	}

	// Two practices, each with an installed office.
	install := func(name string) (Practice, ed25519.PrivateKey, string) {
		p, err := st.AddPractice(ctx, actor, name, "laptop")
		if err != nil {
			t.Fatal(err)
		}
		_, officeKey, _ := ed25519.GenerateKey(rand.Reader)
		pk, _ := ssh.NewPublicKey(officeKey.Public())
		tok, _, err := st.IssueInstallToken(ctx, actor, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		code, res := postInstall(t, srv, tok, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk))))
		if code != http.StatusOK {
			t.Fatalf("install %s: %d", name, code)
		}
		return p, officeKey, res.OfficeID.String()
	}
	demo, demoKey, demoOffice := install("Demo")
	other, otherKey, otherOffice := install("Other")

	add := func(practice Practice, name, email, role string) Provider {
		p, err := st.AddProvider(ctx, "console:support@wehelp.test", Provider{PracticeID: practice.ID, Name: name, Email: email,
			Role: role, Credentials: "ND", LicenceNumber: "L-1"})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	ada := add(demo, "Dr. Ada", "ada@demo.test", "provider")
	add(demo, "Office Manager", "admin@demo.test", "admin")
	gone := add(demo, "Dr. Gone", "gone@demo.test", "provider")
	if err := st.RemoveProvider(ctx, actor, gone.ID); err != nil {
		t.Fatal(err)
	}
	add(other, "Dr. Elsewhere", "else@other.test", "provider")

	client := func(officeID string, officeKey ed25519.PrivateKey) *roster.Client {
		return &roster.Client{HTTP: srv.Client(), RegistryURL: srv.URL, OfficeID: officeID, OfficeKey: officeKey,
			RegistryKey: key.Public().(ed25519.PublicKey)}
	}
	r, err := client(demoOffice, demoKey).Fetch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byEmail := map[string]roster.Provider{}
	for _, p := range r.Providers {
		byEmail[p.Email] = p
	}
	if len(r.Providers) != 3 || byEmail["else@other.test"].ID != "" {
		t.Fatalf("demo roster = %+v, want its 3 entries only", r.Providers)
	}
	if p := byEmail["ada@demo.test"]; p.ID != ada.ID.String() || p.Role != "provider" || p.Credentials != "ND" || p.RemovedAt != nil {
		t.Errorf("ada = %+v", p)
	}
	if byEmail["gone@demo.test"].RemovedAt == nil {
		t.Error("removed provider not flagged")
	}
	if byEmail["admin@demo.test"].Role != "admin" {
		t.Error("admin role lost")
	}
	if r2, err := client(otherOffice, otherKey).Fetch(ctx); err != nil || len(r2.Providers) != 1 {
		t.Errorf("other roster = %+v, %v", r2, err)
	}

	// Another office's key, a made-up office, a wrong Registry key: refused.
	if _, err := client(demoOffice, otherKey).Fetch(ctx); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("other office's key: %v, want 401", err)
	}
	if _, err := client("00000000-0000-0000-0000-000000000001", demoKey).Fetch(ctx); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("unknown office: %v, want 401", err)
	}
	wrong := client(demoOffice, demoKey)
	wrong.RegistryKey = newSigner(t).Public().(ed25519.PublicKey)
	if _, err := wrong.Fetch(ctx); !errors.Is(err, roster.ErrBadSignature) {
		t.Errorf("wrong Registry key: %v", err)
	}

	// An old signed request is refused.
	body, sig, _ := roster.SignRequest(demoKey, demoOffice, time.Now().Add(-10*time.Minute))
	req, _ := http.NewRequest("POST", srv.URL+"/v1/roster", bytes.NewReader(body))
	req.Header.Set(roster.SignatureHeader, sig)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("stale request: %d, want 400", resp.StatusCode)
	}

	// A suspended practice gets no roster.
	if err := st.SetPracticeStatus(ctx, actor, demo.ID, "suspended"); err != nil {
		t.Fatal(err)
	}
	if _, err := client(demoOffice, demoKey).Fetch(ctx); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("suspended: %v, want 403", err)
	}
}
