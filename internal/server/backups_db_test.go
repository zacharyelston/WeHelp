package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zacharyelston/wehelp/internal/seed"
)

// TestBackupStatus is #41's alert: admins are warned when no backup has
// run, when the last one failed, and when the last good one is over 36
// hours old; never otherwise. Only admins, on the LAN.
func TestBackupStatus(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	ctx := context.Background()
	adm := webSignIn(t, h, pool, seed.AdminEmail, seed.AdminPassword)
	ada := webSignIn(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)
	if code, _ := ada.do("GET", "/staff/backups", nil); code != http.StatusForbidden {
		t.Errorf("provider: %d, want 403", code)
	}
	relay := &client{t: t, h: h, relay: true, cookies: adm.cookies, csrf: adm.csrf}
	if code, _ := relay.do("GET", "/staff/backups", nil); code != http.StatusNotFound {
		t.Errorf("through the Relay: %d, want 404", code)
	}
	warning := func() string {
		t.Helper()
		code, out := adm.do("GET", "/staff/backups", nil)
		if code != http.StatusOK {
			t.Fatalf("backups: %d %v", code, out)
		}
		return out["warning"].(string)
	}
	run := func(ago time.Duration, ok bool, errText string) {
		t.Helper()
		at := time.Now().Add(-ago)
		if _, err := pool.Exec(ctx, `insert into backup_runs (started_at, finished_at, ok, bytes, error) values ($1, $1, $2, 1000, $3)`,
			at, ok, errText); err != nil {
			t.Fatal(err)
		}
	}

	if w := warning(); !strings.Contains(w, "No backup has run") {
		t.Errorf("no runs: %q", w)
	}
	run(48*time.Hour, true, "")
	if w := warning(); !strings.Contains(w, "more than 36 hours") {
		t.Errorf("stale: %q", w)
	}
	run(2*time.Hour, true, "")
	if w := warning(); w != "" {
		t.Errorf("recent good backup: %q", w)
	}
	run(time.Hour, false, "copy target /Volumes/PNY1TB isn't there (drive not mounted?)")
	if w := warning(); !strings.Contains(w, "failed") || !strings.Contains(w, "drive not mounted") {
		t.Errorf("failed: %q", w)
	}
	run(time.Minute, true, "")
	if w := warning(); w != "" {
		t.Errorf("good again: %q", w)
	}
	_, out := adm.do("GET", "/staff/backups", nil)
	if runs := out["runs"].([]any); len(runs) != 4 {
		t.Errorf("runs listed: %d", len(runs))
	}
}
