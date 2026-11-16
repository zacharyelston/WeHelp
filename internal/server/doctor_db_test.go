package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zacharyelston/wehelp/internal/doctor"
	"github.com/zacharyelston/wehelp/internal/tunnel"
)

type connectedPusher struct{ up bool }

func (p connectedPusher) Push(context.Context, tunnel.PushRequest) (string, error) {
	return "sent", nil
}
func (p connectedPusher) Connected() bool { return p.up }

// TestDoctorDB is #81: the doctor's database checks against a real (seeded)
// Back Office, and /readyz reporting the tunnel.
func TestDoctorDB(t *testing.T) {
	s, pool := testBackOffice(t)
	ctx := context.Background()
	verify, lastBackup, seedAccounts := doctor.FromDB(pool)

	events, broken, err := verify(ctx)
	if err != nil || events == 0 || broken != 0 {
		t.Errorf("verify: %d events, %d broken, %v", events, broken, err)
	}
	if _, ok, err := lastBackup(ctx); ok || err != nil {
		t.Errorf("last backup on a new office: ok %v err %v", ok, err)
	}
	// The demo seed's accounts are exactly what the check must catch.
	if n, err := seedAccounts(ctx); err != nil || n < 5 {
		t.Errorf("seed accounts = %d, %v", n, err)
	}
	if _, err := pool.Exec(ctx, `insert into backup_runs (started_at, finished_at, ok) values (now() - interval '2 hours', now() - interval '1 hour', true),
		(now() - interval '10 minutes', now(), false)`); err != nil {
		t.Fatal(err)
	}
	if at, ok, err := lastBackup(ctx); !ok || err != nil || time.Since(at) < 50*time.Minute {
		t.Errorf("last good backup %v ok %v err %v: want the successful one, not the failed later run", at, ok, err)
	}

	relay := func() string {
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != http.StatusOK || body["status"] != "ok" {
			t.Errorf("readyz %d %v", rec.Code, body)
		}
		return body["relay"]
	}
	if got := relay(); got != "off" {
		t.Errorf("no tunnel: relay %q, want off", got)
	}
	s.SetPusher(connectedPusher{up: false})
	if got := relay(); got != "down" {
		t.Errorf("tunnel down: relay %q", got)
	}
	s.SetPusher(connectedPusher{up: true})
	if got := relay(); got != "connected" {
		t.Errorf("tunnel up: relay %q", got)
	}
}
