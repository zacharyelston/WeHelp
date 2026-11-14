package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/practice"
)

// Practice settings (#108). The practice type sets the words the staff
// web, website and app use, and whether the practice is clinical. Anyone
// signed in reads it (the app shows the words); only an admin on the
// office LAN changes it.

// practiceType returns the tenant's preset (the default if none is set).
func practiceType(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, tenant uuid.UUID) (practice.Type, error) {
	var id string
	err := q.QueryRow(ctx, `select practice_type from practice_settings where tenant_id = $1`, tenant).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return practice.Default, nil
	}
	if err != nil {
		return practice.Type{}, err
	}
	if t, ok := practice.Lookup(id); ok {
		return t, nil
	}
	return practice.Default, nil
}

type practiceView struct {
	Type  practice.Type   `json:"type"`
	Types []practice.Type `json:"types"` // the choices, for the Settings screen
}

// GetPractice returns the practice type and its words. Any signed-in user.
// Route: GET /api/v1/practice
func (s *Server) GetPractice(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	t, err := practiceType(r.Context(), s.pool, id.TenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	writeJSON(w, http.StatusOK, practiceView{Type: t, Types: practice.Types})
}

type putPracticeRequest struct {
	Type string `json:"type"`
}

// PutPractice sets the practice type. Admins on the office LAN; audited.
// Route: PUT /api/v1/practice
func (s *Server) PutPractice(w http.ResponseWriter, r *http.Request) {
	id, ok := adminOnLAN(w, r)
	if !ok {
		return
	}
	var req putPracticeRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	t, known := practice.Lookup(req.Type)
	if !known {
		writeErr(w, http.StatusBadRequest, "unknown practice type")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	prev, err := practiceType(ctx, tx, id.TenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	if _, err := tx.Exec(ctx, `
		insert into practice_settings (tenant_id, practice_type, updated_by) values ($1, $2, $3)
		on conflict (tenant_id) do update set practice_type = excluded.practice_type,
			updated_by = excluded.updated_by, updated_at = now()`, id.TenantID, t.ID, id.UserID); err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{TenantID: id.TenantID, ActorID: &id.UserID, Action: "practice.update",
		ResourceType: "tenant", ResourceID: id.TenantID.String(),
		Detail: mustJSON(map[string]string{"practice_type": t.ID, "previous": prev.ID})}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, practiceView{Type: t, Types: practice.Types})
}
