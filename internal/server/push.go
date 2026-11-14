package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/tunnel"
)

// Push (#7). The app registers its APNs token for its device; when something
// happens for a person, the Back Office asks its Relay to notify each of
// their devices with a fixed category. No text or IDs leave the office.

// Pusher sends one push through the Relay (tunnel.Pusher in production).
type Pusher interface {
	Push(ctx context.Context, r tunnel.PushRequest) (string, error)
}

// SetPusher enables push. Without it (no Relay tunnel) pushes are skipped.
func (s *Server) SetPusher(p Pusher) { s.pusher = p }

type pushTokenRequest struct {
	Token       string `json:"token"`
	Environment string `json:"environment"`
}

// SetPushToken stores this device's APNs token (device-bound sessions only).
func (s *Server) SetPushToken(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	if id.DeviceID == nil {
		writeErr(w, http.StatusForbidden, "only a paired device can register for push")
		return
	}
	var req pushTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Token = strings.ToLower(strings.TrimSpace(req.Token))
	if b, err := hex.DecodeString(req.Token); err != nil || len(b) < 32 || len(b) > 100 {
		writeErr(w, http.StatusBadRequest, "token must be a hex APNs device token")
		return
	}
	if req.Environment != "sandbox" && req.Environment != "production" {
		writeErr(w, http.StatusBadRequest, "environment must be sandbox or production")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `update devices set push_token = $2, push_env = $3 where id = $1 and user_id = $4`,
		id.DeviceID, req.Token, req.Environment, id.UserID); err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ClearPushToken forgets this device's token (the user turned notifications
// off).
func (s *Server) ClearPushToken(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	if id.DeviceID == nil {
		writeErr(w, http.StatusForbidden, "only a paired device has a push token")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `update devices set push_token = null, push_env = null where id = $1 and user_id = $2`,
		id.DeviceID, id.UserID); err != nil {
		writeErr(w, http.StatusInternalServerError, "clear failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// notify pushes a category to every active device of a user, in the
// background. Tokens Apple rejects are cleared. Logs carry device IDs only.
func (s *Server) notify(userID uuid.UUID, category string) {
	if s.pusher == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		rows, err := s.pool.Query(ctx, `
			select id, push_token, push_env from devices
			where user_id = $1 and revoked_at is null and push_token is not null`, userID)
		if err != nil {
			slog.Warn("push: device lookup failed", "err", err)
			return
		}
		type dev struct {
			ID         uuid.UUID
			Token, Env string
		}
		devs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (dev, error) {
			var d dev
			err := row.Scan(&d.ID, &d.Token, &d.Env)
			return d, err
		})
		if err != nil {
			return
		}
		for _, d := range devs {
			result, err := s.pusher.Push(ctx, tunnel.PushRequest{Token: d.Token, Environment: d.Env, Category: category})
			switch {
			case err != nil:
				slog.Warn("push: not sent", "device", d.ID, "category", category, "err", err)
			case result == tunnel.PushInvalidToken:
				_, _ = s.pool.Exec(ctx, `update devices set push_token = null, push_env = null where id = $1`, d.ID)
				slog.Info("push: token no longer valid; cleared", "device", d.ID)
			default:
				slog.Info("push", "device", d.ID, "category", category, "result", result)
			}
		}
	}()
}
