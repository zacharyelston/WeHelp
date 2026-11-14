package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/officelist"
	"github.com/zacharyelston/wehelp/internal/roster"
	"golang.org/x/crypto/ssh"
)

// Provider roster (#62): an installed office asks for its practice's
// providers, signing the request with its office SSH key. The answer is
// signed with the Registry key the office pinned at install, and holds only
// that practice's providers, removed ones included so the Back Office can
// flag their accounts. Accounts and credentials stay in the Back Office.

// Roster verifies a signed request and returns the signed roster for the
// office that made it.
func (s *Store) Roster(ctx context.Context, body []byte, sig *ssh.Signature) (officelist.Envelope, error) {
	var req roster.Request
	if err := json.Unmarshal(body, &req); err != nil {
		return officelist.Envelope{}, fmt.Errorf("%w: request: %v", ErrInvalidInput, err)
	}
	officeID, err := uuid.Parse(req.OfficeID)
	if err != nil {
		return officelist.Envelope{}, fmt.Errorf("%w: office_id", ErrInvalidInput)
	}
	var key, status string
	var practiceID uuid.UUID
	err = s.pool.QueryRow(ctx, `
		select o.ssh_public_key, o.practice_id, p.status from offices o join practices p on p.id = o.practice_id
		where o.id = $1`, officeID).Scan(&key, &practiceID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return officelist.Envelope{}, ErrInvalidToken
	}
	if err != nil {
		return officelist.Envelope{}, err
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil || sig == nil || pub.Verify(body, sig) != nil {
		return officelist.Envelope{}, ErrInvalidToken
	}
	now := s.now()
	if d := now.Sub(time.Unix(req.At, 0)); d > roster.MaxSkew || d < -roster.MaxSkew {
		return officelist.Envelope{}, fmt.Errorf("%w: request time is off by %s", ErrInvalidInput, d.Round(time.Second))
	}
	if status != "active" {
		return officelist.Envelope{}, ErrNotActive
	}
	rows, err := s.pool.Query(ctx, `
		select id::text, name, email::text, role, credentials, licence_number, removed_at
		from practice_providers where practice_id = $1 order by role, name, created_at`, practiceID)
	if err != nil {
		return officelist.Envelope{}, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (roster.Provider, error) {
		var p roster.Provider
		err := row.Scan(&p.ID, &p.Name, &p.Email, &p.Role, &p.Credentials, &p.LicenceNumber, &p.RemovedAt)
		return p, err
	})
	if err != nil {
		return officelist.Envelope{}, err
	}
	return roster.Sign(s.key, roster.Roster{OfficeID: officeID.String(), IssuedAt: now.UTC(), Providers: list})
}

func (s *Server) roster(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(r.Header.Get(roster.SignatureHeader))
	sig := new(ssh.Signature)
	if err != nil || ssh.Unmarshal(raw, sig) != nil {
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}
	env, err := s.store.Roster(r.Context(), body, sig)
	switch {
	case err == nil:
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, env)
	case errors.Is(err, ErrInvalidToken):
		writeError(w, http.StatusUnauthorized, "invalid signature")
	case errors.Is(err, ErrNotActive):
		writeError(w, http.StatusForbidden, "practice is suspended")
	case errors.Is(err, ErrInvalidInput):
		writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "registry: invalid input: "))
	default:
		slog.Error("roster", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
