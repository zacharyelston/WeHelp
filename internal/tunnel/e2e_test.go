package tunnel_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/keyfile"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/officelist"
	"github.com/zacharyelston/wehelp/internal/relay"
	"github.com/zacharyelston/wehelp/internal/tunnel"
	"golang.org/x/crypto/ssh"
)

// fakeRegistry serves whatever signed list it was last given.
type fakeRegistry struct {
	key     ed25519.PrivateKey
	mu      sync.Mutex
	env     officelist.Envelope
	version int64
}

func (f *fakeRegistry) publish(t *testing.T, version int64, offices ...officelist.Office) {
	t.Helper()
	payload, sig, err := officelist.Sign(f.key, officelist.List{Version: version, IssuedAt: time.Now().UTC(), Offices: offices})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.env, f.version = officelist.Seal(payload, sig), version
	f.mu.Unlock()
}

func (f *fakeRegistry) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(f.env)
}

// recordingListener keeps every byte the Relay's CONNECT side sees.
type recordingListener struct {
	net.Listener
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *recordingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &recordingConn{Conn: c, l: l}, nil
}

func (l *recordingListener) bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte{}, l.buf.Bytes()...)
}

type recordingConn struct {
	net.Conn
	l *recordingListener
}

func (c *recordingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.l.mu.Lock()
	c.l.buf.Write(p[:n])
	c.l.mu.Unlock()
	return n, err
}

func (c *recordingConn) Write(p []byte) (int, error) {
	c.l.mu.Lock()
	c.l.buf.Write(p)
	c.l.mu.Unlock()
	return c.Conn.Write(p)
}

type harness struct {
	reg        *fakeRegistry
	rl         *relay.Relay
	keys       *relay.Keys
	relayID    string
	sshAddr    string
	connect    *recordingListener
	registry   *httptest.Server
	cancelRest context.CancelFunc
	cache      string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	_, regKey, _ := ed25519.GenerateKey(rand.Reader)
	h := &harness{reg: &fakeRegistry{key: regKey}, relayID: uuid.NewString()}
	h.reg.publish(t, 1)
	h.registry = httptest.NewServer(h.reg)
	t.Cleanup(h.registry.Close)

	keys, _, err := relay.EnsureKeys(filepath.Join(t.TempDir(), "relay"))
	if err != nil {
		t.Fatal(err)
	}
	h.keys = keys
	h.cache = filepath.Join(t.TempDir(), "cache.json")
	h.rl, err = relay.New(relay.Config{
		RelayID: h.relayID, HostKey: keys.HostKey, TLSCert: keys.TLSCert,
		RegistryURL: h.registry.URL, RegistryKey: regKey.Public().(ed25519.PublicKey),
		CachePath: h.cache, ListInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	sshLn, _ := net.Listen("tcp", "127.0.0.1:0")
	cl, _ := net.Listen("tcp", "127.0.0.1:0")
	h.connect = &recordingListener{Listener: cl}
	h.sshAddr = sshLn.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancelRest = cancel
	go func() { _ = h.rl.Serve(ctx, sshLn, h.connect) }()
	t.Cleanup(cancel)
	return h
}

// newOffice creates an installed office identity assigned to this Relay.
func (h *harness) newOffice(t *testing.T) *office.Identity {
	t.Helper()
	id, _, err := office.Ensure(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = id.SaveRegistration(office.Registration{
		OfficeID: uuid.NewString(), RegistryURL: h.registry.URL,
		RegistrySigningKey: officelist.PublicKeyString(h.reg.key.Public().(ed25519.PublicKey)),
		Relay: office.Relay{ID: h.relayID, Name: "test", SSHAddress: h.sshAddr, ConnectAddress: h.connect.Addr().String(),
			SSHHostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(h.keys.HostKey.PublicKey()))), TLSPin: h.keys.TLSPin},
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func listed(id *office.Identity, relayID string) officelist.Office {
	return officelist.Office{OfficeID: id.Registration.OfficeID, SSHPublicKey: id.SSHAuthorizedKey(), RelayID: relayID}
}

func runOffice(t *testing.T, id *office.Identity) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ping", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "pong-secret via_relay=%v", tunnel.ViaRelay(r.Context()))
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = tunnel.Run(ctx, id, mux, tunnel.Options{MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, Keepalive: time.Second})
	}()
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// phone returns an HTTP client that reaches offices through the Relay,
// pinning the Relay key on the outer TLS and the office key on the inner.
func (h *harness) phone(officePin string) *http.Client {
	verify := func(cs tls.ConnectionState) error {
		pub, ok := cs.PeerCertificates[0].PublicKey.(*ecdsa.PublicKey)
		if !ok {
			return errors.New("not P-256")
		}
		want := officePin
		if !strings.HasSuffix(cs.ServerName, ".wehelp.invalid") || cs.ServerName == "relay.wehelp.invalid" {
			want = h.keys.TLSPin
		}
		if keyfile.P256Pin(pub) != want {
			return fmt.Errorf("pin mismatch for %s", cs.ServerName)
		}
		return nil
	}
	proxy, _ := url.Parse("https://" + h.connect.Addr().String())
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		Proxy:             http.ProxyURL(proxy),
		ForceAttemptHTTP2: false,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, VerifyConnection: verify, // pins replace CA checks
		},
	}}
}

