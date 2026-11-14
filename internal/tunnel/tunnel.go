// Package tunnel is the Back Office side of the Relay connection (#4). It
// keeps one outbound SSH connection to the office's Relay, authenticating
// with the office SSH key and pinning the Relay's host key. Each
// "wehelp-phone" channel the Relay opens is one phone connection: the office
// terminates TLS 1.3 on it with the office TLS key (the key phones pin) and
// serves it with the normal API handler. No inbound port is opened.
package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/zacharyelston/wehelp/internal/office"
	"golang.org/x/crypto/ssh"
)

// ChannelType must match the Relay's.
const ChannelType = "wehelp-phone"

type ctxKey struct{}

// ViaRelay reports whether a request arrived through the Relay rather than
// the office LAN. Staff routes refuse these (#38).
func ViaRelay(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKey{}).(bool)
	return v
}

// WithViaRelay marks a context as having arrived through the Relay. Run
// does this for every phone connection; tests use it to exercise the
// LAN-only checks.
func WithViaRelay(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKey{}, true)
}

// Options configures Run.
type Options struct {
	// RelayAddress overrides the Relay SSH address from the registration
	// (demos only, e.g. through an SSH port forward).
	RelayAddress string
	MinBackoff   time.Duration // default 1 s
	MaxBackoff   time.Duration // default 30 s
	Keepalive    time.Duration // default 30 s
	// OnConnect, if set, is called each time the tunnel is up (tests).
	OnConnect func()
}

// Run keeps the tunnel up until ctx ends, reconnecting with backoff.
func Run(ctx context.Context, id *office.Identity, handler http.Handler, opt Options) error {
	if id.Registration == nil {
		return errors.New("tunnel: office is not installed with the Registry (wehelpd init --install-token)")
	}
	reg := id.Registration
	signer, err := ssh.NewSignerFromKey(id.SSHKey)
	if err != nil {
		return err
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(reg.Relay.SSHHostKey))
	if err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(id.Dir, office.TLSCertFile), filepath.Join(id.Dir, office.TLSKeyFile))
	if err != nil {
		return err
	}
	addr := reg.Relay.SSHAddress
	if opt.RelayAddress != "" {
		addr = opt.RelayAddress
	}
	if opt.MinBackoff <= 0 {
		opt.MinBackoff = time.Second
	}
	if opt.MaxBackoff <= 0 {
		opt.MaxBackoff = 30 * time.Second
	}
	if opt.Keepalive <= 0 {
		opt.Keepalive = 30 * time.Second
	}

	ln := newChanListener()
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13},
		ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
			return context.WithValue(ctx, ctxKey{}, true)
		},
	}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		_ = ln.Close()
	}()

	cfg := &ssh.ClientConfig{
		User:              reg.OfficeID,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback:   ssh.FixedHostKey(hostKey), // pin the Relay
		HostKeyAlgorithms: []string{hostKey.Type()},
		Timeout:           15 * time.Second,
	}
	backoff := opt.MinBackoff
	for {
		start := time.Now()
		err := connectOnce(ctx, addr, cfg, ln, opt)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(start) > 2*time.Minute {
			backoff = opt.MinBackoff // it was up for a while; retry promptly
		}
		wait := backoff/2 + rand.N(backoff/2+1)
		slog.Warn("tunnel: relay connection down; retrying", "relay", addr, "err", err, "in", wait.Round(time.Millisecond).String())
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		if backoff *= 2; backoff > opt.MaxBackoff {
			backoff = opt.MaxBackoff
		}
	}
}

func connectOnce(ctx context.Context, addr string, cfg *ssh.ClientConfig, ln *chanListener, opt Options) error {
	d := net.Dialer{Timeout: cfg.Timeout, KeepAlive: 30 * time.Second}
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	conn, chans, reqs, err := ssh.NewClientConn(nc, addr, cfg)
	if err != nil {
		_ = nc.Close()
		return err
	}
	client := ssh.NewClient(conn, chans, reqs)
	defer client.Close()
	phones := client.HandleChannelOpen(ChannelType) // all other types are rejected
	slog.Info("tunnel: connected to relay", "relay", addr)
	if opt.OnConnect != nil {
		opt.OnConnect()
	}

	stop := make(chan struct{})
	defer close(stop)
	go func() { // end the connection when ctx ends or keepalives fail
		t := time.NewTicker(opt.Keepalive)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = client.Close()
				return
			case <-stop:
				return
			case <-t.C:
				done := make(chan error, 1)
				go func() { _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); done <- err }()
				select {
				case err := <-done:
					if err != nil {
						_ = client.Close()
						return
					}
				case <-time.After(opt.Keepalive):
					_ = client.Close()
					return
				case <-stop:
					return
				}
			}
		}
	}()

	for nch := range phones {
		ch, chReqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(chReqs)
		if !ln.deliver(&chanConn{Channel: ch}) {
			_ = ch.Close()
		}
	}
	return errors.New("relay connection closed")
}

// chanListener hands SSH channels to the TLS/HTTP server as net.Conns.
type chanListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newChanListener() *chanListener {
	return &chanListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *chanListener) deliver(c net.Conn) bool {
	select {
	case l.conns <- c:
		return true
	case <-l.closed:
		return false
	}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return relayAddr{} }

// chanConn adapts an SSH channel to net.Conn. Deadlines are not supported
// by SSH channels; the HTTP server's timeouts close the channel instead.
type chanConn struct{ ssh.Channel }

func (c *chanConn) LocalAddr() net.Addr              { return relayAddr{} }
func (c *chanConn) RemoteAddr() net.Addr             { return relayAddr{} }
func (c *chanConn) SetDeadline(time.Time) error      { return nil }
func (c *chanConn) SetReadDeadline(time.Time) error  { return nil }
func (c *chanConn) SetWriteDeadline(time.Time) error { return nil }

// relayAddr stands in for the phone's address, which the office never sees.
type relayAddr struct{}

func (relayAddr) Network() string { return "relay" }
func (relayAddr) String() string  { return "relay" }
