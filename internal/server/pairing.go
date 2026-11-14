package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/auth"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/tunnel"
	"rsc.io/qr"
)

// QR pairing (#37). Patients have no password. In the office:
//
//  1. Staff press Pair phone. The server issues a single-use invite (10
//     minutes, bound to one patient) and shows it in a QR code with the
//     office's pairing identity (Relay address and pin, office ID and pin).
//  2. The phone scans it, makes a Secure Enclave key, reaches the office
//     through the Relay with both pins checked, and redeems the invite with a
//     proof from that key. That enrolls the device and returns its
//     device-bound session. From then on it refreshes with the device key.
//
// The invite is 128 random bits; only its hash is stored, bound to the
// patient here in the Back Office. Nothing about it goes to the Relay.
// Issuing is a staff action on the office LAN and is refused on Relay
// channels; redeeming is what phones call, so it arrives through the Relay.

const (
	inviteTTL   = 10 * time.Minute // redeem deadline
	inviteBytes = 16
)

// purposePair is the device proof purpose for redeeming, signed like
// auth.DeviceProofMessage with the invite code in place of the refresh token.
const purposePair = "pair"

func hashInvite(code string) []byte {
	sum := sha256.Sum256([]byte(code))
	return sum[:]
}

// SetPairing gives the server the office's pairing identity. Without it
// (office not installed with the Registry) invites can't be issued.
func (s *Server) SetPairing(p office.Pairing) { s.pairing = &p }

// staffOnLAN checks that the caller is staff and the request did not come
// through the Relay.
func staffOnLAN(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	id, ok := identityFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return id, false
	}
	if id.Role != "provider" && id.Role != "admin" {
		writeErr(w, http.StatusForbidden, "staff only")
		return id, false
	}
	if tunnel.ViaRelay(r.Context()) {
		writeErr(w, http.StatusForbidden, "pairing is done in the office, on the office network")
		return id, false
	}
	return id, true
}

// QRPayload is what the QR code holds. The app parses it (OfficeEndpoint
// plus the invite); field names match office.Pairing.
type QRPayload struct {
	Version int `json:"v"`
	office.Pairing
	Invite string `json:"invite"`
}

type createInviteRequest struct {
	PatientID string `json:"patient_id"`
}

type inviteResponse struct {
	ID          string    `json:"id"`
	PatientID   string    `json:"patient_id"`
	PatientName string    `json:"patient_name"`
	ExpiresAt   time.Time `json:"expires_at"`
	QRPayload   string    `json:"qr_payload"`
	QRPNG       string    `json:"qr_png"` // base64 PNG
}

// CreateInvite issues a pairing invite for one patient (staff, LAN only).
func (s *Server) CreateInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	if s.pairing == nil {
		writeErr(w, http.StatusServiceUnavailable, "this office is not installed with the Registry yet (wehelpd init --install-token)")
		return
	}
	var req createInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	patientID, err := uuid.Parse(req.PatientID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "patient_id must be a UUID")
		return
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)

	name, err := staffPatient(ctx, tx, id, patientID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such patient for you")
		return
	}

	raw := make([]byte, inviteBytes)
	if _, err := rand.Read(raw); err != nil {
		writeErr(w, http.StatusInternalServerError, "random failed")
		return
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().Add(inviteTTL).UTC().Truncate(time.Second)
	var inviteID uuid.UUID
	if err := tx.QueryRow(ctx, `
		insert into pairing_invites (tenant_id, patient_id, created_by, code_hash, expires_at)
		values ($1, $2, $3, $4, $5) returning id`,
		id.TenantID, patientID, id.UserID, hashInvite(code), expires).Scan(&inviteID); err != nil {
		writeErr(w, http.StatusInternalServerError, "invite failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: id.TenantID, ActorID: &id.UserID,
		Action: "pairing.invite", ResourceType: "pairing_invite", ResourceID: inviteID.String(),
		Detail: mustJSON(map[string]string{"patient_id": patientID.String(), "expires_at": expires.Format(time.RFC3339)}),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}

	payload, _ := json.Marshal(QRPayload{Version: 1, Pairing: *s.pairing, Invite: code})
	png, err := qrPNG(string(payload))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "qr failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, inviteResponse{
		ID: inviteID.String(), PatientID: patientID.String(), PatientName: name, ExpiresAt: expires,
		QRPayload: string(payload), QRPNG: base64.StdEncoding.EncodeToString(png),
	})
}

func qrPNG(text string) ([]byte, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil, err
	}
	c.Scale = 8
	return c.PNG(), nil
}

// staffPatient returns the patient's display name if the staff member may
// act for them: admins for any patient in the tenant, providers for patients
// linked to them (pending or active).
func staffPatient(ctx context.Context, tx pgx.Tx, staff Identity, patientID uuid.UUID) (string, error) {
	var name string
	q := `select u.display_name from users u where u.id = $1 and u.tenant_id = $2 and u.role = 'patient'`
	args := []any{patientID, staff.TenantID}
	if staff.Role != "admin" {
		q += ` and exists (select 1 from provider_patients pp where pp.provider_id = $3
			and pp.patient_id = u.id and pp.status <> 'revoked')`
		args = append(args, staff.UserID)
	}
	err := tx.QueryRow(ctx, q, args...).Scan(&name)
	return name, err
}

