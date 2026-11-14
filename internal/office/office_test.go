package office

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zacharyelston/wehelp/internal/officelist"
	"golang.org/x/crypto/ssh"
)

func TestEnsureCreatesOnceAndNeverOverwrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "office")
	id, created, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{SSHKeyFile, SSHPubFile, TLSKeyFile, TLSCertFile, CAKeyFile, CACertFile} {
		if !slices.Contains(created, f) {
			t.Errorf("%s not created", f)
		}
	}
	for _, f := range []string{SSHKeyFile, TLSKeyFile, CAKeyFile} {
		st, _ := os.Stat(filepath.Join(dir, f))
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 600", f, st.Mode().Perm())
		}
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v, want 700", st.Mode().Perm())
	}

	again, created2, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(created2) != 0 {
		t.Errorf("second run created %v", created2)
	}
	if !again.SSHKey.Equal(id.SSHKey) || !again.TLSKey.Equal(id.TLSKey) || !again.CAKey.Equal(id.CAKey) {
		t.Fatal("keys changed on second run")
	}
	if again.KeyPin() != id.KeyPin() {
		t.Fatal("pin changed on second run")
	}
}

func TestLocalCA(t *testing.T) {
	id, _, err := Ensure(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := id.CACert
	if !c.IsCA || !c.BasicConstraintsValid || c.MaxPathLen != 0 || !c.MaxPathLenZero || c.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Fatalf("CA cert not a path-length-0 CA: %+v", c)
	}
	if err := c.CheckSignatureFrom(c); err != nil {
		t.Fatal(err)
	}
}

// The pin is SHA-256 over the certificate SPKI's subjectPublicKey bits, the
// same bytes iOS SecKeyCopyExternalRepresentation gives the app.
func TestKeyPinMatchesCertificateSPKI(t *testing.T) {
	dir := t.TempDir()
	id, _, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, TLSCertFile), filepath.Join(dir, TLSKeyFile))
	if err != nil {
		t.Fatalf("tls.crt and tls.key don't load as a pair: %v", err)
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	var spki struct {
		Algorithm asn1.RawValue
		PublicKey asn1.BitString
	}
	if _, err := asn1.Unmarshal(leaf.RawSubjectPublicKeyInfo, &spki); err != nil {
		t.Fatal(err)
	}
	if len(spki.PublicKey.Bytes) != 65 || spki.PublicKey.Bytes[0] != 0x04 {
		t.Fatalf("unexpected key encoding (%d bytes)", len(spki.PublicKey.Bytes))
	}
	sum := sha256.Sum256(spki.PublicKey.Bytes)
	if want := base64.StdEncoding.EncodeToString(sum[:]); id.KeyPin() != want {
		t.Fatalf("pin %s, want %s", id.KeyPin(), want)
	}
	if !slices.Equal(leaf.DNSNames, []string{"office.wehelp.invalid"}) {
		t.Errorf("pre-install names %v", leaf.DNSNames)
	}
}

func fakeRegistry(t *testing.T, status int, resp any, got *map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/install" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func validResponse(t *testing.T) map[string]any {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	hostPub, _, _ := ed25519.GenerateKey(rand.Reader)
	hk, _ := ssh.NewPublicKey(hostPub)
	return map[string]any{
		"office_id": "8c1f5a8e-4f40-4b6e-9b8e-2f7b3a1d9c10",
		"relay": map[string]string{"id": "r1", "name": "laptop", "ssh_address": "relay.test:2222",
			"connect_address": "relay.test:8443", "ssh_host_key": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hk))),
			"tls_pin": "Ar1cxddT3OFraDM2dIXtqUQKiaz5jzUiiH/IBXyI8Ik="},
		"registry_signing_key": officelist.PublicKeyString(pub),
	}
}

func TestInstall(t *testing.T) {
	dir := t.TempDir()
	id, _, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	pinBefore := id.KeyPin()
	if _, err := id.Pairing(); err == nil {
		t.Error("pairing identity before install")
	}

	var sent map[string]string
	srv := fakeRegistry(t, http.StatusOK, validResponse(t), &sent)
	r, err := id.Install(context.Background(), srv.Client(), srv.URL+"/", "wht_tok")
	if err != nil {
		t.Fatal(err)
	}
	if sent["install_token"] != "wht_tok" || sent["ssh_public_key"] != id.SSHAuthorizedKey() {
		t.Errorf("sent %v", sent)
	}
	if r.Relay.ConnectAddress != "relay.test:8443" || r.RegistryURL != srv.URL {
		t.Errorf("registration %+v", r)
	}
	if st, _ := os.Stat(filepath.Join(dir, RegistrationFile)); st.Mode().Perm() != 0o600 {
		t.Errorf("registration mode %v", st.Mode().Perm())
	}

	// Reload from disk: registration persists, the cert names the office
	// host, and the pin (the key) is unchanged.
	re, created, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 0 {
		t.Errorf("reload created %v", created)
	}
	p, err := re.Pairing()
	if err != nil {
		t.Fatal(err)
	}
	if p.OfficeID != r.OfficeID || p.RelayAddress != "relay.test:8443" || p.KeyPin != pinBefore || p.RelayPin != "Ar1cxddT3OFraDM2dIXtqUQKiaz5jzUiiH/IBXyI8Ik=" {
		t.Errorf("pairing %+v", p)
	}
	cert, err := readCert(filepath.Join(dir, TLSCertFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.VerifyHostname(r.OfficeID + ".wehelp.invalid"); err != nil {
		t.Errorf("cert does not cover the office host: %v", err)
	}
}

func TestInstallRefusals(t *testing.T) {
	id, _, err := Ensure(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]string
	refused := fakeRegistry(t, http.StatusUnauthorized, map[string]string{"error": "invalid install token"}, &sent)
	if _, err := id.Install(context.Background(), refused.Client(), refused.URL, "wht_x"); err == nil || !strings.Contains(err.Error(), "invalid install token") {
		t.Errorf("refused install: err = %v", err)
	}

	for name, mutate := range map[string]func(map[string]any){
		"bad signing key": func(m map[string]any) { m["registry_signing_key"] = "ed25519:nope" },
		"bad office id":   func(m map[string]any) { m["office_id"] = "../../etc" },
		"bad host key":    func(m map[string]any) { m["relay"].(map[string]string)["ssh_host_key"] = "junk" },
		"no relay pin":    func(m map[string]any) { m["relay"].(map[string]string)["tls_pin"] = "" },
		"no relay":        func(m map[string]any) { m["relay"].(map[string]string)["connect_address"] = "" },
	} {
		resp := validResponse(t)
		mutate(resp)
		srv := fakeRegistry(t, http.StatusOK, resp, &sent)
		if _, err := id.Install(context.Background(), srv.Client(), srv.URL, "wht_x"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if id.Registration != nil {
		t.Error("a refused install saved a registration")
	}
	if _, err := id.Install(context.Background(), http.DefaultClient, "ftp://x", "wht_x"); err == nil {
		t.Error("non-http registry URL accepted")
	}
}
