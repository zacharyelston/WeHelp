package relay

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
	"github.com/zacharyelston/wehelp/internal/keyfile"
	"github.com/zacharyelston/wehelp/internal/turncred"
)

// TURN for video visits (#6). Media between two phones is DTLS-SRTP end to
// end; the Relay only forwards ciphertext, and every call is forced through
// it (iceTransportPolicy = relay), so phones never learn each other's
// addresses.
//
// Credentials: the Relay's master secret never leaves it. Each office gets
// its own key (turncred) when its tunnel connects and mints per-visit
// credentials itself. The Relay accepts a credential only if its key belongs
// to an office on the current signed list and it hasn't expired.
//
// Hardening from spike S2: an allocation quota per credential and a limit
// of live visits per office; peers may only be this Relay's own relay
// address, so TURN can't be used as a proxy to anywhere else; and short
// allocation and permission lifetimes, so allocations left by a crashed app
// go within minutes.

// TURNOptions configures the TURN server. Addr empty means TURN is off.
type TURNOptions struct {
	Addr      string      // UDP and TCP, e.g. ":3478"
	TLSAddr   string      // TURN over TLS, e.g. ":443"; needs TLSConfig
	TLSConfig *tls.Config // a publicly trusted certificate (Let's Encrypt): libwebrtc can't pin
	Hostname  string      // public DNS name for turns: URLs
	PublicIP  net.IP      // the address phones reach; relay addresses use it too
	PortMin   int         // relay port range
	PortMax   int
	Secret    []byte // master secret, never sent anywhere
	MaxVisits int    // live visits per office when the list gives no cap; default 4
	// MaxTotalVisits caps live visits across every office on this Relay, to
	// protect its bandwidth (#175). 0 means no total cap.
	MaxTotalVisits int
	MaxAllocs      int // allocations per visit credential; default 8
	MaxLife        time.Duration
}

const (
	turnAllocLife    = 5 * time.Minute
	turnPermLife     = 2 * time.Minute
	turnChannelLife  = 5 * time.Minute
	turnMaxCredLife  = 12 * time.Hour // a credential can't be minted further out than this
	defaultMaxVisits = 4
	defaultMaxAllocs = 8
)

// turnState is the Relay's live TURN bookkeeping.
type turnState struct {
	opt    TURNOptions
	srv    *turn.Server
	mu     sync.Mutex
	allocs map[string]int            // "keyID:token" → live allocations
	visits map[string]map[string]int // keyID → token → live allocations
	limits map[string]int            // keyID → visit cap from the office list; absent = opt.MaxVisits
	total  int
	done   chan struct{}
}

// StartTURN starts the TURN server. Call it after New; it stops with Close.
func (r *Relay) StartTURN(opt TURNOptions) error {
	if opt.Addr == "" {
		return nil
	}
	if len(opt.Secret) < 32 || opt.PublicIP == nil || opt.PortMin <= 0 || opt.PortMax < opt.PortMin {
		return errors.New("relay: TURN needs a 32-byte secret, a public IP and a relay port range")
	}
	if opt.MaxVisits <= 0 {
		opt.MaxVisits = defaultMaxVisits
	}
	if opt.MaxAllocs <= 0 {
		opt.MaxAllocs = defaultMaxAllocs
	}
	if opt.MaxLife <= 0 {
		opt.MaxLife = turnMaxCredLife
	}
	ts := &turnState{opt: opt, allocs: map[string]int{}, visits: map[string]map[string]int{}, limits: map[string]int{},
		done: make(chan struct{})}

	gen := func() turn.RelayAddressGenerator {
		return &turn.RelayAddressGeneratorPortRange{RelayAddress: opt.PublicIP, Address: "0.0.0.0",
			MinPort: uint16(opt.PortMin), MaxPort: uint16(opt.PortMax)}
	}
	// Only this Relay's own relay address: a peer is always the other
	// phone's allocation here, never anywhere else.
	perm := func(_ net.Addr, peer net.IP) bool { return peer.Equal(opt.PublicIP) }

	udp, err := net.ListenPacket("udp4", opt.Addr)
	if err != nil {
		return fmt.Errorf("relay: TURN UDP: %w", err)
	}
	tcp, err := net.Listen("tcp4", opt.Addr)
	if err != nil {
		udp.Close()
		return fmt.Errorf("relay: TURN TCP: %w", err)
	}
	listeners := []turn.ListenerConfig{{Listener: tcp, RelayAddressGenerator: gen(), PermissionHandler: perm}}
	if opt.TLSAddr != "" && opt.TLSConfig != nil {
		tl, err := tls.Listen("tcp4", opt.TLSAddr, opt.TLSConfig)
		if err != nil {
			udp.Close()
			tcp.Close()
			return fmt.Errorf("relay: TURN TLS: %w", err)
		}
		listeners = append(listeners, turn.ListenerConfig{Listener: tl, RelayAddressGenerator: gen(), PermissionHandler: perm})
	}
	lf := logging.NewDefaultLoggerFactory()
	lf.DefaultLogLevel = logging.LogLevelWarn
	ts.srv, err = turn.NewServer(turn.ServerConfig{
		Realm:              turncred.Realm,
		LoggerFactory:      lf,
		AuthHandler:        r.turnAuth,
		QuotaHandler:       ts.quota,
		AllocationLifetime: turnAllocLife,
		PermissionTimeout:  turnPermLife,
		ChannelBindTimeout: turnChannelLife,
		PacketConnConfigs:  []turn.PacketConnConfig{{PacketConn: udp, RelayAddressGenerator: gen(), PermissionHandler: perm}},
		ListenerConfigs:    listeners,
		EventHandler: turn.EventHandler{
			OnAllocationCreated: func(_, _ net.Addr, _, userID, _ string, _ net.Addr, _ int) { ts.track(userID, +1) },
			OnAllocationDeleted: func(_, _ net.Addr, _, userID, _ string) { ts.track(userID, -1) },
		},
	})
	if err != nil {
		udp.Close()
		tcp.Close()
		return err
	}
	r.mu.Lock()
	r.turn = ts
	r.turnKeys = r.turnKeyIDs()
	r.syncTURNLimits()
	r.mu.Unlock()
	slog.Info("relay: TURN on", "addr", opt.Addr, "tls", opt.TLSAddr != "" && opt.TLSConfig != nil,
		"public_ip", opt.PublicIP.String(), "ports", fmt.Sprintf("%d-%d", opt.PortMin, opt.PortMax))
	go r.logTURN(ts)
	return nil
}