type inviteStatus struct {
	ID         string `json:"id"`
	Status     string `json:"status"` // issued, paired, expired
	DeviceName string `json:"device_name,omitempty"`
}

type inviteRow struct {
	ID, TenantID, PatientID uuid.UUID
	ExpiresAt               time.Time
	RedeemedAt              *time.Time
	DeviceID                *uuid.UUID
}

const inviteCols = `id, tenant_id, patient_id, expires_at, redeemed_at, device_id`

func scanInvite(row pgx.Row) (inviteRow, error) {
	var v inviteRow
	err := row.Scan(&v.ID, &v.TenantID, &v.PatientID, &v.ExpiresAt, &v.RedeemedAt, &v.DeviceID)
	return v, err
}

func (v inviteRow) status(now time.Time) string {
	switch {
	case v.RedeemedAt != nil:
		return "paired"
	case !now.Before(v.ExpiresAt):
		return "expired"
	}
	return "issued"
}

// GetInvite reports whether an invite has been used, for the staff screen
// (staff, LAN only).
func (s *Server) GetInvite(w http.ResponseWriter, r *http.Request) {
	staff, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	inviteID, err := uuid.Parse(chi.URLParam(r, "inviteID"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "invite not found")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	v, err := scanInvite(tx.QueryRow(ctx, `select `+inviteCols+` from pairing_invites where id = $1 and tenant_id = $2`,
		inviteID, staff.TenantID))
	if err == nil {
		_, err = staffPatient(ctx, tx, staff, v.PatientID)
	}
	if err != nil {
		writeErr(w, http.StatusNotFound, "invite not found")
		return
	}
	st := inviteStatus{ID: v.ID.String(), Status: v.status(time.Now())}
	if v.DeviceID != nil {
		_ = tx.QueryRow(ctx, `select name from devices where id = $1`, v.DeviceID).Scan(&st.DeviceName)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, st)
}

type redeemRequest struct {
	Invite    string `json:"invite"`
	PublicKey string `json:"public_key"` // base64 DER SPKI, P-256
	Name      string `json:"name"`
	deviceProof
}

type redeemResponse struct {
	Practice    string `json:"practice"`
	PatientName string `json:"patient_name"`
	enrollResponse
}

// errInvite is the one answer for every invite failure (unknown, used,
// expired), so the endpoint is no oracle.
const errInvite = "invalid or expired invite"

// RedeemInvite is called by the phone through the Relay. A valid, unused,
// unexpired invite plus a proof from the phone's new key enrolls the device
// for the invite's patient and returns its device-bound session.
func (s *Server) RedeemInvite(w http.ResponseWriter, r *http.Request) {
	var req redeemRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil || req.Invite == "" {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	pub, err := base64.StdEncoding.DecodeString(req.PublicKey)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "public_key must be base64")
		return
	}
	if err := req.verify(pub, purposePair, req.Invite); err != nil {
		if errors.Is(err, auth.ErrBadDeviceKey) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeErr(w, http.StatusUnauthorized, "device proof failed")
		return
	}
	name := strings.TrimSpace(req.Name)
	if len(name) > 100 {
		name = strings.ToValidUTF8(name[:100], "")
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	v, err := scanInvite(tx.QueryRow(ctx, `select `+inviteCols+` from pairing_invites where code_hash = $1 for update`,
		hashInvite(req.Invite)))
	if err != nil || v.status(time.Now()) != "issued" {
		writeErr(w, http.StatusUnauthorized, errInvite)
		return
	}

	var deviceID uuid.UUID
	err = tx.QueryRow(ctx, `
		insert into devices (tenant_id, user_id, public_key, name, last_seen_at)
		values ($1, $2, $3, $4, now())
		on conflict (public_key) do nothing returning id`,
		v.TenantID, v.PatientID, pub, name).Scan(&deviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusConflict, "device key already enrolled")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "pairing failed")
		return
	}
	if _, err := tx.Exec(ctx, `update pairing_invites set redeemed_at = now(), device_id = $2 where id = $1`, v.ID, deviceID); err != nil {
		writeErr(w, http.StatusInternalServerError, "pairing failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: v.TenantID, ActorID: &v.PatientID,
		Action: "device.pair", ResourceType: "device", ResourceID: deviceID.String(),
		Detail: mustJSON(map[string]string{"invite_id": v.ID.String(), "name": name}),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	var practice, patientName string
	if err := tx.QueryRow(ctx, `select t.name, u.display_name from users u join tenants t on t.id = u.tenant_id where u.id = $1`,
		v.PatientID).Scan(&practice, &patientName); err != nil {
		writeErr(w, http.StatusInternalServerError, "pairing failed")
		return
	}
	tokens, err := s.issueTokens(ctx, tx, v.PatientID, v.TenantID, "patient", &deviceID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "token failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, redeemResponse{Practice: practice, PatientName: patientName,
		enrollResponse: enrollResponse{DeviceID: deviceID.String(), tokenResponse: tokens}})
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
