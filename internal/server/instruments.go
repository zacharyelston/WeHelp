package server

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/instrumentsync"
)

// Built-in instruments (#199): PHQ-9, PHQ-2, GAD-7 and GAD-2, synced into
// each practice as read-only forms (internal/forms/instruments).
//
// The field gate: they are clinical, and PHQ-9 raises a self-harm flag.
// So they are available only in demo builds, or on a field install whose
// config sets forms_instruments, which waits for the owner to approve who
// monitors the flag and when, what patients are told, and the escalation
// steps. Even then, only for a clinical practice type with forms on.
// The flag is staff-side: nothing about it reaches the patient.

func (s *Server) instrumentsEnabled() bool { return s.cfg.FormsInstruments || demoBuild }

// instrumentsAvailable reports whether this practice has the built-ins.
func (s *Server) instrumentsAvailable(ctx context.Context, tenant uuid.UUID) (bool, error) {
	if !s.instrumentsEnabled() {
		return false, nil
	}
	t, on, err := practiceConfig(ctx, s.pool, tenant)
	if err != nil {
		return false, err
	}
	return on["forms"] && t.Clinical, nil
}

// syncInstruments puts the built-ins into the practice's library.
func (s *Server) syncInstruments(ctx context.Context, tenant uuid.UUID) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	n, err := instrumentsync.Sync(ctx, tx, tenant)
	if err != nil {
		return 0, err
	}
	return n, tx.Commit(ctx)
}

// syncAllInstruments runs the sync for every practice that has them.
func (s *Server) syncAllInstruments(ctx context.Context) (int, error) {
	if !s.instrumentsEnabled() {
		return 0, nil
	}
	rows, err := s.pool.Query(ctx, `select id from tenants`)
	if err != nil {
		return 0, err
	}
	tenants, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	total := 0
	for _, t := range tenants {
		ok, err := s.instrumentsAvailable(ctx, t)
		if err != nil || !ok {
			continue
		}
		n, err := s.syncInstruments(ctx, t)
		if err != nil {
			slog.Warn("instruments: sync failed", "tenant", t, "err", err)
			continue
		}
		total += n
	}
	return total, nil
}
