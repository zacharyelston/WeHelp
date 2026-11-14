package main

// Spike S1 (#32): the relay. Offices connect over SSH with a registered key;
// phones send HTTP CONNECT <office-id>.wehelp.invalid:443 over TLS, and the
// relay splices the connection onto an SSH channel to that office. The relay
// never sees inside the phone↔office TLS. Throwaway code.

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/ssh"
)

type relay struct {
	mu      sync.Mutex
	offices map[string]*ssh.ServerConn // office ID → live SSH connection
	dumpDir string                     // optional: record spliced bytes for the plaintext check
	seq     atomic.Int64
}

func runRelay(keysDir, sshAddr, connectAddr, dumpDir string) error {
	r := &relay{offices: map[string]*ssh.ServerConn{}, dumpDir: dumpDir}

	hostKeyPEM, err := os.ReadFile(filepath.Join(keysDir, "relay_host"))
	if err != nil {
		return err
	}
	hostSigner, err := ssh.ParsePrivateKey(hostKeyPEM)
	if err != nil {
		return err
	}
	authorizedRaw, err := os.ReadFile(filepath.Join(keysDir, "office_ssh.pub"))
	if err != nil {
		return err
	}
	authorized, _, _, _, err := ssh.ParseAuthorizedKey(authorizedRaw)
	if err != nil {
		return err
	}

	sshCfg := &ssh.ServerConfig{
		// Stand-in for the Registry's signed office list: one office, one key.
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() == "office-1" && bytes.Equal(key.Marshal(), authorized.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("unknown office %q", meta.User())
		},
	}
	sshCfg.AddHostKey(hostSigner)

	sshLn, err := net.Listen("tcp", sshAddr)
	if err != nil {
		return err
	}
	go r.acceptOffices(sshLn, sshCfg)

	cert, err := tls.LoadX509KeyPair(filepath.Join(keysDir, "relay_tls.crt"), filepath.Join(keysDir, "relay_tls.key"))
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:    connectAddr,
		Handler: http.HandlerFunc(r.handleConnect),
		// HTTP/1.1 only: CONNECT needs a hijackable connection.
		TLSConfig:    &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}},
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	slog.Info("relay listening", "ssh", sshAddr, "connect", connectAddr)
	return srv.ListenAndServeTLS("", "")
}

func (r *relay) acceptOffices(ln net.Listener, cfg *ssh.ServerConfig) {
	for {
		nc, err := ln.Accept()
		if err != nil {
			slog.Error("ssh accept", "err", err)
			return
		}
		go func() {
			conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
			if err != nil {
				slog.Warn("office rejected", "remote", nc.RemoteAddr(), "err", err)
				return
			}
			id := conn.User()
			slog.Info("office connected", "office", id)
			r.mu.Lock()
			r.offices[id] = conn
			r.mu.Unlock()
			go ssh.DiscardRequests(reqs)
			// The relay never accepts channels from an office.
			go func() {
				for nch := range chans {
					_ = nch.Reject(ssh.Prohibited, "relay accepts no channels")
				}
			}()
			_ = conn.Wait()
			r.mu.Lock()
			if r.offices[id] == conn {
				delete(r.offices, id)
			}
			r.mu.Unlock()
			slog.Info("office disconnected", "office", id)
		}()
	}
}

func (r *relay) handleConnect(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodConnect {
		http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
		return
	}
	host, _, err := net.SplitHostPort(req.Host)
	if err != nil || !strings.HasSuffix(host, ".wehelp.invalid") {
		http.Error(w, "unknown office", http.StatusNotFound)
		return
	}
	officeID := strings.TrimSuffix(host, ".wehelp.invalid")
	r.mu.Lock()
	office := r.offices[officeID]
	r.mu.Unlock()
	if office == nil {
		http.Error(w, "office offline", http.StatusBadGateway)
		return
	}
	slog.Info("opening channel", "office", officeID)
	ch, reqs, err := office.OpenChannel("wehelp-conn", nil)
	slog.Info("channel open result", "office", officeID, "err", err)
	if err != nil {
		http.Error(w, "office channel failed", http.StatusBadGateway)
		return
	}
	go ssh.DiscardRequests(reqs)

	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = ch.Close()
		http.Error(w, "no hijack", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		_ = ch.Close()
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	n := r.seq.Add(1)
	toOffice, toPhone := r.dump(n, "phone-to-office"), r.dump(n, "office-to-phone")
	var up, down atomic.Int64
	done := make(chan struct{}, 2)
	go func() { // phone → office (including anything already buffered)
		src := io.MultiReader(buf, client)
		c, _ := io.Copy(io.MultiWriter(ch, toOffice), src)
		up.Store(c)
		_ = ch.CloseWrite()
		done <- struct{}{}
	}()
	go func() { // office → phone
		c, _ := io.Copy(io.MultiWriter(client, toPhone), ch)
		down.Store(c)
		done <- struct{}{}
	}()
	<-done
	<-done
	_ = client.Close()
	_ = ch.Close()
	_ = toOffice.Close()
	_ = toPhone.Close()
	// Metadata only: office ID and byte counts. No device addresses.
	slog.Info("splice closed", "office", officeID, "conn", n, "bytes_up", up.Load(), "bytes_down", down.Load())
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func (r *relay) dump(n int64, dir string) io.WriteCloser {
	if r.dumpDir == "" {
		return nopWriteCloser{io.Discard}
	}
	f, err := os.Create(filepath.Join(r.dumpDir, fmt.Sprintf("conn-%03d-%s.bin", n, dir)))
	if err != nil {
		return nopWriteCloser{io.Discard}
	}
	return f
}
