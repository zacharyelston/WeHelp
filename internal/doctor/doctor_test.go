package doctor

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zacharyelston/wehelp/internal/office"
)

// The real clock: the test certificates are issued now.
var now = time.Now()

// healthy is a Back Office where every check passes: an office directory
// from office.Ensure with a registration, a /readyz that reports the tunnel
// connected, staff HTTPS from the office CA, and good database answers.
func healthy(t *testing.T) Deps {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "office")
	id, _, err := office.Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := json.Marshal(office.Registration{OfficeID: "office-1", RegistryURL: "https://registry.test",
		Relay: office.Relay{Name: "relay-1"}})
	if err := os.WriteFile(filepath.Join(dir, office.RegistrationFile), reg, 0o600); err != nil {
		t.Fatal(err)
	}

	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","relay":"connected"}`))
	}))
	t.Cleanup(ready.Close)

	cert, err := id.StaffCert([]string{"localhost", "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	port := serveTLS(t, cert)

	return Deps{
		OfficeDir: dir, RelayTunnel: true, ReadyURL: ready.URL + "/readyz",
		StaffTLSAddr: ":" + port, Env: "production", Now: func() time.Time { return now },
		VerifyAudit:  func(context.Context) (int64, int, error) { return 396, 0, nil },
		LastBackup:   func(context.Context) (time.Time, bool, error) { return now.Add(-5 * time.Hour), true, nil },
		SeedAccounts: func(context.Context) (int, error) { return 0, nil },
	}
}

func serveTLS(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = c.(*tls.Conn).Handshake(); c.Close() }()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}

func byName(r Report) map[string]Check {
	m := map[string]Check{}
	for _, c := range r.Checks {
		m[c.Name] = c
	}
	return m
}

func TestAllPass(t *testing.T) {
	r := Run(context.Background(), Checks(healthy(t)))
	if !r.OK {
		var b bytes.Buffer
		r.Text(&b)
		t.Fatalf("not OK:\n%s", b.String())
	}
	got := byName(r)
	for _, name := range []string{"office_keys", "registry_install", "relay_tunnel", "audit_chain",
		"backup_recent", "demo_off", "no_seed_accounts", "staff_https"} {
		if got[name].Status != Pass {
			t.Errorf("%s = %+v, want PASS", name, got[name])
		}
	}
	if got["restore_test"].Status != Skip {
		t.Errorf("restore_test = %+v, want SKIP", got["restore_test"])
	}
}

// Each failure on its own: the one check fails, the report isn't OK.
func TestEachFailure(t *testing.T) {
	cases := map[string]func(d *Deps){
		"office_keys": func(d *Deps) { _ = os.Chmod(d.OfficeDir, 0o755) },
		"registry_install": func(d *Deps) {
			_ = os.Remove(filepath.Join(d.OfficeDir, office.RegistrationFile))
		},
		"relay_tunnel": func(d *Deps) { d.RelayTunnel = false },
		"audit_chain": func(d *Deps) {
			d.VerifyAudit = func(context.Context) (int64, int, error) { return 10, 1, nil }
		},
		"backup_recent": func(d *Deps) {
			d.LastBackup = func(context.Context) (time.Time, bool, error) { return now.Add(-30 * time.Hour), true, nil }
		},
		"demo_off":         func(d *Deps) { d.DemoBuild = true },
		"no_seed_accounts": func(d *Deps) { d.SeedAccounts = func(context.Context) (int, error) { return 6, nil } },
		"staff_https":      func(d *Deps) { d.StaffTLSAddr = "" },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			d := healthy(t)
			breakIt(&d)
			r := Run(context.Background(), Checks(d))
			if r.OK {
				t.Error("report OK with a failure")
			}
			for _, c := range r.Checks {
				if c.Name == name && c.Status != Fail {
					t.Errorf("%s = %+v, want FAIL", name, c)
				}
				if c.Name != name && c.Status == Fail {
					t.Errorf("%s also failed: %+v", c.Name, c)
				}
			}
		})
	}
}

func TestMoreFailures(t *testing.T) {
	ctx := context.Background()
	d := healthy(t)

	// The tunnel down, and a server too old to say.
	for body, want := range map[string]string{`{"status":"ok","relay":"down"}`: "tunnel down", `{"status":"ok"}`: "doesn't report"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		d.ReadyURL = srv.URL
		if c := d.relayTunnel(ctx); c.Status != Fail || !strings.Contains(c.Detail, want) {
			t.Errorf("readyz %s: %+v", body, c)
		}
		srv.Close()
	}
	d.ReadyURL = "http://127.0.0.1:1/readyz"
	if c := d.relayTunnel(ctx); c.Status != Fail {
		t.Errorf("server down: %+v", c)
	}

	// No backup ever, and a database error.
	d.LastBackup = func(context.Context) (time.Time, bool, error) { return time.Time{}, false, nil }
	if c := d.backup(ctx); c.Status != Fail || !strings.Contains(c.Detail, "no successful backup") {
		t.Errorf("no backup: %+v", c)
	}
	d.VerifyAudit = func(context.Context) (int64, int, error) { return 0, 0, errors.New("boom") }
	if c := d.auditChain(ctx); c.Status != Fail {
		t.Errorf("audit error: %+v", c)
	}

	// The dev or demo env, even in a field build.
	d.Env = "demo"
	if c := d.demoOff(ctx); c.Status != Fail {
		t.Errorf("env demo: %+v", c)
	}

	// A key readable by others.
	_ = os.Chmod(filepath.Join(d.OfficeDir, office.TLSKeyFile), 0o644)
	if c := d.officeKeys(ctx); c.Status != Fail || !strings.Contains(c.Detail, office.TLSKeyFile) {
		t.Errorf("tls.key 0644: %+v", c)
	}
}

// A certificate from some other CA fails the staff HTTPS check.
func TestStaffHTTPSOtherCA(t *testing.T) {
	d := healthy(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	d.StaffTLSAddr = ":" + serveTLS(t, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key})
	if c := d.staffHTTPS(context.Background()); c.Status != Fail || !strings.Contains(c.Detail, "office CA") {
		t.Errorf("self-signed: %+v", c)
	}
}

func TestHostChecksAndOutput(t *testing.T) {
	host := "filevault|PASS|on\nauto_login|FAIL|automatic login is on\npostgres_loopback|SKIP|x\ngarbage line\n"
	r := Run(context.Background(), ParseHost(host))
	if r.OK || len(r.Checks) != 4 {
		t.Fatalf("report %+v", r)
	}
	if r.Checks[0] != (Check{"filevault", Pass, "on"}) || r.Checks[1].Status != Fail || r.Checks[2].Status != Skip {
		t.Errorf("parsed %+v", r.Checks)
	}
	if r.Checks[3].Status != Fail || !strings.Contains(r.Checks[3].Detail, "unreadable") {
		t.Errorf("garbage = %+v, want FAIL", r.Checks[3])
	}

	var text bytes.Buffer
	r.Text(&text)
	if !strings.Contains(text.String(), "FAIL  auto_login") || !strings.Contains(text.String(), "NOT READY") {
		t.Errorf("text:\n%s", text.String())
	}
	var js bytes.Buffer
	if err := r.JSON(&js); err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(js.Bytes(), &back); err != nil || back.OK || len(back.Checks) != 4 {
		t.Errorf("json %s: %v", js.String(), err)
	}

	// A crashing check fails instead of stopping the report.
	r = Run(context.Background(), []Func{func(context.Context) Check { panic("bad") }})
	if r.OK || r.Checks[0].Status != Fail {
		t.Errorf("panic: %+v", r)
	}
}
