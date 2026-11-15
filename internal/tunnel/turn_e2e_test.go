package tunnel_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
	"github.com/zacharyelston/wehelp/internal/relay"
	"github.com/zacharyelston/wehelp/internal/tunnel"
	"github.com/zacharyelston/wehelp/internal/turncred"
)

// freePort returns a port free for both UDP and TCP on 127.0.0.1.
func freePort(t *testing.T) string {
	t.Helper()
	for i := 0; i < 20; i++ {
		u, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := u.LocalAddr().String()
		tl, err := net.Listen("tcp4", addr)
		u.Close()
		if err == nil {
			tl.Close()
			return addr
		}
	}
	t.Fatal("no free port")
	return ""
}

// turnClient allocates a relayed UDP address with the given credentials.
func turnClient(t *testing.T, server, user, pass string) (*turn.Client, net.PacketConn, error) {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lf := logging.NewDefaultLoggerFactory()
	lf.DefaultLogLevel = logging.LogLevelDisabled
	c, err := turn.NewClient(&turn.ClientConfig{STUNServerAddr: server, TURNServerAddr: server, Conn: conn,
		Username: user, Password: pass, Realm: turncred.Realm, LoggerFactory: lf, RTO: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); conn.Close() })
	relayed, err := c.Allocate()
	return c, relayed, err
}

// TestTURNOverTCPAndTLS: phones on networks that block UDP reach TURN over
// TCP, or over TLS on 443 with a public certificate (a test one here).
func TestTURNOverTCPAndTLS(t *testing.T) {
	h := newHarness(t)
	id := h.newOffice(t)
	h.reg.publish(t, 2, listed(id, h.relayID))
	eventually(t, "list v2", func() bool { return h.rl.Version() == 2 })
	secret, err := relay.EnsureTURNSecret(filepath.Join(t.TempDir(), relay.TURNSecretFile))
	if err != nil {
		t.Fatal(err)
	}
	addr, tlsAddr := freePort(t), freePort(t)
	if err := h.rl.StartTURN(relay.TURNOptions{Addr: addr, TLSAddr: tlsAddr, Hostname: "relay.test",
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{h.keys.TLSCert}},
		PublicIP:  net.ParseIP("127.0.0.1").To4(), PortMin: 50200, PortMax: 50299, Secret: secret}); err != nil {
		t.Fatal(err)
	}
	pusher := &tunnel.Pusher{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = tunnel.Run(ctx, id, http.NotFoundHandler(), tunnel.Options{Pusher: pusher,
			MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, Keepalive: time.Second})
	}()
	var key turncred.Key
	eventually(t, "TURN key", func() bool { var ok bool; key, ok = pusher.TURNKey(); return ok })
	_, tport, _ := net.SplitHostPort(tlsAddr)
	if len(key.URLs) != 3 || key.URLs[2] != "turns:relay.test:"+tport+"?transport=tcp" {
		t.Fatalf("urls %v", key.URLs)
	}
	user, pass := turncred.Mint(key, "visit1", time.Now().Add(time.Hour))
	lf := logging.NewDefaultLoggerFactory()
	lf.DefaultLogLevel = logging.LogLevelDisabled
	for name, dial := range map[string]func() (net.Conn, error){
		"tcp": func() (net.Conn, error) { return net.Dial("tcp4", addr) },
		"tls": func() (net.Conn, error) {
			return tls.Dial("tcp4", tlsAddr, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test certificate
		},
	} {
		nc, err := dial()
		if err != nil {
			t.Fatalf("%s dial: %v", name, err)
		}
		c, err := turn.NewClient(&turn.ClientConfig{STUNServerAddr: addr, TURNServerAddr: addr, Conn: turn.NewSTUNConn(nc),
			Username: user, Password: pass, Realm: turncred.Realm, LoggerFactory: lf})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Listen(); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Allocate(); err != nil {
			t.Errorf("allocate over %s: %v", name, err)
		}
		c.Close()
		nc.Close()
	}
}

