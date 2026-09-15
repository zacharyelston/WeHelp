// Package audit writes tamper-evident, hash-chained audit events. Each event
// stores the SHA-256 of the previous event for the same tenant plus its own
// content, so deletion or reordering breaks the chain. The table is
// append-only — enforced by a database trigger.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Event is one auditable action. ActorID may be nil for system actions.
type Event struct {
	TenantID     uuid.UUID
	ActorID      *uuid.UUID
	Action       string // e.g. "message.create", "ledger.post"
	ResourceType string // e.g. "message", "ledger_transaction"
	ResourceID   string
	Detail       json.RawMessage
}

// Record appends an event to the tenant's hash chain inside the caller's
// transaction, locking the chain head to serialize concurrent writers.
func Record(ctx context.Context, tx pgx.Tx, e Event) error {
	var prev []byte
	err := tx.QueryRow(ctx,
		`select hash from audit_events where tenant_id = $1 order by seq desc limit 1 for update`,
		e.TenantID,
	).Scan(&prev)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("audit: read chain head: %w", err)
	}
	if prev == nil {
		prev = []byte{} // genesis event links to an empty bytea, not NULL
	}

	detail := e.Detail
	if detail == nil {
		detail = json.RawMessage(`{}`)
	}

	h := sha256.New()
	h.Write(prev)
	h.Write(e.TenantID[:])
	if e.ActorID != nil {
		h.Write(e.ActorID[:])
	}
	h.Write([]byte(e.Action))
	h.Write([]byte(e.ResourceType))
	h.Write([]byte(e.ResourceID))
	h.Write(detail)
	sum := h.Sum(nil)

	_, err = tx.Exec(ctx, `
		insert into audit_events
			(tenant_id, actor_id, action, resource_type, resource_id, detail, prev_hash, hash)
		values ($1, $2, $3, $4, $5, $6, $7, $8)`,
		e.TenantID, e.ActorID, e.Action, e.ResourceType, e.ResourceID, detail, prev, sum,
	)
	if err != nil {
		return fmt.Errorf("audit: insert: %w", err)
	}
	return nil
}

// Verify recomputes the hash chain for a tenant and reports the sequence
// number of the first broken link, or 0 if the chain is intact.
func Verify(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (int64, error) {
	rows, err := tx.Query(ctx, `
		select seq, actor_id, action, resource_type, resource_id, detail, prev_hash, hash
		from audit_events where tenant_id = $1 order by seq`, tenantID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var prev []byte
	for rows.Next() {
		var (
			seq      int64
			actorID  *uuid.UUID
			action   string
			rtype    string
			rid      string
			detail   []byte
			prevHash []byte
			hash     []byte
		)
		if err := rows.Scan(&seq, &actorID, &action, &rtype, &rid, &detail, &prevHash, &hash); err != nil {
			return 0, err
		}
		if string(prevHash) != string(prev) {
			return seq, nil
		}
		h := sha256.New()
		h.Write(prev)
		h.Write(tenantID[:])
		if actorID != nil {
			h.Write(actorID[:])
		}
		h.Write([]byte(action))
		h.Write([]byte(rtype))
		h.Write([]byte(rid))
		h.Write(detail)
		if string(h.Sum(nil)) != string(hash) {
			return seq, nil
		}
		prev = hash
	}
	return 0, rows.Err()
}
