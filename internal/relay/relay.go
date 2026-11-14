// Package relay is the WeHelp Relay (#4). Offices connect out to it over SSH
// with an office key on the Registry's signed office list; phones send HTTP
// CONNECT <office-id>.wehelp.invalid:443 over TLS, and the Relay splices the
// connection onto a "wehelp-phone" SSH channel to that office. The Relay sees
// only the phone↔office TLS ciphertext.
//
// The Relay holds no patient data and imports no Back Office package
// (cmd/wehelp-relay/boundary_test.go). It logs office IDs and byte counts,
// never device addresses.
package relay

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/zacharyelston/wehelp/internal/officelist"
	"golang.org/x/crypto/ssh"
)

// ChannelType is the only SSH channel type: one phone connection.
const ChannelType = "wehelp-phone"

const hostSuffix = ".wehelp.invalid"

var spliceGrace = 30 * time.Second

// Config configures a Relay.
type Config struct {
	RelayID      string            // this Relay's ID in the Registry; only its offices are accepted
	HostKey      ssh.Signer        // SSH host key, pinned by offices
	TLSCert      tls.Certificate   // CONNECT listener certificate, pinned by phones
	RegistryURL  string            // base URL; the list is GET {RegistryURL}/v1/office-list
	RegistryKey  ed25519.PublicKey // pinned Registry signing key
	CachePath    string            // last good signed list
	ListInterval time.Duration     // default 1 minute
	HTTPClient   *http.Client      // default: 20 s timeout
	MaxChannels  int               // concurrent phone connections per office; default 64
	Keepalive    time.Duration     // SSH keepalive to offices; default 30 s
	Now          func() time.Time  // for tests
}

type officeConn struct {
	conn *ssh.ServerConn
	key  string // marshalled public key it authenticated with
}

// Relay routes phone connections to offices.
type Relay struct {
	cfg Config

	mu      sync.Mutex
	allowed map[string]string // marshalled office SSH key → office ID
	version int64
	offices map[string]*officeConn // office ID → live connection
	active  map[string]int         // office ID → open phone channels
}

// New returns a Relay, loading the cached list if there is one.
func New(cfg Config) (*Relay, error) {
	if cfg.RelayID == "" || cfg.HostKey == nil || len(cfg.RegistryKey) != ed25519.PublicKeySize {
		return nil, errors.New("relay: relay ID, host key and Registry key are required")
	}
	if cfg.ListInterval <= 0 {
		cfg.ListInterval = time.Minute
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	}
	if cfg.MaxChannels <= 0 {
		cfg.MaxChannels = 64
	}
	if cfg.Keepalive <= 0 {
		cfg.Keepalive = 30 * time.Second
	}
	r := &Relay{cfg: cfg, allowed: map[string]string{}, offices: map[string]*officeConn{}, active: map[string]int{}}
	if cfg.CachePath != "" {
		if l, err := loadCache(cfg.CachePath, cfg.RegistryKey); err != nil {
			slog.Warn("relay: ignoring office list cache", "err", err)
		} else if l != nil {
			r.apply(*l)
			slog.Info("relay: loaded cached office list", "version", l.Version, "offices", len(r.allowed))
		}
	}
	return r, nil
}

// Version is the version of the list in force (0 if none).
func (r *Relay) Version() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.version
}

// Connected reports whether an office has a live SSH connection.
func (r *Relay) Connected(officeID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.offices[officeID] != nil
}

// Serve runs the SSH and CONNECT listeners and the list sync until ctx ends.
func (r *Relay) Serve(ctx context.Context, sshLn, connectLn net.Listener) error {
	go r.syncLoop(ctx)

	sshCfg := &ssh.ServerConfig{PublicKeyCallback: r.authorize, ServerVersion: "SSH-2.0-wehelp-relay"}
	sshCfg.AddHostKey(r.cfg.HostKey)
	go r.acceptOffices(ctx, sshLn, sshCfg)

	srv := &http.Server{
		Handler:           http.HandlerFunc(r.handleConnect),
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{r.cfg.TLSCert},
			MinVersion:   tls.VersionTLS13,
			NextProtos:   []string{"http/1.1"},
		},
		// HTTP/1.1 only: CONNECT needs a hijackable connection.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
		// The default error log names client addresses (TLS handshake
		// errors). Phones are devices; keep their addresses out of logs.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go func() {
		<-ctx.Done()
		_ = sshLn.Close()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		r.closeAll()
	}()
	err := srv.ServeTLS(connectLn, "", "")
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// authorize accepts an office only if its key is on the current list for
// this Relay and it presents its own office ID as the SSH user.
func (r *Relay) authorize(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	k := string(key.Marshal())
	r.mu.Lock()
	id, ok := r.allowed[k]
	r.mu.Unlock()
	if !ok || meta.User() != id {
		slog.Warn("relay: office refused", "key", ssh.FingerprintSHA256(key))
		return nil, errors.New("office not on the list")
	}
	return &ssh.Permissions{Extensions: map[string]string{"office_id": id, "key": k}}, nil
}

func (r *Relay) acceptOffices(ctx context.Context, ln net.Listener, cfg *ssh.ServerConfig) {
	for {
		nc, err := ln.Accept()
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("relay: ssh accept", "err", err)
			}
			return
		}
		go r.serveOffice(nc, cfg)
	}
}

