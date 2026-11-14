package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/auth"
)

// Device-bound sessions. See docs/ARCHITECTURE.md, "Patient access is
// mobile-only": the app enrolls a Secure Enclave key, and refresh tokens
// issued after that require a signature from it.

// deviceProof is the possession proof sent with enroll and device-bound refresh.
type deviceProof struct {
	Timestamp int64  `json:"timestamp"` // unix seconds
	Signature string `json:"signature"` // base64 DER ECDSA-SHA256 over auth.DeviceProofMessage
}

func (p deviceProof) verify(pubDER []byte, purpose, refreshToken string) error {
	sig, err := base64.StdEncoding.DecodeString(p.Signature)
	if err != nil {
		return auth.ErrBadProof
	}
	return auth.VerifyDeviceProof(pubDER, purpose, refreshToken, p.Timestamp, sig, time.Now())
}

// tokenRow is a live refresh token, locked for rotation.
type tokenRow struct {
	ID, UserID, TenantID uuid.UUID
	Role                 string
	DeviceID             *uuid.UUID
	DeviceKey            []byte
	DeviceRevoked        bool
}

// lockRefreshToken finds a valid, unrevoked refresh token and locks its row.
func lockRefreshToken(ctx context.Context, tx pgx.Tx, raw string) (tokenRow, error) {
	var t tokenRow
	hash, err := auth.HashRefreshToken(raw)
	if err != nil {
		return t, pgx.ErrNoRows
	}
	err = tx.QueryRow(ctx, `
		select rt.id, rt.user_id, rt.tenant_id, u.role, rt.device_id,
		       d.public_key, coalesce(d.revoked_at is not null, false)
		from refresh_tokens rt
		join users u on u.id = rt.user_id
		left join devices d on d.id = rt.device_id
		where rt.token_hash = $1 and rt.revoked_at is null and rt.expires_at > now()
		for update of rt`, hash).
		Scan(&t.ID, &t.UserID, &t.TenantID, &t.Role, &t.DeviceID, &t.DeviceKey, &t.DeviceRevoked)
	return t, err
}

// lockRetriedRefreshToken finds a device-bound token that a refresh already
// rotated, when the phone never got the reply (#136): its successor must be
// unused and live. It locks both rows and returns the successor's ID. Tokens
// without a device never qualify, and anything that revokes the live
// successor (logout, device revocation, staff deactivation) ends the retry.
func lockRetriedRefreshToken(ctx context.Context, tx pgx.Tx, raw string) (tokenRow, uuid.UUID, error) {
	var t tokenRow
	var next uuid.UUID
	hash, err := auth.HashRefreshToken(raw)
	if err != nil {
		return t, next, pgx.ErrNoRows
	}
	err = tx.QueryRow(ctx, `
		select rt.id, rt.user_id, rt.tenant_id, u.role, rt.device_id,
		       d.public_key, d.revoked_at is not null, nx.id
		from refresh_tokens rt
		join users u on u.id = rt.user_id
		join devices d on d.id = rt.device_id
		join refresh_tokens nx on nx.id = rt.replaced_by
		where rt.token_hash = $1 and rt.revoked_at is not null and rt.expires_at > now()
		  and nx.revoked_at is null and nx.expires_at > now()
		for update of rt, nx`, hash).
		Scan(&t.ID, &t.UserID, &t.TenantID, &t.Role, &t.DeviceID, &t.DeviceKey, &t.DeviceRevoked, &next)
	return t, next, err
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
	deviceProof
}

