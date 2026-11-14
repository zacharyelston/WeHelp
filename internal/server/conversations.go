package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
)

// Conversations and threads (#67). A conversation is the caller and one
// person they are linked with (a provider and a patient). Threads show both
// directions; reading one is audited.

type conversationView struct {
	UserID      uuid.UUID  `json:"user_id"`
	Name        string     `json:"name"`
	Role        string     `json:"role"`
	LinkStatus  string     `json:"link_status"`
	LastBody    *string    `json:"last_body,omitempty"`
	LastAt      *time.Time `json:"last_at,omitempty"`
	LastFromMe  bool       `json:"last_from_me"`
	UnreadCount int        `json:"unread_count"`
}

// ListConversations lists everyone the caller is linked with (pending or
// active), with the latest message either way and the unread count.
// Conversations with messages come first, newest first.
func (s *Server) ListConversations(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `
		with links as (
			select case when pp.provider_id = $2 then pp.patient_id else pp.provider_id end as other, pp.status
			from provider_patients pp
			where pp.tenant_id = $1 and pp.status <> 'revoked' and (pp.provider_id = $2 or pp.patient_id = $2)
		)
		select l.other, u.display_name, u.role::text, l.status, last.body, last.created_at, coalesce(last.sender_id = $2, false),
		       (select count(*) from messages m
		        where m.tenant_id = $1 and m.recipient_id = $2 and m.sender_id = l.other and m.read_at is null)
		from links l
		join users u on u.id = l.other
		left join lateral (
			select m.body, m.created_at, m.sender_id from messages m
			where m.tenant_id = $1
			  and ((m.sender_id = $2 and m.recipient_id = l.other) or (m.sender_id = l.other and m.recipient_id = $2))
			order by m.created_at desc, m.id desc limit 1
		) last on true
		order by last.created_at desc nulls last, u.display_name`, id.TenantID, id.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (conversationView, error) {
		var c conversationView
		err := row.Scan(&c.UserID, &c.Name, &c.Role, &c.LinkStatus, &c.LastBody, &c.LastAt, &c.LastFromMe, &c.UnreadCount)
		return c, err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "scan failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": list})
}

// linkedAtAll reports whether two users have a link that isn't revoked.
func linkedAtAll(ctx context.Context, tx pgx.Tx, tenantID, a, b uuid.UUID) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `
		select exists(select 1 from provider_patients
			where tenant_id = $1 and status <> 'revoked'
			  and ((provider_id = $2 and patient_id = $3) or (provider_id = $3 and patient_id = $2)))`,
		tenantID, a, b).Scan(&ok)
	return ok, err
}

// listThread serves GET /messages?with=<user>: both directions between the
// caller and one linked user, newest first, keyset-paginated, audited.
func (s *Server) listThread(w http.ResponseWriter, r *http.Request, with uuid.UUID, limit int, before time.Time, beforeID uuid.UUID) {
	id, _ := identityFrom(r.Context())
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	ok, err := linkedAtAll(ctx, tx, id.TenantID, id.UserID, with)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "link check failed")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no conversation with that user")
		return
	}
	q := `
		select id, sender_id, recipient_id, kind, body, metadata, read_at, created_at
		from messages
		where tenant_id = $1
		  and ((sender_id = $2 and recipient_id = $3) or (sender_id = $3 and recipient_id = $2))`
	args := []any{id.TenantID, id.UserID, with}
	if !before.IsZero() {
		q += ` and (created_at, id) < ($4, $5)`
		args = append(args, before, beforeID)
	}
	q += fmt.Sprintf(` order by created_at desc, id desc limit $%d`, len(args)+1)
	args = append(args, limit+1)
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	msgs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (messageView, error) {
		var m messageView
		err := row.Scan(&m.ID, &m.SenderID, &m.RecipientID, &m.Kind, &m.Body, &m.Metadata, &m.ReadAt, &m.CreatedAt)
		return m, err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "scan failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: id.TenantID, ActorID: &id.UserID,
		Action: "messages.read_thread", ResourceType: "user", ResourceID: with.String(),
		Detail: mustJSON(map[string]int{"returned": min(len(msgs), limit)}),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	resp := map[string]any{}
	if len(msgs) > limit {
		last := msgs[limit-1]
		resp["next_cursor"] = encodeCursor(last.CreatedAt, last.ID)
		msgs = msgs[:limit]
	}
	resp["messages"] = msgs
	writeJSON(w, http.StatusOK, resp)
}
