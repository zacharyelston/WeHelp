package relay

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TestReport is #61 on the Relay side: the status report carries the list
// version and connected office IDs, signed with the host key.
func TestReport(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	regPub, _, _ := ed25519.GenerateKey(rand.Reader)

	var got statusReport
	var verified bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		raw, _ := base64.StdEncoding.DecodeString(r.Header.Get(SignatureHeader))
		sig := new(ssh.Signature)
		verified = r.URL.Path == "/v1/relay-status" && ssh.Unmarshal(raw, sig) == nil && signer.PublicKey().Verify(body, sig) == nil
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	r, err := New(Config{RelayID: "relay-1", HostKey: signer, RegistryKey: regPub, RegistryURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.version = 7
	r.offices["office-b"] = &officeConn{}
	r.offices["office-a"] = &officeConn{}
	r.mu.Unlock()

	if err := r.Report(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !verified {
		t.Fatal("report signature didn't verify against the host key")
	}
	if got.RelayID != "relay-1" || got.ListVersion != 7 || len(got.ConnectedOffices) != 2 || got.ConnectedOffices[0] != "office-a" {
		t.Errorf("report %+v", got)
	}
}