// Refresh rotates a refresh token and returns a new token pair. A
// device-bound token needs a valid device proof. Patients may only refresh
// device-bound tokens: their sessions live on an enrolled phone.
func (s *Server) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)

	t, err := lockRefreshToken(ctx, tx, req.RefreshToken)
	var successor *uuid.UUID // set when this is a retry of a lost refresh (#136)
	if errors.Is(err, pgx.ErrNoRows) {
		var next uuid.UUID
		t, next, err = lockRetriedRefreshToken(ctx, tx, req.RefreshToken)
		if err == nil {
			successor = &next
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrCode(w, http.StatusUnauthorized, "invalid_token", "invalid or expired refresh token")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "refresh failed")
		return
	}

	switch {
	case t.DeviceID == nil && t.Role == "patient":
		writeErrCode(w, http.StatusUnauthorized, "not_enrolled", "patient sessions must be device-bound; enroll this device")
		return
	case t.DeviceID != nil && t.DeviceRevoked:
		writeErrCode(w, http.StatusUnauthorized, "device_revoked", "device revoked")
		return
	case t.DeviceID != nil:
		if err := req.verify(t.DeviceKey, auth.PurposeRefresh, req.RefreshToken); err != nil {
			// A skewed clock is the phone's to fix, not a reason to sign
			// the patient out, so it gets its own code.
			if errors.Is(err, auth.ErrProofTimestamp) {
				writeErrCode(w, http.StatusUnauthorized, "proof_timestamp", "device clock is off; check the date and time")
				return
			}
			writeErrCode(w, http.StatusUnauthorized, "proof_failed", "device proof failed")
			return
		}
		if _, err := tx.Exec(ctx, `update devices set last_seen_at = now() where id = $1`, t.DeviceID); err != nil {
			writeErr(w, http.StatusInternalServerError, "refresh failed")
			return
		}
	}

	// Retire the presented token, or on a retry the successor the phone
	// never received.
	retire, action := t.ID, "auth.refresh"
	if successor != nil {
		retire, action = *successor, "auth.refresh_retry"
	}
	if _, err := tx.Exec(ctx, `update refresh_tokens set revoked_at = now() where id = $1`, retire); err != nil {
		writeErr(w, http.StatusInternalServerError, "refresh failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: t.TenantID, ActorID: &t.UserID,
		Action: action, ResourceType: "refresh_token", ResourceID: t.ID.String(),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}

	resp, err := s.issueTokens(ctx, tx, t.UserID, t.TenantID, t.Role, t.DeviceID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "token failed")
		return
	}
	if _, err := tx.Exec(ctx, `update refresh_tokens set replaced_by = $2 where id = $1`, t.ID, resp.id); err != nil {
		writeErr(w, http.StatusInternalServerError, "refresh failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type enrollRequest struct {
	RefreshToken string `json:"refresh_token"` // unbound token from password login
	PublicKey    string `json:"public_key"`    // base64 DER SubjectPublicKeyInfo, P-256
	Name         string `json:"name"`
	deviceProof
}

type enrollResponse struct {
	DeviceID string `json:"device_id"`
	tokenResponse
}

// Enroll registers a device key and swaps an unbound refresh token (from
// password login) for a token pair bound to that device. The proof shows the
// caller holds the private key for the submitted public key.
func (s *Server) Enroll(w http.ResponseWriter, r *http.Request) {
	var req enrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	pub, err := base64.StdEncoding.DecodeString(req.PublicKey)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "public_key must be base64")
		return
	}
	if err := req.verify(pub, auth.PurposeEnroll, req.RefreshToken); err != nil {
		if errors.Is(err, auth.ErrBadDeviceKey) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeErr(w, http.StatusUnauthorized, "device proof failed")
		return
	}
	if len(req.Name) > 100 {
		req.Name = strings.ToValidUTF8(req.Name[:100], "")
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)

	t, err := lockRefreshToken(ctx, tx, req.RefreshToken)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusUnauthorized, "invalid or expired refresh token")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "enroll failed")
		return
	}
	if t.DeviceID != nil {
		writeErr(w, http.StatusConflict, "session is already bound to a device")
		return
	}

	var deviceID uuid.UUID
	err = tx.QueryRow(ctx, `
		insert into devices (tenant_id, user_id, public_key, name, last_seen_at)
		values ($1, $2, $3, $4, now()) returning id`,
		t.TenantID, t.UserID, pub, req.Name).Scan(&deviceID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeErr(w, http.StatusConflict, "device key already enrolled")
			return
		}
		writeErr(w, http.StatusInternalServerError, "enroll failed")
		return
	}
	if _, err := tx.Exec(ctx, `update refresh_tokens set revoked_at = now() where id = $1`, t.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "enroll failed")
		return
	}
	detail, _ := json.Marshal(map[string]string{"name": req.Name})
	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: t.TenantID, ActorID: &t.UserID,
		Action: "device.enroll", ResourceType: "device", ResourceID: deviceID.String(),
		Detail: detail,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}

	resp, err := s.issueTokens(ctx, tx, t.UserID, t.TenantID, t.Role, &deviceID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "token failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusCreated, enrollResponse{DeviceID: deviceID.String(), tokenResponse: resp})
}

type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Logout revokes the presented refresh token. If it is device-bound, the
// device and all of its tokens are revoked too, since the app deletes the
// device key on sign-out. Unknown or expired tokens still return 204.
func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	var req logoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)

	t, err := lockRefreshToken(ctx, tx, req.RefreshToken)
	if errors.Is(err, pgx.ErrNoRows) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "logout failed")
		return
	}

	resourceType, resourceID := "refresh_token", t.ID.String()
	if t.DeviceID != nil {
		resourceType, resourceID = "device", t.DeviceID.String()
		if _, err := tx.Exec(ctx, `update devices set revoked_at = now() where id = $1 and revoked_at is null`, t.DeviceID); err != nil {
			writeErr(w, http.StatusInternalServerError, "logout failed")
			return
		}
		if _, err := tx.Exec(ctx, `update refresh_tokens set revoked_at = now() where device_id = $1 and revoked_at is null`, t.DeviceID); err != nil {
			writeErr(w, http.StatusInternalServerError, "logout failed")
			return
		}
	} else if _, err := tx.Exec(ctx, `update refresh_tokens set revoked_at = now() where id = $1`, t.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "logout failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: t.TenantID, ActorID: &t.UserID,
		Action: "auth.logout", ResourceType: resourceType, ResourceID: resourceID,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
