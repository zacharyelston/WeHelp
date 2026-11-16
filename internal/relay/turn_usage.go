package relay

import (
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Per-visit TURN usage, for load tests and capacity planning: when a visit's
// last allocation goes, the Relay logs how long it ran and how many bytes
// the phones sent to and received from TURN. Counts only, no addresses and
// no credentials, like the rest of the Relay's logs.
//
// pion/turn doesn't count bytes, so the client-facing sockets (UDP, TCP and
// TLS) are wrapped and their bytes are added up by the phone's address,
// which each allocation's event names. The Relay-to-Relay hop between the
// two phones' allocations isn't counted: it never leaves this host.

// VisitUsage is one visit's TURN use, from its first allocation to its last
// byte, logged when its last allocation goes away. If every allocation goes and the phones allocate again
// (a rejoin), that's a new VisitUsage.
type VisitUsage struct {
	Office      string // office ID, or "" if it's no longer listed
	Started     time.Time
	Duration    time.Duration
	BytesIn     uint64 // from the phones to TURN
	BytesOut    uint64 // from TURN to the phones
	Allocations int    // allocations made in this visit
}

// KbpsIn and KbpsOut are average rates over the visit, in kbit/s.
func (u VisitUsage) KbpsIn() float64  { return kbps(u.BytesIn, u.Duration) }
func (u VisitUsage) KbpsOut() float64 { return kbps(u.BytesOut, u.Duration) }

func kbps(b uint64, d time.Duration) float64 {
	if d < time.Second { // too short for a meaningful rate
		return 0
	}
	return float64(b) * 8 / 1000 / d.Seconds()
}

// visitCounter is one live visit's byte counts.
type visitCounter struct {
	started     time.Time
	in, out     atomic.Uint64
	last        atomic.Int64 // unix nanos of the last byte either way
	allocations int          // guarded by usage.mu
}

// usage maps phone addresses to their visit's counter.
type usage struct {
	mu     sync.RWMutex
	byAddr map[string]*visitCounter // phone address → its visit
	visits map[string]*visitCounter // "keyID:token" → counter
}

func newUsage() *usage {
	return &usage{byAddr: map[string]*visitCounter{}, visits: map[string]*visitCounter{}}
}

func (u *usage) allocated(src net.Addr, userID string, now time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	c := u.visits[userID]
	if c == nil {
		c = &visitCounter{started: now}
		u.visits[userID] = c
	}
	c.allocations++
	u.byAddr[src.String()] = c
}

func (u *usage) released(src net.Addr) {
	u.mu.Lock()
	delete(u.byAddr, src.String())
	u.mu.Unlock()
}

// ended removes the visit's counter and returns its totals.
func (u *usage) ended(userID string) (*visitCounter, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	c, ok := u.visits[userID]
	delete(u.visits, userID)
	return c, ok
}

func (u *usage) count(addr net.Addr, n int, in bool) {
	if n <= 0 || addr == nil {
		return
	}
	u.mu.RLock()
	c := u.byAddr[addr.String()]
	u.mu.RUnlock()
	if c == nil {
		return
	}
	if in {
		c.in.Add(uint64(n))
	} else {
		c.out.Add(uint64(n))
	}
	c.last.Store(time.Now().UnixNano())
}

// countingPacketConn counts UDP bytes by the phone's address.
type countingPacketConn struct {
	net.PacketConn
	u *usage
}

func (c countingPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(p)
	c.u.count(addr, n, true)
	return n, addr, err
}

func (c countingPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(p, addr)
	c.u.count(addr, n, false)
	return n, err
}

// countingListener counts each TCP or TLS connection's bytes by its
// remote address.
type countingListener struct {
	net.Listener
	u *usage
}

func (l countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return c, err
	}
	return countingConn{Conn: c, u: l.u}, nil
}

type countingConn struct {
	net.Conn
	u *usage
}

func (c countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.u.count(c.RemoteAddr(), n, true)
	return n, err
}

func (c countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.u.count(c.RemoteAddr(), n, false)
	return n, err
}

// visitEnded logs, and reports to OnVisitEnded, a visit whose last
// allocation just went.
func (r *Relay) visitEnded(ts *turnState, keyID, userID string) {
	c, ok := ts.usage.ended(userID)
	if !ok {
		return
	}
	r.mu.Lock()
	office := r.turnKeys[keyID]
	r.mu.Unlock()
	ts.usage.mu.RLock()
	allocs := c.allocations
	ts.usage.mu.RUnlock()
	// Up to the last byte, not to when the last allocation went: an app
	// that doesn't release its allocation leaves it idle until it expires.
	var d time.Duration
	if last := c.last.Load(); last > 0 {
		d = time.Unix(0, last).Sub(c.started)
	}
	v := VisitUsage{Office: office, Started: c.started, Duration: d,
		BytesIn: c.in.Load(), BytesOut: c.out.Load(), Allocations: allocs}
	slog.Info("relay: TURN visit ended", "office", v.Office, "duration", v.Duration.Round(100*time.Millisecond).String(),
		"bytes_in", v.BytesIn, "bytes_out", v.BytesOut,
		"kbps_in", int(v.KbpsIn()), "kbps_out", int(v.KbpsOut()), "allocations", v.Allocations)
	if ts.opt.OnVisitEnded != nil {
		ts.opt.OnVisitEnded(v)
	}
}