// turnKeyIDs maps each allowed office's key ID to the office. Call with r.mu held.
func (r *Relay) turnKeyIDs() map[string]string {
	if r.turn == nil {
		return nil
	}
	m := make(map[string]string, len(r.allowed))
	for _, office := range r.allowed {
		m[turncred.KeyID(r.turn.opt.Secret, office)] = office
	}
	return m
}

// syncTURNLimits gives TURN each listed office's visit cap, by key ID. Call
// with r.mu held.
func (r *Relay) syncTURNLimits() {
	if r.turn == nil {
		return
	}
	limits := make(map[string]int, len(r.caps))
	for office, n := range r.caps {
		limits[turncred.KeyID(r.turn.opt.Secret, office)] = n
	}
	r.turn.mu.Lock()
	r.turn.limits = limits
	r.turn.mu.Unlock()
}

// maxVisitsLocked is the office's visit cap. Call with ts.mu held.
func (ts *turnState) maxVisitsLocked(keyID string) int {
	if n := ts.limits[keyID]; n > 0 {
		return n
	}
	return ts.opt.MaxVisits
}

// turnAuth checks a credential: an office on the list, not expired, not
// minted too far ahead, and the right HMAC.
func (r *Relay) turnAuth(ra *turn.RequestAttributes) (string, []byte, bool) {
	u, err := turncred.Parse(ra.Username)
	if err != nil {
		return "", nil, false
	}
	r.mu.Lock()
	ts, office := r.turn, r.turnKeys[u.KeyID]
	r.mu.Unlock()
	now := r.cfg.Now()
	if ts == nil || office == "" || !u.Expires.After(now) || u.Expires.Sub(now) > ts.opt.MaxLife {
		return "", nil, false
	}
	pass := turncred.Password(turncred.Secret(ts.opt.Secret, u.KeyID), ra.Username)
	return u.KeyID + ":" + u.Token, turn.GenerateAuthKey(ra.Username, ra.Realm, pass), true
}

