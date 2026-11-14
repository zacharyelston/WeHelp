package registry

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"

	"github.com/jackc/pgx/v5"
)

// The Registry keeps one hash-chained audit log. Each event's hash covers
// the previous hash and every field, each length-prefixed so field
// boundaries can't be shifted between fields. The detail is hashed in the
// form Postgres stores it (jsonb::text), so verification sees the same bytes.

// Lock keys for pg_advisory_xact_lock. Callers that need both take
// lockPublish before lockAudit.
const (
	lockPublish int64 = 0x5745_4845_4c50_0001 // "WEHELP" + 1
	lockAudit   int64 = 0x5745_4845_4c50_0002
)

// Event is one auditable Registry action.
type Event struct {
	Actor        string // "cli:<user>", "install-token:<id>", "system"
	Action       string // e.g. "practice.create"
	ResourceType string
	ResourceID   string
	Detail       any // marshalled to JSON; nil means {}
}

func writeField(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	h.Write(n[:])
	h.Write(b)
}

func eventHash(prev []byte, actor, action, rtype, rid, detail string) []byte {
	h := sha256.New()
	writeField(h, prev)
	writeField(h, []byte(actor))
	writeField(h, []byte(action))
	writeField(h, []byte(rtype))
	writeField(h, []byte(rid))
	writeField(h, []byte(detail))
	return h.Sum(nil)
}

// record appends e to the chain inside the caller's transaction.
func record(ctx context.Context, tx pgx.Tx, e Event) error {
	if e.Actor == "" || e.Action == "" || e.ResourceType == "" {
		return errors.New("audit: actor, action and resource type are required")
	}
	raw := []byte(`{}`)
	if e.Detail != nil {
		var err error
		if raw, err = json.Marshal(e.Detail); err != nil {
			return fmt.Errorf("audit: detail: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1)`, lockAudit); err != nil {
		return fmt.Errorf("audit: lock: %w", err)
	}
	var detail string
	if err := tx.QueryRow(ctx, `select $1::jsonb::text`, string(raw)).Scan(&detail); err != nil {
		return fmt.Errorf("audit: normalize detail: %w", err)
	}
	prev := []byte{}
	err := tx.QueryRow(ctx, `select hash from audit_events order by seq desc limit 1`).Scan(&prev)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("audit: read chain head: %w", err)
	}
	sum := eventHash(prev, e.Actor, e.Action, e.ResourceType, e.ResourceID, detail)
	_, err = tx.Exec(ctx, `
		insert into audit_events (actor, action, resource_type, resource_id, detail, prev_hash, hash)
		values ($1, $2, $3, $4, $5::jsonb, $6, $7)`,
		e.Actor, e.Action, e.ResourceType, e.ResourceID, detail, prev, sum)
	if err != nil {
		return fmt.Errorf("audit: insert: %w", err)
	}
	return nil
}

// VerifyAudit recomputes the chain. It returns the sequence number of the
// first broken event, or 0 when the chain is intact, and the event count.
func (s *Store) VerifyAudit(ctx context.Context) (broken, count int64, err error) {
	rows, err := s.pool.Query(ctx, `
		select seq, actor, action, resource_type, resource_id, detail::text, prev_hash, hash
		from audit_events order by seq`)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	prev := []byte{}
	for rows.Next() {
		var (
			seq                               int64
			actor, action, rtype, rid, detail string
			prevHash, h                       []byte
		)
		if err := rows.Scan(&seq, &actor, &action, &rtype, &rid, &detail, &prevHash, &h); err != nil {
			return 0, count, err
		}
		count++
		if string(prevHash) != string(prev) || string(eventHash(prev, actor, action, rtype, rid, detail)) != string(h) {
			return seq, count, nil
		}
		prev = h
	}
	return 0, count, rows.Err()
}
