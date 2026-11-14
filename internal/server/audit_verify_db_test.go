package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/seed"
)

// TestAuditVerifyEndpoint is #24: an admin checks their practice's chain on
// the LAN and the check is recorded; nobody else can; a tampered row is
// reported by its seq, by the endpoint and by the daily check.
func TestAuditVerifyEndpoint(t *testing.T) {
	s, pool := testBackOffice(t)
	h := s.Router()
	ctx := context.Background()
	adm := webSignIn(t, h, pool, seed.AdminEmail, seed.AdminPassword)
	ada := webSignIn(t, h, pool, seed.ProviderEmail, seed.ProviderPassword)

	if code, _ := ada.do("GET", "/staff/audit/verify", nil); code != http.StatusForbidden {
		t.Errorf("provider verifies: %d, want 403", code)
	}
	relay := &client{t: t, h: h, relay: true, cookies: adm.cookies, csrf: adm.csrf}
	if code, _ := relay.do("GET", "/staff/audit/verify", nil); code != http.StatusNotFound {
		t.Errorf("through the Relay: %d, want 404", code)
	}

	code, out := adm.do("GET", "/staff/audit/verify", nil)
	if code != http.StatusOK || out["ok"] != true || out["verified"].(float64) < 1 || out["broken_at"] != nil {
		t.Fatalf("intact chain: %d %v", code, out)
	}
	tenant := demoTenantID(t, pool)
	var checks int
	_ = pool.QueryRow(ctx, `select count(*) from audit_events where tenant_id = $1 and action = 'audit.verify'`, tenant).Scan(&checks)
	if checks != 1 {
		t.Errorf("audit.verify events = %d, want 1", checks)
	}
	if n := s.verifyAllChains(ctx); n != 0 {
		t.Errorf("daily check found %d broken chains", n)
	}

	// Tamper with one row as the database owner would have to.
	var target int64
	_ = pool.QueryRow(ctx, `select seq from audit_events where tenant_id = $1 order by seq limit 1 offset 2`, tenant).Scan(&target)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `alter table audit_events disable trigger user`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update audit_events set resource_id = 'tampered' where seq = $1`, target); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `alter table audit_events enable trigger user`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	code, out = adm.do("GET", "/staff/audit/verify", nil)
	if code != http.StatusOK || out["ok"] != false || int64(out["broken_at"].(float64)) != target {
		t.Errorf("tampered chain: %d %v, want broken at %d", code, out, target)
	}
	if n := s.verifyAllChains(ctx); n != 1 {
		t.Errorf("daily check found %d broken chains, want 1", n)
	}
}
