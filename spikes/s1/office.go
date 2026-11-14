package main

// Spike S1 (#32): the office side. It keeps one outbound SSH connection to
// the relay, pinning the relay's host key. Each "wehelp-conn" channel the relay
// opens is a phone connection: the office terminates TLS 1.3 on it with its own
// key (the one phones pin) and reverse-proxies to the local wehelpd.
// Throwaway code.

import (
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
)

func runOffice(keysDir, relayAddr, upstream string) error {
	keyPEM, err := os.ReadFile(filepath.Join(keysDir, "office_ssh"))
	if err != nil {
		return err
	}
	signer, err := ssh.ParsePrivateKey(keyPEM)
	if err != nil {
		return err
	}
	hostRaw, err := os.ReadFile(filepath.Join(keysDir, "relay_host.pub"))
	if err != nil {
		return err
	}
	relayHostKey, _, _, _, err := ssh.ParseAuthorizedKey(hostRaw)
	if err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(keysDir, "office_tls.crt"), filepath.Join(keysDir, "office_tls.key"))
	if err != nil {
		return err
	}
	target, err := url.Parse(upstream)
	if err != nil {
		return err
	}

	ln := newChanListener()
	tlsLn := tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
	go func() {
		srv := &http.Server{Handler: httputil.NewSingleHostReverseProxy(target), ReadHeaderTimeout: 10 * time.Second}
		_ = srv.Serve(tlsLn)
	}()

	cfg := &ssh.ClientConfig{
		User:            "office-1",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.FixedHostKey(relayHostKey), // pin the relay
		Timeout:         10 * time.Second,
	}
	backoff := time.Second
	for {
		err := connectOnce(relayAddr, cfg, ln)
		slog.Warn("relay connection ended; reconnecting", "err", err, "in", backoff)
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func connectOnce(addr string, cfg *ssh.ClientConfig, ln *chanListener) error {
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	slog.Info("connected to relay", "relay", addr)
	chans := client.HandleChannelOpen("wehelp-conn")
	for nch := range chans {
		slog.Info("channel offered", "type", nch.ChannelType())
		ch, reqs, err := nch.Accept()
		if err != nil {
			slog.Warn("channel accept failed", "err", err)
			continue
		}
		go ssh.DiscardRequests(reqs)
		ln.conns <- &chanConn{Channel: ch}
		slog.Info("channel handed to TLS server")
	}
	return errors.New("channel stream closed")
}

// chanListener hands SSH channels to the TLS/HTTP server as net.Conns.
type chanListener struct{ conns chan net.Conn }

func newChanListener() *chanListener { return &chanListener{conns: make(chan net.Conn)} }

func (l *chanListener) Accept() (net.Conn, error) { return <-l.conns, nil }
func (l *chanListener) Close() error              { return nil }
func (l *chanListener) Addr() net.Addr            { return dummyAddr{} }

type chanConn struct{ ssh.Channel }

func (c *chanConn) LocalAddr() net.Addr              { return dummyAddr{} }
func (c *chanConn) RemoteAddr() net.Addr             { return dummyAddr{} }
func (c *chanConn) SetDeadline(time.Time) error      { return nil }
func (c *chanConn) SetReadDeadline(time.Time) error  { return nil }
func (c *chanConn) SetWriteDeadline(time.Time) error { return nil }

type dummyAddr struct{}

func (dummyAddr) Network() string { return "ssh-channel" }
func (dummyAddr) String() string  { return "relay" }
