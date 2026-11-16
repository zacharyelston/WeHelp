package tunnel_test

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/zacharyelston/wehelp/internal/relay"
	"github.com/zacharyelston/wehelp/internal/tunnel"
	"github.com/zacharyelston/wehelp/internal/turncred"
)

// TestTURNVisitUsage: when a visit's last allocation goes, the Relay reports
// its duration and the bytes the phones sent to and got from TURN.
func TestTURNVisitUsage(t *testing.T) {
	h := newHarness(t)
	id := h.newOffice(t)
	h.reg.publish(t, 2, listed(id, h.relayID))
	eventually(t, "list v2", func() bool { return h.rl.Version() == 2 })

	secret, err := relay.EnsureTURNSecret(filepath.Join(t.TempDir(), relay.TURNSecretFile))
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan relay.VisitUsage, 4)
	addr := freePort(t)
	if err := h.rl.StartTURN(relay.TURNOptions{Addr: addr, PublicIP: net.ParseIP("127.0.0.1").To4(),
		PortMin: 50300, PortMax: 50399, Secret: secret,
		OnVisitEnded: func(u relay.VisitUsage) { ended <- u }}); err != nil {
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

	user, pass := turncred.Mint(key, "visit-usage", time.Now().Add(time.Hour))
	ca, ra, err := turnClient(t, addr, user, pass)
	if err != nil {
		t.Fatal(err)
	}
	cb, rb, err := turnClient(t, addr, user, pass)
	if err != nil {
		t.Fatal(err)
	}
	if err := ca.CreatePermission(rb.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	if err := cb.CreatePermission(ra.LocalAddr()); err != nil {
		t.Fatal(err)
	}

	// Phone A sends 40 KB of "media" to phone B; B reads it all.
	const packets, size = 40, 1000
	got := make(chan int, 1)
	go func() {
		buf, total := make([]byte, 1500), 0
		_ = rb.SetReadDeadline(time.Now().Add(5 * time.Second))
		for total < packets*size {
			n, _, err := rb.ReadFrom(buf)
			if err != nil {
				break
			}
			total += n
		}
		got <- total
	}()
	pkt := make([]byte, size)
	for i := 0; i < packets; i++ {
		if _, err := ra.WriteTo(pkt, rb.LocalAddr()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // don't overrun loopback UDP buffers
	}
	if n := <-got; n < packets*size {
		t.Fatalf("phone B got %d bytes, want %d", n, packets*size)
	}
	if s := h.rl.TURNStats(); s.Visits != 1 {
		t.Fatalf("stats %+v", s)
	}

	// Both phones leave: the visit's usage is reported once.
	ra.Close()
	rb.Close()
	var u relay.VisitUsage
	select {
	case u = <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("no visit usage after both allocations went")
	}
	if u.Office != id.Registration.OfficeID || u.Allocations != 2 {
		t.Errorf("usage office %q allocations %d", u.Office, u.Allocations)
	}
	// Media plus TURN framing and control messages: at least the payload.
	if u.BytesIn < packets*size || u.BytesOut < packets*size {
		t.Errorf("bytes in %d out %d, want each ≥ %d", u.BytesIn, u.BytesOut, packets*size)
	}
	if u.BytesIn > 3*packets*size || u.BytesOut > 3*packets*size {
		t.Errorf("bytes in %d out %d: far more than was sent", u.BytesIn, u.BytesOut)
	}
	if u.Duration <= 0 || u.Duration > 10*time.Second {
		t.Errorf("duration %v", u.Duration)
	}
	// Rates over a 5-minute call like the one measured on Barnacle.
	call := relay.VisitUsage{Duration: 272 * time.Second, BytesIn: 74_070_000, BytesOut: 74_180_000}
	if in := int(call.KbpsIn()); in != 2178 {
		t.Errorf("kbps in = %d, want 2178", in)
	}
	if (relay.VisitUsage{Duration: 500 * time.Millisecond, BytesIn: 1000}).KbpsIn() != 0 {
		t.Error("a sub-second visit should report no rate")
	}
	select {
	case extra := <-ended:
		t.Errorf("visit reported twice: %+v", extra)
	case <-time.After(200 * time.Millisecond):
	}
	if s := h.rl.TURNStats(); s.Visits != 0 || s.Allocations != 0 {
		t.Errorf("stats after the visit %+v", s)
	}
}