// TestTURNKeyAndAllocation is #6's Relay half: the Relay sends an office its
// TURN key when the tunnel connects (the office never asks), credentials the
// office mints from it relay media between two phones, and everything else
// is refused: expired, forged, unlisted, over quota, over the visit limit,
// and peers that aren't this Relay's own relay address.
func TestTURNKeyAndAllocation(t *testing.T) {
	h := newHarness(t)
	id := h.newOffice(t)
	h.reg.publish(t, 2, listed(id, h.relayID))
	eventually(t, "list v2", func() bool { return h.rl.Version() == 2 })

	secret, err := relay.EnsureTURNSecret(filepath.Join(t.TempDir(), relay.TURNSecretFile))
	if err != nil {
		t.Fatal(err)
	}
	addr := freePort(t)
	if err := h.rl.StartTURN(relay.TURNOptions{Addr: addr, PublicIP: net.ParseIP("127.0.0.1").To4(),
		PortMin: 50100, PortMax: 50199, Secret: secret, MaxVisits: 2, MaxAllocs: 3}); err != nil {
		t.Fatal(err)
	}

	// The office connects and receives its key without asking.
	pusher := &tunnel.Pusher{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = tunnel.Run(ctx, id, http.NotFoundHandler(), tunnel.Options{Pusher: pusher,
			MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, Keepalive: time.Second})
	}()
	var key turncred.Key
	eventually(t, "TURN key", func() bool { var ok bool; key, ok = pusher.TURNKey(); return ok })
	if key.KeyID != turncred.KeyID(secret, id.Registration.OfficeID) || len(key.URLs) != 2 {
		t.Fatalf("key = %+v", key)
	}

	// Two phones in one visit relay data to each other through the Relay.
	exp := time.Now().Add(time.Hour)
	user, pass := turncred.Mint(key, "visit1", exp)
	ca, ra, err := turnClient(t, addr, user, pass)
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	cb, rb, err := turnClient(t, addr, user, pass)
	if err != nil {
		t.Fatalf("second allocate: %v", err)
	}
	if err := ca.CreatePermission(rb.LocalAddr()); err != nil {
		t.Fatalf("permission to the other phone's relay address: %v", err)
	}
	if err := cb.CreatePermission(ra.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	if _, err := ra.WriteTo([]byte("srtp-ish"), rb.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = rb.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := rb.ReadFrom(buf)
	if err != nil || !bytes.Equal(buf[:n], []byte("srtp-ish")) {
		t.Fatalf("relayed read: %q %v", buf[:n], err)
	}
	if s := h.rl.TURNStats(); s.Allocations != 2 || s.Visits != 1 {
		t.Errorf("stats %+v", s)
	}

	// TURN is not an open proxy: no permission to anywhere else.
	if err := ca.CreatePermission(&net.UDPAddr{IP: net.ParseIP("8.8.8.8"), Port: 53}); err == nil {
		t.Error("permission to an outside address granted")
	}

	// Per-credential quota (3 here): a third allocation works, a fourth doesn't.
	if _, _, err := turnClient(t, addr, user, pass); err != nil {
		t.Fatalf("third allocation: %v", err)
	}
	if _, _, err := turnClient(t, addr, user, pass); err == nil {
		t.Error("allocation past the quota")
	}
	// Live visits per office (2 here).
	u2, p2 := turncred.Mint(key, "visit2", exp)
	if _, _, err := turnClient(t, addr, u2, p2); err != nil {
		t.Fatalf("second visit: %v", err)
	}
	u3, p3 := turncred.Mint(key, "visit3", exp)
	if _, _, err := turnClient(t, addr, u3, p3); err == nil {
		t.Error("third live visit allowed")
	}

	// Expired, minted too far ahead, forged with another master, or for an
	// office not on the list: refused.
	for name, k := range map[string]turncred.Key{
		"forged":   {KeyID: key.KeyID, Secret: turncred.Secret([]byte("another master secret, 32 bytes!"), key.KeyID)},
		"unlisted": {KeyID: turncred.KeyID(secret, "not-an-office"), Secret: turncred.Secret(secret, turncred.KeyID(secret, "not-an-office"))},
	} {
		u, p := turncred.Mint(k, "visitX", exp)
		if _, _, err := turnClient(t, addr, u, p); err == nil {
			t.Errorf("%s credentials allocated", name)
		}
	}
	if u, p := turncred.Mint(key, "old", time.Now().Add(-time.Minute)); true {
		if _, _, err := turnClient(t, addr, u, p); err == nil {
			t.Error("expired credentials allocated")
		}
	}
	if u, p := turncred.Mint(key, "far", time.Now().Add(48*time.Hour)); true {
		if _, _, err := turnClient(t, addr, u, p); err == nil {
			t.Error("credentials minted two days ahead allocated")
		}
	}

	// Taken off the list: its key stops working at once.
	h.reg.publish(t, 3)
	eventually(t, "list v3", func() bool { return h.rl.Version() == 3 })
	u4, p4 := turncred.Mint(key, "visit2", exp)
	if _, _, err := turnClient(t, addr, u4, p4); err == nil {
		t.Error("delisted office's credentials allocated")
	}
}

// TestTURNVisitCapFromList is #175: each office's live visit cap comes from
// the signed list (the Relay's setting is only the fallback), the office
// learns its cap with its key and again when the list changes it, and an
// optional total caps the whole Relay.
func TestTURNVisitCapFromList(t *testing.T) {
	h := newHarness(t)
	a, b := h.newOffice(t), h.newOffice(t)
	withCap := listed(a, h.relayID)
	withCap.MaxVisits = 3
	h.reg.publish(t, 2, withCap, listed(b, h.relayID)) // b: no cap in the list
	eventually(t, "list v2", func() bool { return h.rl.Version() == 2 })

	secret, err := relay.EnsureTURNSecret(filepath.Join(t.TempDir(), relay.TURNSecretFile))
	if err != nil {
		t.Fatal(err)
	}
	addr := freePort(t)
	if err := h.rl.StartTURN(relay.TURNOptions{Addr: addr, PublicIP: net.ParseIP("127.0.0.1").To4(),
		PortMin: 50200, PortMax: 50299, Secret: secret, MaxVisits: 1, MaxTotalVisits: 4}); err != nil {
		t.Fatal(err)
	}
	pusher := &tunnel.Pusher{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = tunnel.Run(ctx, a, http.NotFoundHandler(), tunnel.Options{Pusher: pusher,
			MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, Keepalive: time.Second})
	}()
	var key turncred.Key
	eventually(t, "TURN key with cap 3", func() bool { var ok bool; key, ok = pusher.TURNKey(); return ok && key.MaxVisits == 3 })

	exp := time.Now().Add(time.Hour)
	visit := func(k turncred.Key, token string) error {
		u, p := turncred.Mint(k, token, exp)
		_, _, err := turnClient(t, addr, u, p)
		return err
	}
	// Office a: three visits from the list's cap, not the Relay's 1.
	for i := 1; i <= 3; i++ {
		if err := visit(key, "a"+strconv.Itoa(i)); err != nil {
			t.Fatalf("office a visit %d: %v", i, err)
		}
	}
	if visit(key, "a4") == nil {
		t.Error("office a's fourth visit allowed past its cap of 3")
	}
	// Office b has no cap in the list, so the Relay's setting (1) applies.
	bID := turncred.KeyID(secret, b.Registration.OfficeID)
	keyB := turncred.Key{KeyID: bID, Secret: turncred.Secret(secret, bID)}
	if err := visit(keyB, "b1"); err != nil {
		t.Fatalf("office b visit: %v", err)
	}
	if visit(keyB, "b2") == nil {
		t.Error("office b's second visit allowed past the Relay default of 1")
	}

	// Raising a's cap to 5: the Relay resends the key, but the total (4,
	// now reached) still refuses a fifth visit on the Relay.
	withCap.MaxVisits = 5
	h.reg.publish(t, 3, withCap, listed(b, h.relayID))
	eventually(t, "resent key with cap 5", func() bool { k, ok := pusher.TURNKey(); return ok && k.MaxVisits == 5 })
	if visit(key, "a5") == nil {
		t.Error("visit allowed past the Relay's total of 4")
	}
	if s := h.rl.TURNStats(); s.Visits != 4 {
		t.Errorf("live visits = %d, want 4", s.Visits)
	}
}
