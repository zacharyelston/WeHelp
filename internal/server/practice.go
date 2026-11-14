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
func practiceType(ctx context.Context, q rowQuerier, tenant uuid.UUID) (practice.Type, error) {
	t, _, err := practiceConfig(ctx, q, tenant)
	return t, err
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// practiceConfig returns the tenant's preset, the admin's switch overrides
// and every switch's effective state (#109).
func practiceConfig(ctx context.Context, q rowQuerier, tenant uuid.UUID) (practice.Type, map[string]bool, error) {
	var id string
	var raw []byte
	err := q.QueryRow(ctx, `select practice_type, features from practice_settings where tenant_id = $1`, tenant).Scan(&id, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return practice.Default, practice.Effective(practice.Default, nil), nil
	}
	if err != nil {
		return practice.Type{}, nil, err
	}
	t, ok := practice.Lookup(id)
	if !ok {
		t = practice.Default
	}
	overrides := map[string]bool{}
	_ = json.Unmarshal(raw, &overrides)
	return t, practice.Effective(t, overrides), nil
}

// featureOff writes a 409 and returns true if the practice has turned
// feature off (#109), so old app versions can't use it either.
func featureOff(w http.ResponseWriter, ctx context.Context, q rowQuerier, tenant uuid.UUID, feature string) bool {
	_, on, err := practiceConfig(ctx, q, tenant)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "settings lookup failed")
		return true
	}
	if !on[feature] {
		writeErr(w, http.StatusConflict, practice.FeatureLabel(feature)+" is turned off for this practice")
		return true
	}
	return false
}

type practiceView struct {
	Type     practice.Type      `json:"type"`
	Types    []practice.Type    `json:"types"`    // the choices, for the Settings screen
	Features map[string]bool    `json:"features"` // effective switches (#109)
	Switches []practice.Feature `json:"switches"` // their labels, in order
}

func newPracticeView(t practice.Type, on map[string]bool) practiceView {
	return practiceView{Type: t, Types: practice.Types, Features: on, Switches: practice.Features}
}

// GetPractice returns the practice type and its words. Any signed-in user.
// Route: GET /api/v1/practice
func (s *Server) GetPractice(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	t, on, err := practiceConfig(r.Context(), s.pool, id.TenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	writeJSON(w, http.StatusOK, newPracticeView(t, on))
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
	_, on, err := practiceConfig(ctx, tx, id.TenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, newPracticeView(t, on))
}

type putFeaturesRequest struct {
	Features map[string]bool `json:"features"`
}

// PutPracticeFeatures sets the admin's switches (#109): only the ones sent
// change. Admins on the office LAN; audited as practice.features.
// Route: PUT /api/v1/practice/features
func (s *Server) PutPracticeFeatures(w http.ResponseWriter, r *http.Request) {
	id, ok := adminOnLAN(w, r)
	if !ok {
		return
	}
	var req putFeaturesRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || len(req.Features) == 0 {
		writeErr(w, http.StatusBadRequest, "send {\"features\": {\"name\": true|false}}")
		return
	}
	for k := range req.Features {
		if !practice.IsFeature(k) {
			writeErr(w, http.StatusBadRequest, "unknown feature "+k)
			return
		}
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	raw, _ := json.Marshal(req.Features)
	if _, err := tx.Exec(ctx, `
		insert into practice_settings (tenant_id, features, updated_by) values ($1, $2, $3)
		on conflict (tenant_id) do update set features = practice_settings.features || excluded.features,
			updated_by = excluded.updated_by, updated_at = now()`, id.TenantID, raw, id.UserID); err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{TenantID: id.TenantID, ActorID: &id.UserID, Action: "practice.features",
		ResourceType: "tenant", ResourceID: id.TenantID.String(), Detail: raw}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	t, on, err := practiceConfig(ctx, tx, id.TenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, newPracticeView(t, on))
}