func (r *Relay) serveOffice(nc net.Conn, cfg *ssh.ServerConfig) {
	_ = nc.SetDeadline(time.Now().Add(20 * time.Second)) // handshake only
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		_ = nc.Close()
		return
	}
	_ = nc.SetDeadline(time.Time{})
	id, key := conn.Permissions.Extensions["office_id"], conn.Permissions.Extensions["key"]

	r.mu.Lock()
	// The list may have changed during the handshake.
	if r.allowed[key] != id {
		r.mu.Unlock()
		_ = conn.Close()
		return
	}
	old := r.offices[id]
	r.offices[id] = &officeConn{conn: conn, key: key}
	r.mu.Unlock()
	if old != nil {
		_ = old.conn.Close() // a reconnect replaces the previous connection
	}
	slog.Info("relay: office connected", "office", id)

	go ssh.DiscardRequests(reqs)
	go func() { // offices never open channels to the Relay
		for nch := range chans {
			_ = nch.Reject(ssh.Prohibited, "the relay accepts no channels")
		}
	}()
	stop := make(chan struct{})
	go r.keepalive(conn, stop)
	_ = conn.Wait()
	close(stop)

	r.mu.Lock()
	if cur := r.offices[id]; cur != nil && cur.conn == conn {
		delete(r.offices, id)
	}
	r.mu.Unlock()
	slog.Info("relay: office disconnected", "office", id)
}

func (r *Relay) keepalive(conn *ssh.ServerConn, stop <-chan struct{}) {
	t := time.NewTicker(r.cfg.Keepalive)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			done := make(chan error, 1)
			go func() { _, _, err := conn.SendRequest("keepalive@openssh.com", true, nil); done <- err }()
			select {
			case err := <-done:
				if err != nil {
					_ = conn.Close()
					return
				}
			case <-time.After(r.cfg.Keepalive):
				_ = conn.Close()
				return
			case <-stop:
				return
			}
		}
	}
}

// officeFromHost parses "<office-id>.wehelp.invalid:443".
func officeFromHost(hostport string) (string, bool) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil || port != "443" {
		return "", false
	}
	id, ok := strings.CutSuffix(strings.ToLower(host), hostSuffix)
	if !ok {
		return "", false
	}
	if _, err := uuid.Parse(id); err != nil || len(id) != 36 {
		return "", false
	}
	return id, true
}

func (r *Relay) handleConnect(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodConnect {
		http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
		return
	}
	id, ok := officeFromHost(req.Host)
	if !ok {
		http.Error(w, "unknown office", http.StatusNotFound)
		return
	}
	r.mu.Lock()
	oc := r.offices[id]
	full := r.active[id] >= r.cfg.MaxChannels
	if oc != nil && !full {
		r.active[id]++
	}
	r.mu.Unlock()
	switch {
	case oc == nil:
		http.Error(w, "office offline", http.StatusBadGateway)
		return
	case full:
		http.Error(w, "office busy", http.StatusServiceUnavailable)
		return
	}
	defer func() {
		r.mu.Lock()
		if r.active[id]--; r.active[id] <= 0 {
			delete(r.active, id)
		}
		r.mu.Unlock()
	}()

	ch, reqs, err := oc.conn.OpenChannel(ChannelType, nil)
	if err != nil {
		http.Error(w, "office unreachable", http.StatusBadGateway)
		return
	}
	go ssh.DiscardRequests(reqs)
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = ch.Close()
		http.Error(w, "cannot splice", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		_ = ch.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = client.Close()
		_ = ch.Close()
		return
	}
	start := time.Now()
	up, down := splice(client, buf.Reader, ch)
	slog.Info("relay: phone connection closed", "office", id, "bytes_up", up, "bytes_down", down,
		"duration", time.Since(start).Round(time.Millisecond).String())
}

// splice copies both ways. When one direction finishes, the other gets
// spliceGrace to finish too before both ends are closed, so a peer that
// never closes can't hold the connection open.
func splice(client net.Conn, buffered io.Reader, ch ssh.Channel) (up, down int64) {
	done := make(chan struct{}, 2)
	go func() { // phone → office, including anything read ahead
		up, _ = io.Copy(ch, io.MultiReader(buffered, client))
		_ = ch.CloseWrite()
		done <- struct{}{}
	}()
	go func() { // office → phone
		down, _ = io.Copy(client, ch)
		if tc, ok := client.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	finished := 1
	select {
	case <-done:
		finished++
	case <-time.After(spliceGrace):
	}
	_ = client.Close()
	_ = ch.Close()
	for ; finished < 2; finished++ {
		<-done // closing both ends makes the other copy return
	}
	return up, down
}

// apply installs a verified list and disconnects offices no longer on it
// (or whose key changed).
func (r *Relay) apply(l officelist.List) {
	allowed := map[string]string{}
	for _, o := range l.Offices {
		if o.RelayID != r.cfg.RelayID {
			continue
		}
		pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(o.SSHPublicKey))
		if err != nil {
			slog.Warn("relay: skipping office with a bad key", "office", o.OfficeID)
			continue
		}
		allowed[string(pk.Marshal())] = o.OfficeID
	}
	var drop []*officeConn
	r.mu.Lock()
	r.allowed, r.version = allowed, l.Version
	for id, oc := range r.offices {
		if allowed[oc.key] != id {
			drop = append(drop, oc)
			delete(r.offices, id)
		}
	}
	r.mu.Unlock()
	for _, oc := range drop {
		slog.Info("relay: disconnecting office no longer listed", "office", oc.conn.User())
		_ = oc.conn.Close()
	}
}

func (r *Relay) closeAll() {
	r.mu.Lock()
	conns := make([]*officeConn, 0, len(r.offices))
	for _, oc := range r.offices {
		conns = append(conns, oc)
	}
	r.mu.Unlock()
	for _, oc := range conns {
		_ = oc.conn.Close()
	}
}

func (r *Relay) String() string { return fmt.Sprintf("relay %s", r.cfg.RelayID) }