// quota refuses a new allocation past the per-credential quota, or for a
// new visit when the office already has its cap live, or when the Relay
// already has its total live.
func (ts *turnState) quota(userID, _ string, _ net.Addr) bool {
	keyID, token, ok := strings.Cut(userID, ":")
	if !ok {
		return false
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.allocs[userID] >= ts.opt.MaxAllocs {
		slog.Warn("relay: TURN allocation quota reached", "key", keyID)
		return false
	}
	v := ts.visits[keyID]
	if v[token] > 0 {
		return true // a visit already live may add allocations up to its quota
	}
	if max := ts.maxVisitsLocked(keyID); len(v) >= max {
		slog.Warn("relay: TURN visit limit reached", "key", keyID, "visits", len(v), "max", max)
		return false
	}
	if ts.opt.MaxTotalVisits > 0 {
		total := 0
		for _, ov := range ts.visits {
			total += len(ov)
		}
		if total >= ts.opt.MaxTotalVisits {
			slog.Warn("relay: TURN total visit limit reached", "visits", total)
			return false
		}
	}
	return true
}

func (ts *turnState) track(userID string, d int) {
	keyID, token, ok := strings.Cut(userID, ":")
	if !ok {
		return
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.total += d
	ts.allocs[userID] += d
	if ts.allocs[userID] <= 0 {
		delete(ts.allocs, userID)
	}
	v := ts.visits[keyID]
	if v == nil {
		v = map[string]int{}
		ts.visits[keyID] = v
	}
	v[token] += d
	if v[token] <= 0 {
		delete(v, token)
	}
	if len(v) == 0 {
		delete(ts.visits, keyID)
	}
}

// TURNStats is a snapshot for logs and metrics: counts only, no addresses.
type TURNStats struct {
	Allocations int
	Visits      int
}

func (r *Relay) TURNStats() TURNStats {
	r.mu.Lock()
	ts := r.turn
	r.mu.Unlock()
	if ts == nil {
		return TURNStats{}
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	s := TURNStats{Allocations: ts.total}
	for _, v := range ts.visits {
		s.Visits += len(v)
	}
	return s
}

// turnKeyFor is the key this Relay sends an office when it connects.
func (r *Relay) turnKeyFor(officeID string) (turncred.Key, bool) {
	r.mu.Lock()
	ts := r.turn
	r.mu.Unlock()
	if ts == nil {
		return turncred.Key{}, false
	}
	id := turncred.KeyID(ts.opt.Secret, officeID)
	ts.mu.Lock()
	max := ts.maxVisitsLocked(id)
	ts.mu.Unlock()
	_, port, _ := net.SplitHostPort(ts.opt.Addr)
	host := ts.opt.PublicIP.String()
	urls := []string{"turn:" + net.JoinHostPort(host, port) + "?transport=udp",
		"turn:" + net.JoinHostPort(host, port) + "?transport=tcp"}
	if ts.opt.TLSAddr != "" && ts.opt.TLSConfig != nil && ts.opt.Hostname != "" {
		_, tport, _ := net.SplitHostPort(ts.opt.TLSAddr)
		urls = append(urls, "turns:"+net.JoinHostPort(ts.opt.Hostname, tport)+"?transport=tcp")
	}
	return turncred.Key{KeyID: id, Secret: turncred.Secret(ts.opt.Secret, id), URLs: urls, MaxVisits: max}, true
}

// sendTURNKey gives a newly connected office its TURN key. It is an update,
// not a request the office makes: no reply is wanted, and an office that
// doesn't know it just ignores it.
func (r *Relay) sendTURNKey(conn interface {
	SendRequest(string, bool, []byte) (bool, []byte, error)
}, officeID string) {
	k, ok := r.turnKeyFor(officeID)
	if !ok {
		return
	}
	payload, err := json.Marshal(k)
	if err != nil {
		return
	}
	go func() {
		if _, _, err := conn.SendRequest(turncred.RequestType, false, payload); err != nil {
			slog.Warn("relay: sending TURN key", "office", officeID, "err", err)
		}
	}()
}

func (r *Relay) closeTURN() {
	r.mu.Lock()
	ts := r.turn
	r.mu.Unlock()
	if ts != nil {
		close(ts.done)
		_ = ts.srv.Close()
	}
}

// logTURN logs live counts once a minute while anything is live: counts
// only, no addresses and no usernames. OTLP export comes with #55.
func (r *Relay) logTURN(ts *turnState) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ts.done:
			return
		case <-t.C:
			if s := r.TURNStats(); s.Allocations > 0 {
				slog.Info("relay: TURN", "allocations", s.Allocations, "visits", s.Visits)
			}
		}
	}
}

// TURNSecretFile is the master TURN secret in the Relay directory.
const TURNSecretFile = "turn.secret"

// EnsureTURNSecret reads the master TURN secret at path, creating 32 random
// bytes (mode 600) the first time. It refuses a file others can read. The
// secret is never sent anywhere; rotating it (delete and restart) gives every
// office a new key on its next connect.
func EnsureTURNSecret(path string) ([]byte, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		if _, err := f.WriteString(hex.EncodeToString(b) + "\n"); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	if err := keyfile.CheckPrivate(path); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(b) < 32 {
		return nil, fmt.Errorf("relay: %s must hold at least 32 hex-encoded bytes", path)
	}
	return b, nil
}

// ParsePortRange reads "49160-49999".
func ParsePortRange(s string) (int, int, error) {
	a, b, ok := strings.Cut(s, "-")
	lo, err1 := strconv.Atoi(strings.TrimSpace(a))
	hi, err2 := strconv.Atoi(strings.TrimSpace(b))
	if !ok || err1 != nil || err2 != nil || lo < 1024 || hi > 65535 || hi < lo {
		return 0, 0, fmt.Errorf("relay: port range %q: want like 49160-49999", s)
	}
	return lo, hi, nil
}
