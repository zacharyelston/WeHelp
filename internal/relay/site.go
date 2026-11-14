package relay

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// Practice websites (#97, #5). An office publishes its site by opening a
// "wehelp-site" channel on its SSH connection and writing one tar. The Relay
// files it under the office ID that connection authenticated as, so an
// office can only ever write its own site, and serves it read-only at
// /s/<office-id>/ with no scripts allowed. Sites are practice content, never
// patient data.

// SiteChannelType is the one channel type an office may open.
const SiteChannelType = "wehelp-site"

// Site limits. internal/site in the Back Office keeps the same numbers.
const (
	MaxSiteBytes     = 25 << 20
	MaxSiteFiles     = 200
	MaxSiteFileBytes = 10 << 20
	sitePublishGap   = 5 * time.Second // between publishes, per office
)

// SiteTypes are the only files a site may contain.
var SiteTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".txt":  "text/plain; charset=utf-8",
	".pdf":  "application/pdf",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".ico":  "image/x-icon",
}

// siteCSP forbids scripts, frames, forms and anything off the site.
const siteCSP = "default-src 'none'; style-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// A site path is one or two lower-case segments: "index.html", "files/x.pdf".
var sitePathRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}(/[a-z0-9][a-z0-9._-]{0,99})?$`)

func validSitePath(p string) bool {
	if !sitePathRE.MatchString(p) || strings.Contains(p, "..") {
		return false
	}
	_, ok := SiteTypes[path.Ext(p)]
	return ok
}

// ReadSiteBundle reads and checks a tar: regular files only, allowed
// names and types, no HTML with scripts, within the limits, and an
// index.html.
func ReadSiteBundle(r io.Reader) (map[string][]byte, error) {
	lr := &io.LimitedReader{R: r, N: MaxSiteBytes + 1}
	tr := tar.NewReader(lr)
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if lr.N <= 0 {
				return nil, errors.New("site is larger than 25 MB")
			}
			return nil, errors.New("not a valid tar bundle")
		}
		if h.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("%q is not a regular file", h.Name)
		}
		if !validSitePath(h.Name) {
			return nil, fmt.Errorf("%q is not an allowed site file", h.Name)
		}
		if h.Size > MaxSiteFileBytes {
			return nil, fmt.Errorf("%q is larger than 10 MB", h.Name)
		}
		if _, dup := files[h.Name]; dup {
			return nil, fmt.Errorf("%q appears twice", h.Name)
		}
		if len(files) >= MaxSiteFiles {
			return nil, errors.New("site has more than 200 files")
		}
		data, err := io.ReadAll(io.LimitReader(tr, MaxSiteFileBytes+1))
		if err != nil {
			if lr.N <= 0 {
				return nil, errors.New("site is larger than 25 MB")
			}
			return nil, errors.New("not a valid tar bundle")
		}
		if strings.HasSuffix(h.Name, ".html") && bytes.Contains(bytes.ToLower(data), []byte("<script")) {
			return nil, fmt.Errorf("%q contains a script; sites are static", h.Name)
		}
		files[h.Name] = data
	}
	if lr.N <= 0 {
		return nil, errors.New("site is larger than 25 MB")
	}
	if files["index.html"] == nil {
		return nil, errors.New("site has no index.html")
	}
	return files, nil
}

type siteReply struct {
	OK    bool   `json:"ok"`
	Files int    `json:"files,omitempty"`
	Bytes int64  `json:"bytes,omitempty"`
	Path  string `json:"path,omitempty"`
	URL   string `json:"url,omitempty"`
	Error string `json:"error,omitempty"`
}

// handleSiteChannel receives one publish from an office.
func (r *Relay) handleSiteChannel(officeID string, nch ssh.NewChannel) {
	if r.cfg.SitesDir == "" {
		_ = nch.Reject(ssh.Prohibited, "this relay does not host websites")
		return
	}
	ch, reqs, err := nch.Accept()
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	defer ch.Close()
	reply := func(rep siteReply) {
		b, _ := json.Marshal(rep)
		_, _ = ch.Write(b)
		_ = ch.CloseWrite()
	}
	if !r.allowPublish(officeID, time.Now()) {
		reply(siteReply{Error: "publishing too often; wait a few seconds"})
		return
	}
	files, err := ReadSiteBundle(ch)
	if err != nil {
		slog.Warn("relay: site refused", "office", officeID, "reason", err.Error())
		reply(siteReply{Error: err.Error()})
		return
	}
	n, err := r.storeSite(officeID, files)
	if err != nil {
		slog.Error("relay: site store failed", "office", officeID, "err", err)
		reply(siteReply{Error: "the relay could not store the site"})
		return
	}
	p := "/s/" + officeID + "/"
	rep := siteReply{OK: true, Files: len(files), Bytes: n, Path: p}
	if r.cfg.SitePublicURL != "" {
		rep.URL = strings.TrimRight(r.cfg.SitePublicURL, "/") + p
	}
	slog.Info("relay: site published", "office", officeID, "files", len(files), "bytes", n)
	reply(rep)
}

func (r *Relay) allowPublish(officeID string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if last, ok := r.published[officeID]; ok && now.Sub(last) < sitePublishGap {
		return false
	}
	r.published[officeID] = now
	return true
}

// storeSite writes the files to a fresh directory and swaps it in, so a
// visitor sees either the old site or the new one, never half of each.
func (r *Relay) storeSite(officeID string, files map[string][]byte) (int64, error) {
	if _, err := uuid.Parse(officeID); err != nil {
		return 0, errors.New("bad office id")
	}
	if err := os.MkdirAll(r.cfg.SitesDir, 0o755); err != nil {
		return 0, err
	}
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	tmp := filepath.Join(r.cfg.SitesDir, ".new-"+officeID+"-"+hex.EncodeToString(suffix))
	var total int64
	for name, data := range files {
		dst := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return 0, err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			_ = os.RemoveAll(tmp)
			return 0, err
		}
		total += int64(len(data))
	}
	final := filepath.Join(r.cfg.SitesDir, officeID)
	old := tmp + ".old"
	r.siteMu.Lock()
	defer r.siteMu.Unlock()
	if err := os.Rename(final, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.RemoveAll(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Rename(old, final)
		_ = os.RemoveAll(tmp)
		return 0, err
	}
	_ = os.RemoveAll(old)
	return total, nil
}

// SiteHandler serves published sites: GET and HEAD of /s/<office-id>/<file>.
// Sites of offices not on the current list are not served.
func (r *Relay) SiteHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", siteCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rest, ok := strings.CutPrefix(req.URL.Path, "/s/")
		if !ok {
			http.NotFound(w, req)
			return
		}
		id, file, slash := strings.Cut(rest, "/")
		if _, err := uuid.Parse(id); err != nil || len(id) != 36 || id != strings.ToLower(id) || !r.listed(id) {
			http.NotFound(w, req)
			return
		}
		if !slash {
			http.Redirect(w, req, "/s/"+id+"/", http.StatusMovedPermanently)
			return
		}
		if file == "" {
			file = "index.html"
		}
		if !validSitePath(file) {
			http.NotFound(w, req)
			return
		}
		r.siteMu.RLock()
		data, err := os.ReadFile(filepath.Join(r.cfg.SitesDir, id, filepath.FromSlash(file)))
		r.siteMu.RUnlock()
		if err != nil {
			http.NotFound(w, req)
			return
		}
		h.Set("Content-Type", SiteTypes[path.Ext(file)])
		h.Set("Cache-Control", "public, max-age=300")
		if path.Dir(file) == "files" && path.Ext(file) != ".png" && path.Ext(file) != ".jpg" && path.Ext(file) != ".jpeg" && path.Ext(file) != ".webp" {
			h.Set("Content-Disposition", "attachment")
		}
		http.ServeContent(w, req, "", time.Time{}, bytes.NewReader(data))
	})
}

// listed reports whether an office is on the current list for this Relay.
func (r *Relay) listed(officeID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range r.allowed {
		if id == officeID {
			return true
		}
	}
	return false
}

// ServeSites serves published sites over TLS on ln until ctx ends. Visitor
// addresses are not logged.
func (r *Relay) ServeSites(ctx context.Context, ln net.Listener, cert tls.Certificate) error {
	srv := &http.Server{
		Handler:           r.SiteHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	err := srv.ServeTLS(ln, "", "")
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
