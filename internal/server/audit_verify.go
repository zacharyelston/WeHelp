package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
)

// Audit chain checks (#24): an admin checks their practice's chain from the
// staff web, and the Back Office checks every practice's chain once a day.
// The command line equivalent is `wehelpd audit verify`.

type auditVerifyView struct {
	OK       bool  `json:"ok"`
	Verified int64 `json:"verified"`            // events checked
	BrokenAt int64 `json:"broken_at,omitempty"` // seq of the first bad event
	LinkOnly int64 `json:"link_only"`           // pre-#28 events checked by link only
}

// VerifyAuditChain recomputes the caller's practice's chain. Admins only,
// on the office LAN. The check itself is recorded.
func (s *Server) VerifyAuditChain(w http.ResponseWriter, r *http.Request) {
	id, ok := adminOnLAN(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	res, err := audit.Verify(ctx, s.pool, id.TenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "verify failed")
		return
	}
	v := auditVerifyView{OK: res.BrokenAt == 0, Verified: res.Events, BrokenAt: res.BrokenAt, LinkOnly: res.LinkOnly}
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Event{TenantID: id.TenantID, ActorID: &id.UserID, Action: "audit.verify",
			ResourceType: "audit_chain", ResourceID: id.TenantID.String(), Detail: mustJSON(v)})
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, v)
}

// verifyAllChains checks every practice's chain and logs any break as an
// error (alerts come with the observability work, #54). IDs only.
func (s *Server) verifyAllChains(ctx context.Context) (broken int) {
	rows, err := s.pool.Query(ctx, `select id from tenants`)
	if err != nil {
		slog.Error("audit verify: tenants", "err", err)
		return 0
	}
	tenants, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		slog.Error("audit verify: tenants", "err", err)
		return 0
	}
	for _, t := range tenants {
		res, err := audit.Verify(ctx, s.pool, t)
		switch {
		case err != nil:
			slog.Error("audit verify failed", "tenant", t, "err", err)
		case res.BrokenAt != 0:
			broken++
			slog.Error("audit chain broken", "tenant", t, "seq", res.BrokenAt, "events", res.Events)
		default:
			slog.Info("audit chain intact", "tenant", t, "events", res.Events, "link_only", res.LinkOnly)
		}
	}
	return broken
}

// RunAuditVerify checks every chain shortly after start and then daily.
func (s *Server) RunAuditVerify(ctx context.Context) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.verifyAllChains(ctx)
			t.Reset(24 * time.Hour)
		}
	}
}
