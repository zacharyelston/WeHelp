// Package audit writes tamper-evident, hash-chained audit events. Each event
// stores the SHA-256 of the previous event for the same tenant plus its own
// content, so deletion or reordering breaks the chain. The table is
// append-only — enforced by a database trigger.
//
// Hash version 2 (#28): every field is length-prefixed, so bytes can't move
// between fields, and the detail is hashed as Postgres stores it
// (jsonb::text), so Verify reads back exactly the bytes that were hashed.
// Version 1 (before #28) hashed the caller's detail bytes, which jsonb
// rewrites, so those events can only be checked by their links.
//
// The chain proves nothing on its own against someone who can rewrite the
// whole table and recompute every hash; anchoring the head outside the Back
// Office is the audit export (#56).
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// HashVersion is what Record writes.
const HashVersion = 2

// Event is one auditable action. ActorID may be nil for system actions.
type Event struct {
	TenantID     uuid.UUID
	ActorID      *uuid.UUID
	Action       string // e.g. "message.create", "ledger.post"
	ResourceType string // e.g. "message", "ledger_transaction"
	ResourceID   string
	Detail       json.RawMessage
}

func writeField(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	h.Write(n[:])
	h.Write(b)
}

// hashV2 covers the previous hash and every field, each length-prefixed.
// No actor is an empty field; a present one is its 16 bytes.
func hashV2(prev []byte, tenant uuid.UUID, actor *uuid.UUID, action, rtype, rid, detail string) []byte {
	h := sha256.New()
	writeField(h, []byte("wehelp-audit-v2"))
	writeField(h, prev)
	writeField(h, tenant[:])
	if actor != nil {
		writeField(h, actor[:])
	} else {
		writeField(h, nil)
	}
	writeField(h, []byte(action))
	writeField(h, []byte(rtype))
	writeField(h, []byte(rid))
	writeField(h, []byte(detail))
	return h.Sum(nil)
}

// hashV1 is the pre-#28 hash, for events whose detail jsonb didn't rewrite.
func hashV1(prev []byte, tenant uuid.UUID, actor *uuid.UUID, action, rtype, rid string, detail []byte) []byte {
	h := sha256.New()
	h.Write(prev)
	h.Write(tenant[:])
	if actor != nil {
		h.Write(actor[:])
	}
	h.Write([]byte(action))
	h.Write([]byte(rtype))
	h.Write([]byte(rid))
	h.Write(detail)
	return h.Sum(nil)
}

// Record appends an event to the tenant's hash chain inside the caller's
// transaction. A per-tenant advisory lock serializes writers, including the
// very first event, when there is no chain head yet to lock.
func Record(ctx context.Context, tx pgx.Tx, e Event) error {
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('wehelp-audit:' || $1::text, 0))`,
		e.TenantID); err != nil {
		return fmt.Errorf("audit: lock chain: %w", err)
	}
	prev := []byte{} // genesis event links to an empty bytea, not NULL
	err := tx.QueryRow(ctx, `select hash from audit_events where tenant_id = $1 order by seq desc limit 1`,
		e.TenantID).Scan(&prev)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("audit: read chain head: %w", err)
	}

	raw := e.Detail
	if raw == nil {
		raw = json.RawMessage(`{}`)
	}
	// Hash the detail exactly as jsonb will store and return it.
	var detail string
	if err := tx.QueryRow(ctx, `select $1::jsonb::text`, string(raw)).Scan(&detail); err != nil {
		return fmt.Errorf("audit: normalize detail: %w", err)
	}
	sum := hashV2(prev, e.TenantID, e.ActorID, e.Action, e.ResourceType, e.ResourceID, detail)

	_, err = tx.Exec(ctx, `
		insert into audit_events
			(tenant_id, actor_id, action, resource_type, resource_id, detail, prev_hash, hash, hash_version)
		values ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9)`,
		e.TenantID, e.ActorID, e.Action, e.ResourceType, e.ResourceID, detail, prev, sum, HashVersion,
	)
	if err != nil {
		return fmt.Errorf("audit: insert: %w", err)
	}
	return nil
}

// Querier is a pool or a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Result is what Verify found.
type Result struct {
	Events int64 // events checked
	// BrokenAt is the sequence number of the first event whose link or
	// content doesn't match, or 0 if the chain is intact.
	BrokenAt int64
	// LinkOnly counts version-1 events whose content can't be recomputed
	// (their detail was rewritten by jsonb); only their links were checked.
	LinkOnly int64
	// Forked counts version-1 events that link to an earlier event than the
	// one before them. Before #28 the chain head lock let two events written
	// at once link to the same parent. A link to a hash that isn't in the
	// chain at all still breaks it, so deletions are still found.
	Forked int64
}

// Verify recomputes the hash chain for a tenant. Version-1 events (before
// #28) are checked by content where possible and otherwise by link only,
// and may only come before every version-2 event.
func Verify(ctx context.Context, q Querier, tenantID uuid.UUID) (Result, error) {
	var res Result
	rows, err := q.Query(ctx, `
		select seq, actor_id, action, resource_type, resource_id, detail::text, prev_hash, hash, hash_version
		from audit_events where tenant_id = $1 order by seq`, tenantID)
	if err != nil {
		return res, err
	}
	defer rows.Close()

	prev := []byte{}
	seenV2 := false
	earlier := map[string]bool{"": true} // version-1 hashes so far, and genesis
	for rows.Next() {
		var (
			seq                       int64
			actorID                   *uuid.UUID
			action, rtype, rid, dtext string
			prevHash, h               []byte
			version                   int16
		)
		if err := rows.Scan(&seq, &actorID, &action, &rtype, &rid, &dtext, &prevHash, &h, &version); err != nil {
			return res, err
		}
		res.Events++
		switch version {
		case 2:
			seenV2 = true
			if string(prevHash) != string(prev) ||
				string(hashV2(prev, tenantID, actorID, action, rtype, rid, dtext)) != string(h) {
				res.BrokenAt = seq
				return res, nil
			}
		case 1:
			if seenV2 { // versions never go back
				res.BrokenAt = seq
				return res, nil
			}
			if string(prevHash) != string(prev) {
				if !earlier[string(prevHash)] {
					res.BrokenAt = seq
					return res, nil
				}
				res.Forked++
			}
			if string(hashV1(prevHash, tenantID, actorID, action, rtype, rid, []byte(dtext))) != string(h) {
				res.LinkOnly++
			}
			earlier[string(h)] = true
		default:
			res.BrokenAt = seq
			return res, nil
		}
		prev = h
	}
	return res, rows.Err()
}
