package server

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// Backups (#41): deploy/backup.sh records each nightly run in backup_runs.
// Admins see the recent runs, and a warning when the last backup failed or
// the last good one is too old. Sizes and checksums only, no patient data.

// backupMaxAge is how old the last good backup may be before admins are
// warned: a nightly job plus a day of slack.
const backupMaxAge = 36 * time.Hour

type backupRun struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	OK         bool      `json:"ok"`
	Bytes      int64     `json:"bytes"`
	CopiedTo   string    `json:"copied_to"`
	Error      string    `json:"error,omitempty"`
}

type backupStatus struct {
	LastOKAt *time.Time  `json:"last_ok_at"`
	Warning  string      `json:"warning"` // empty when all is well
	Runs     []backupRun `json:"runs"`    // newest first, at most 10
}

func (s *Server) backupStatus(ctx context.Context, now time.Time) (backupStatus, error) {
	st := backupStatus{Runs: []backupRun{}}
	rows, err := s.pool.Query(ctx, `
		select started_at, finished_at, ok, bytes, copied_to, error
		from backup_runs order by finished_at desc limit 10`)
	if err != nil {
		return st, err
	}
	st.Runs, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (backupRun, error) {
		var b backupRun
		err := row.Scan(&b.StartedAt, &b.FinishedAt, &b.OK, &b.Bytes, &b.CopiedTo, &b.Error)
		return b, err
	})
	if err != nil {
		return st, err
	}
	if err := s.pool.QueryRow(ctx, `select max(finished_at) from backup_runs where ok`).Scan(&st.LastOKAt); err != nil {
		return st, err
	}
	switch {
	case len(st.Runs) == 0:
		st.Warning = "No backup has run on this Back Office. Set up nightly backups (docs/BACKUPS.md)."
	case !st.Runs[0].OK:
		st.Warning = "The last backup failed: " + st.Runs[0].Error
	case st.LastOKAt == nil || now.Sub(*st.LastOKAt) > backupMaxAge:
		st.Warning = "The last good backup is more than 36 hours old. Check the nightly backup job."
	}
	return st, nil
}

// GetBackups shows recent backup runs. Admins only, on the office LAN.
func (s *Server) GetBackups(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminOnLAN(w, r); !ok {
		return
	}
	st, err := s.backupStatus(r.Context(), time.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "backup status failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, st)
}
