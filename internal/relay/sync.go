package relay

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zacharyelston/wehelp/internal/officelist"
)

// SyncResult says what one sync did.
type SyncResult int

const (
	Unchanged SyncResult = iota
	Updated
)

// ErrOlderList means the Registry served a list older than the one in force.
var ErrOlderList = errors.New("relay: Registry served an older office list; keeping the last good one")

func (r *Relay) syncLoop(ctx context.Context) {
	t := time.NewTicker(r.cfg.ListInterval)
	defer t.Stop()
	for {
		if _, err := r.Sync(ctx); err != nil && ctx.Err() == nil {
			// Keep serving with the last good list.
			slog.Warn("relay: office list sync failed", "err", err, "version", r.Version())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Sync fetches the Registry's list once, verifies it against the pinned
// key, and applies it if it is newer than the list in force.
func (r *Relay) Sync(ctx context.Context) (SyncResult, error) {
	url := strings.TrimRight(r.cfg.RegistryURL, "/") + "/v1/office-list"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Unchanged, err
	}
	resp, err := r.cfg.HTTPClient.Do(req)
	if err != nil {
		return Unchanged, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Unchanged, fmt.Errorf("relay: office list: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Unchanged, err
	}
	var env officelist.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Unchanged, fmt.Errorf("relay: office list: %w", err)
	}
	l, err := officelist.Verify(r.cfg.RegistryKey, env)
	if err != nil {
		return Unchanged, err
	}
	cur := r.Version()
	switch {
	case l.Version == cur:
		return Unchanged, nil
	case l.Version < cur:
		return Unchanged, ErrOlderList
	}
	r.apply(l)
	if r.cfg.CachePath != "" {
		if err := saveCache(r.cfg.CachePath, raw); err != nil {
			slog.Warn("relay: could not cache office list", "err", err)
		}
	}
	slog.Info("relay: office list updated", "version", l.Version, "from", cur)
	return Updated, nil
}

// loadCache reads and re-verifies the cached envelope. A missing cache is
// (nil, nil).
func loadCache(path string, key ed25519.PublicKey) (*officelist.List, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var env officelist.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	l, err := officelist.Verify(key, env)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func saveCache(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".office-list.tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
