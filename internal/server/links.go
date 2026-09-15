package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zacharyelston/wehelp/internal/audit"
)

type linkView struct {
	ProviderID uuid.UUID `json:"provider_id"`
	PatientID  uuid.UUID `json:"patient_id"`
	Status     string    `json:"status"`
}

type createLinkRequest struct {
	PatientEmail string `json:"patient_email"`
}

// CreateLink lets a provider invite a patient by email -> status pending.
// Route: POST /api/v1/links
func (s *Server) CreateLink(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if id.Role != "provider" {
		writeErr(w, http.StatusForbidden, "only providers can invite patients")
		return
	}
	var req createLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.PatientEmail = strings.TrimSpace(req.PatientEmail)
	if !strings.Contains(req.PatientEmail, "@") {
		writeErr(w, http.StatusBadRequest, "valid patient_email required")
		return
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)

	var patientID uuid.UUID
	var patientRole string
	err = tx.QueryRow(ctx,
		`select id, role from users where tenant_id = $1 and email = $2`,
		id.TenantID, req.PatientEmail).Scan(&patientID, &patientRole)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "no patient with that email in this tenant")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if patientRole != "patient" {
		writeErr(w, http.StatusBadRequest, "user is not a patient")
		return
	}

	_, err = tx.Exec(ctx,
		`insert into provider_patients (tenant_id, provider_id, patient_id) values ($1, $2, $3)`,
		id.TenantID, id.UserID, patientID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeErr(w, http.StatusConflict, "link already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, "insert failed")
		return
	}

	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: id.TenantID, ActorID: &id.UserID,
		Action: "link.invite", ResourceType: "provider_patient",
		ResourceID: id.UserID.String() + ":" + patientID.String(),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusCreated, linkView{ProviderID: id.UserID, PatientID: patientID, Status: "pending"})
}

// transition moves a link between statuses when the caller is one of its
// parties. otherID is the *other* user in the pair. Returns false when no
// matching row in an allowed current state exists.
func (s *Server) transition(ctx context.Context, id Identity, otherID uuid.UUID, from []string, to, action string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		update provider_patients set status = $5
		where tenant_id = $1 and status = any($2)
		  and ((provider_id = $3 and patient_id = $4) or (provider_id = $4 and patient_id = $3))`,
		id.TenantID, from, id.UserID, otherID, to)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: id.TenantID, ActorID: &id.UserID,
		Action: action, ResourceType: "provider_patient",
		ResourceID: id.UserID.String() + ":" + otherID.String(),
	}); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// AcceptLink lets a patient accept a provider's pending invite.
// Route: POST /api/v1/links/{user_id}/accept — {user_id} is the provider.
func (s *Server) AcceptLink(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	if id.Role != "patient" {
		writeErr(w, http.StatusForbidden, "only patients can accept invites")
		return
	}
	otherID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid user id")
		return
	}
	ok, err := s.transition(r.Context(), id, otherID, []string{"pending"}, "active", "link.accept")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "update failed")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no pending invite from that provider")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "active"})
}

// RevokeLink lets either party revoke a pending or active link.
// Route: POST /api/v1/links/{user_id}/revoke — {user_id} is the other party.
func (s *Server) RevokeLink(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	otherID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid user id")
		return
	}
	ok, err := s.transition(r.Context(), id, otherID, []string{"pending", "active"}, "revoked", "link.revoke")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "update failed")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no link with that user")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// ListLinks returns every link where the caller is provider or patient.
// Route: GET /api/v1/links
func (s *Server) ListLinks(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `
		select provider_id, patient_id, status from provider_patients
		where tenant_id = $1 and (provider_id = $2 or patient_id = $2)
		order by created_at`, id.TenantID, id.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	links := []linkView{}
	for rows.Next() {
		var l linkView
		if err := rows.Scan(&l.ProviderID, &l.PatientID, &l.Status); err != nil {
			writeErr(w, http.StatusInternalServerError, "scan failed")
			return
		}
		links = append(links, l)
	}
	writeJSON(w, http.StatusOK, links)
}

// HasActiveLink reports whether an active provider-patient link exists
// between the two users (in either role order). Exported for #2's
// message-gating check.
func HasActiveLink(ctx context.Context, tx pgx.Tx, tenantID, a, b uuid.UUID) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `
		select exists(select 1 from provider_patients
			where tenant_id = $1 and status = 'active'
			  and ((provider_id = $2 and patient_id = $3) or (provider_id = $3 and patient_id = $2)))`,
		tenantID, a, b).Scan(&ok)
	return ok, err
}