func get(c *http.Client, officeID string) (string, error) {
	resp, err := c.Get("https://" + officeID + ".wehelp.invalid/api/v1/ping")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

func TestPhoneReachesOfficeThroughRelay(t *testing.T) {
	h := newHarness(t)
	id := h.newOffice(t)
	h.reg.publish(t, 2, listed(id, h.relayID))
	eventually(t, "list v2", func() bool { return h.rl.Version() == 2 })
	runOffice(t, id)
	eventually(t, "office connected", func() bool { return h.rl.Connected(id.Registration.OfficeID) })

	body, err := get(h.phone(id.KeyPin()), id.Registration.OfficeID)
	if err != nil {
		t.Fatal(err)
	}
	if body != "pong-secret via_relay=true" {
		t.Fatalf("body %q", body)
	}
	seen := h.connect.bytes()
	for _, plain := range []string{"/api/v1/ping", "pong-secret", "via_relay"} {
		if bytes.Contains(seen, []byte(plain)) {
			t.Errorf("relay saw plaintext %q", plain)
		}
	}

	// Pins fail closed: a wrong office pin is refused.
	if _, err := get(h.phone("AAAA"), id.Registration.OfficeID); err == nil {
		t.Error("wrong office pin accepted")
	}
	// An office that isn't connected is a gateway error, not a hang.
	if _, err := get(h.phone(id.KeyPin()), uuid.NewString()); err == nil {
		t.Error("unknown office reachable")
	}
}

func TestRelayRefusesUnlistedAndDelistedOffices(t *testing.T) {
	h := newHarness(t)
	listedOffice, other := h.newOffice(t), h.newOffice(t)
	// Listed for a different Relay: this Relay must not accept it.
	h.reg.publish(t, 2, listed(listedOffice, h.relayID), listed(other, uuid.NewString()))
	eventually(t, "list v2", func() bool { return h.rl.Version() == 2 })
	runOffice(t, listedOffice)
	runOffice(t, other)
	eventually(t, "listed office connected", func() bool { return h.rl.Connected(listedOffice.Registration.OfficeID) })
	time.Sleep(300 * time.Millisecond)
	if h.rl.Connected(other.Registration.OfficeID) {
		t.Fatal("office listed for another Relay was accepted")
	}

	// Suspension: the next list leaves the office out and it is dropped.
	h.reg.publish(t, 3)
	eventually(t, "office dropped", func() bool { return !h.rl.Connected(listedOffice.Registration.OfficeID) })
	time.Sleep(300 * time.Millisecond) // the tunnel keeps retrying and keeps being refused
	if h.rl.Connected(listedOffice.Registration.OfficeID) {
		t.Fatal("delisted office reconnected")
	}

	// Reinstated: it reconnects by itself.
	h.reg.publish(t, 4, listed(listedOffice, h.relayID))
	eventually(t, "office back", func() bool { return h.rl.Connected(listedOffice.Registration.OfficeID) })
}

func TestListRollbackAndForgeryRejected(t *testing.T) {
	h := newHarness(t)
	id := h.newOffice(t)
	h.reg.publish(t, 5, listed(id, h.relayID))
	eventually(t, "list v5", func() bool { return h.rl.Version() == 5 })
	h.cancelRest() // stop the background sync; drive Sync by hand

	h.reg.publish(t, 3) // older
	if _, err := h.rl.Sync(context.Background()); !errors.Is(err, relay.ErrOlderList) {
		t.Errorf("older list: err = %v", err)
	}
	_, forger, _ := ed25519.GenerateKey(rand.Reader)
	real := h.reg.key
	h.reg.key = forger
	h.reg.publish(t, 9) // newer but signed by the wrong key
	h.reg.key = real
	if _, err := h.rl.Sync(context.Background()); !errors.Is(err, officelist.ErrBadSignature) {
		t.Errorf("forged list: err = %v", err)
	}
	if v := h.rl.Version(); v != 5 {
		t.Fatalf("version %d after rejected lists, want 5", v)
	}

	// The cache holds the last good list and survives a restart.
	rl2, err := relay.New(relay.Config{RelayID: h.relayID, HostKey: h.keys.HostKey, TLSCert: h.keys.TLSCert,
		RegistryURL: "http://127.0.0.1:1", RegistryKey: real.Public().(ed25519.PublicKey),
		CachePath: h.cache})
	if err != nil {
		t.Fatal(err)
	}
	if rl2.Version() != 5 {
		t.Fatalf("restarted relay loaded version %d, want 5", rl2.Version())
	}
}
