package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
)

var messageKinds = map[string]bool{
	"message":  true,
	"memo":     true,
	"checkin":  true,
	"reminder": true,
}

type messageView struct {
	ID          uuid.UUID       `json:"id"`
	SenderID    uuid.UUID       `json:"sender_id"`
	RecipientID uuid.UUID       `json:"recipient_id"`
	Kind        string          `json:"kind"`
	Body        string          `json:"body"`
	Metadata    json.RawMessage `json:"metadata"`
	ReadAt      *time.Time      `json:"read_at"`
	CreatedAt   time.Time       `json:"created_at"`
}

type createMessageRequest struct {
	RecipientID uuid.UUID       `json:"recipient_id"`
	Kind        string          `json:"kind"`
	Body        string          `json:"body"`
	Metadata    json.RawMessage `json:"metadata"`
}

// CreateMessage sends a message to a user the caller has an active link with.
// Route: POST /api/v1/messages
func (s *Server) CreateMessage(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())

	var req createMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Kind == "" {
		req.Kind = "message"
	}
	if !messageKinds[req.Kind] {
		writeErr(w, http.StatusBadRequest, "kind must be message, memo, checkin, or reminder")
		return
	}
	req.Body = strings.TrimSpace(req.Body)
	if req.Body == "" {
		writeErr(w, http.StatusBadRequest, "body required")
		return
	}
	if req.RecipientID == id.UserID {
		writeErr(w, http.StatusBadRequest, "cannot message yourself")
		return
	}
	if len(req.Metadata) > 0 {
		var m map[string]any
		if err := json.Unmarshal(req.Metadata, &m); err != nil {
			writeErr(w, http.StatusBadRequest, "metadata must be a JSON object")
			return
		}
	} else {
		req.Metadata = json.RawMessage(`{}`)
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)

	linked, err := HasActiveLink(ctx, tx, id.TenantID, id.UserID, req.RecipientID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "link check failed")
		return
	}
	if !linked {
		writeErr(w, http.StatusForbidden, "no active link with that user")
		return
	}

	var msgID uuid.UUID
	err = tx.QueryRow(ctx, `
		insert into messages (tenant_id, sender_id, recipient_id, kind, body, metadata)
		values ($1, $2, $3, $4, $5, $6) returning id`,
		id.TenantID, id.UserID, req.RecipientID, req.Kind, req.Body, req.Metadata).
		Scan(&msgID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "insert failed")
		return
	}

	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: id.TenantID, ActorID: &id.UserID,
		Action: "message.create", ResourceType: "message", ResourceID: msgID.String(),
		Detail: json.RawMessage(`{"kind":"` + req.Kind + `","recipient":"` + req.RecipientID.String() + `"}`),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": msgID, "kind": req.Kind})
}

// ListMessages returns the caller's inbox, newest first, keyset-paginated via
// an opaque cursor. ?unread=true filters to unread; ?limit= (default 50,
// max 200); ?cursor= continues a previous page.
// Route: GET /api/v1/messages
func (s *Server) ListMessages(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	q := r.URL.Query()

	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeErr(w, http.StatusBadRequest, "limit must be 1-200")
			return
		}
		limit = n
	}
	unreadOnly := q.Get("unread") == "true"

	var before time.Time
	var beforeID uuid.UUID
	if c := q.Get("cursor"); c != "" {
		var err error
		if before, beforeID, err = decodeCursor(c); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid cursor")
			return
		}
	}

	// Keyset pagination on (created_at, id), newest first.
	var rows pgx.Rows
	var err error
	base := `
		select id, sender_id, recipient_id, kind, body, metadata, read_at, created_at
		from messages
		where tenant_id = $1 and recipient_id = $2`
	args := []any{id.TenantID, id.UserID}
	if unreadOnly {
		base += ` and read_at is null`
	}
	if !before.IsZero() {
		base += ` and (created_at, id) < ($3, $4)`
		args = append(args, before, beforeID)
	}
	base += ` order by created_at desc, id desc limit ` + strconv.Itoa(limit+1)

	rows, err = s.pool.Query(r.Context(), base, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	msgs := []messageView{}
	for rows.Next() {
		var m messageView
		if err := rows.Scan(&m.ID, &m.SenderID, &m.RecipientID, &m.Kind, &m.Body, &m.Metadata, &m.ReadAt, &m.CreatedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, "scan failed")
			return
		}
		msgs = append(msgs, m)
	}

	resp := map[string]any{"messages": msgs}
	if len(msgs) > limit {
		last := msgs[limit-1]
		msgs = msgs[:limit]
		resp["next_cursor"] = encodeCursor(last.CreatedAt, last.ID)
	}
	resp["messages"] = msgs
	writeJSON(w, http.StatusOK, resp)
}

// MarkRead sets read_at on a message addressed to the caller. Idempotent.
// Route: POST /api/v1/messages/{messageID}/read
func (s *Server) MarkRead(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	msgID, err := uuid.Parse(chi.URLParam(r, "messageID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid message id")
		return
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		update messages set read_at = now()
		where id = $1 and tenant_id = $2 and recipient_id = $3 and read_at is null`,
		msgID, id.TenantID, id.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		// Either not addressed to the caller or already read — distinguish.
		var exists bool
		err := tx.QueryRow(ctx,
			`select exists(select 1 from messages where id = $1 and tenant_id = $2 and recipient_id = $3)`,
			msgID, id.TenantID, id.UserID).Scan(&exists)
		if err != nil || !exists {
			writeErr(w, http.StatusNotFound, "message not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "already read"})
		return
	}

	// read_at is a write on a PHI row — audit it.
	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: id.TenantID, ActorID: &id.UserID,
		Action: "message.read", ResourceType: "message", ResourceID: msgID.String(),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "read"})
}

// Cursor = base64("RFC3339Nano|uuid") — opaque to clients.
func encodeCursor(ts time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(ts.Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(c string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	ts, idStr, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, uuid.Nil, errors.New("malformed cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("bad cursor timestamp: %w", err)
	}
	uid, err := uuid.Parse(idStr)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("bad cursor id: %w", err)
	}
	return t, uid, nil
}
