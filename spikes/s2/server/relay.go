package main

import (
	"fmt"
	"log/slog"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/turn/v5"
)

// countingGenerator wraps every relay socket so we can count the bytes each
// allocation forwards, and classify them by first byte (RFC 7983):
// 0-3 STUN, 20-63 DTLS, 128-191 RTP/RTCP (SRTP/SRTCP once DTLS is done).
type countingGenerator struct {
	*turn.RelayAddressGeneratorPortRange
	reg *relayRegistry
}

func (g *countingGenerator) AllocatePacketConn(conf turn.AllocateListenerConfig) (net.PacketConn, net.Addr, error) {
	pc, addr, err := g.RelayAddressGeneratorPortRange.AllocatePacketConn(conf)
	if err != nil {
		return nil, nil, err
	}
	c := &countingConn{PacketConn: pc, user: conf.UserID, relay: addr.String(), born: time.Now(), reg: g.reg}
	g.reg.add(c)
	return c, addr, nil
}

type classCounts struct {
	stun, dtls, rtp, other atomic.Uint64 // packets
}

func (cc *classCounts) count(b []byte) {
	if len(b) == 0 {
		cc.other.Add(1)
		return
	}
	switch f := b[0]; {
	case f <= 3:
		cc.stun.Add(1)
	case f >= 20 && f <= 63:
		cc.dtls.Add(1)
	case f >= 128 && f <= 191:
		cc.rtp.Add(1)
	default:
		cc.other.Add(1)
	}
}

func (cc *classCounts) String() string {
	return fmt.Sprintf("stun=%d dtls=%d srtp=%d other=%d", cc.stun.Load(), cc.dtls.Load(), cc.rtp.Load(), cc.other.Load())
}

// countingConn is the relay-side socket of one allocation. "in" is what
// arrives from the peer (forwarded to the client); "out" is what the client
// sends to the peer.
type countingConn struct {
	net.PacketConn
	user, relay string
	born        time.Time
	reg         *relayRegistry

	inBytes, outBytes atomic.Uint64
	inPk, outPk       atomic.Uint64
	cls               classCounts

	mu       sync.Mutex
	lastIn   uint64
	lastOut  uint64
	lastT    time.Time
	lastKbps float64
	closed   bool
}

func (c *countingConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, a, err := c.PacketConn.ReadFrom(p)
	if n > 0 {
		c.inBytes.Add(uint64(n))
		c.inPk.Add(1)
		c.cls.count(p[:n])
	}
	return n, a, err
}

func (c *countingConn) WriteTo(p []byte, a net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(p, a)
	if n > 0 {
		c.outBytes.Add(uint64(n))
		c.outPk.Add(1)
		c.cls.count(p[:n])
	}
	return n, err
}

func (c *countingConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.log("closed")
	c.reg.remove(c)
	return c.PacketConn.Close()
}

// rate returns the combined in+out kbit/s since the previous call.
func (c *countingConn) rate() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	in, out := c.inBytes.Load(), c.outBytes.Load()
	if !c.lastT.IsZero() {
		dt := now.Sub(c.lastT).Seconds()
		if dt > 0 {
			c.lastKbps = float64((in-c.lastIn)+(out-c.lastOut)) * 8 / 1000 / dt
		}
	}
	c.lastIn, c.lastOut, c.lastT = in, out, now
	return c.lastKbps
}

func (c *countingConn) log(event string) {
	slog.Info("relay", "event", event, "user", c.user, "relay", c.relay,
		"age", time.Since(c.born).Round(time.Second),
		"in_bytes", c.inBytes.Load(), "out_bytes", c.outBytes.Load(),
		"in_pkts", c.inPk.Load(), "out_pkts", c.outPk.Load(),
		"kbps", fmt.Sprintf("%.0f", c.lastKbps), "classes", c.cls.String())
}

type relayRegistry struct {
	mu    sync.Mutex
	conns map[*countingConn]struct{}
}

func newRelayRegistry() *relayRegistry {
	return &relayRegistry{conns: map[*countingConn]struct{}{}}
}

func (r *relayRegistry) add(c *countingConn) {
	r.mu.Lock()
	r.conns[c] = struct{}{}
	r.mu.Unlock()
}

func (r *relayRegistry) remove(c *countingConn) {
	r.mu.Lock()
	delete(r.conns, c)
	r.mu.Unlock()
}

func (r *relayRegistry) snapshot() []*countingConn {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*countingConn, 0, len(r.conns))
	for c := range r.conns {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].relay < out[j].relay })
	return out
}

type totals struct {
	live int
	kbps float64
}

// totals also advances each relay's rate window; call it once per interval.
func (r *relayRegistry) totals() totals {
	var t totals
	for _, c := range r.snapshot() {
		t.live++
		t.kbps += c.rate()
	}
	// Each relayed packet is counted twice (in on one allocation, out on the
	// other's), so the wire rate through the server is about half of this sum
	// per direction; see S2.md.
	return t
}

func (r *relayRegistry) logAll(event string) {
	for _, c := range r.snapshot() {
		c.log(event)
	}
}
