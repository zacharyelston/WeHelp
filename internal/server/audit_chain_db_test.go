package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zacharyelston/wehelp/internal/audit"
)

func record(t *testing.T, pool *pgxpool.Pool, e audit.Event) {
	t.Helper()
	ctx := context.Background()
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return audit.Record(ctx, tx, e) }); err != nil {
		t.Fatal(err)
	}
}

// TestAuditChain is #28: a chain with multi-key details that jsonb rewrites
// (key order, spacing, numbers, unicode) verifies intact; so does the seeded
// practice, written by the real handlers; tampering is found; version-1
// events are checked by link only and can't follow version-2 ones; and
// concurrent first events don't fork a new tenant's chain.
func TestAuditChain(t *testing.T) {
	_, pool := testBackOffice(t)
	ctx := context.Background()

	// The seeded demo practice, written through the handlers.
	seeded := demoTenantID(t, pool)
	if res, err := audit.Verify(ctx, pool, seeded); err != nil || res.BrokenAt != 0 || res.Events == 0 || res.LinkOnly != 0 {
		t.Fatalf("seeded chain: %+v %v", res, err)
	}

	// A fresh tenant with details jsonb rewrites.
	var tenant uuid.UUID
	if err := pool.QueryRow(ctx, `insert into tenants (name) values ('Audit test') returning id`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	details := []string{
		`{"zeta": 1, "alpha": "a",   "nested": {"b": [1, 2.50, 1e2], "a": null}}`,
		`{"email":"ada@demo.test","role":"provider","seed":true}`,
		`{"name": "Zoë – café ✓", "n": 12345678901234567890}`,
		``,
	}
	for i, d := range details {
		var raw json.RawMessage
		if d != "" {
			raw = json.RawMessage(d)
		}
		record(t, pool, audit.Event{TenantID: tenant, Action: "test.event", ResourceType: "test",
			ResourceID: fmt.Sprint(i), Detail: raw})
	}
	res, err := audit.Verify(ctx, pool, tenant)
	if err != nil || res.BrokenAt != 0 || res.Events != 4 {
		t.Fatalf("rewritten details: %+v %v", res, err)
	}

	// Tampering with a stored detail is found at that event.
	var target int64
	_ = pool.QueryRow(ctx, `select seq from audit_events where tenant_id = $1 and resource_id = '1'`, tenant).Scan(&target)
	tamper := func(sql string, args ...any) {
		t.Helper()
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `alter table audit_events disable trigger user`); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, sql, args...); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `alter table audit_events enable trigger user`)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	tamper(`update audit_events set detail = '{"email":"eve@demo.test","role":"admin","seed":true}' where seq = $1`, target)
	if res, _ := audit.Verify(ctx, pool, tenant); res.BrokenAt != target {
		t.Errorf("tampered detail: %+v, want broken at %d", res, target)
	}

	// Fields can't be shifted between action and resource type.
	var t2 uuid.UUID
	_ = pool.QueryRow(ctx, `insert into tenants (name) values ('Shift test') returning id`).Scan(&t2)
	record(t, pool, audit.Event{TenantID: t2, Action: "ab", ResourceType: "c"})
	var shifted int64
	_ = pool.QueryRow(ctx, `select seq from audit_events where tenant_id = $1`, t2).Scan(&shifted)
	tamper(`update audit_events set action = 'a', resource_type = 'bc' where seq = $1`, shifted)
	if res, _ := audit.Verify(ctx, pool, t2); res.BrokenAt != shifted {
		t.Errorf("shifted fields: %+v, want broken at %d", res, shifted)
	}

	// Legacy (version 1) events: linked ones verify, one whose detail jsonb
	// rewrote counts as link-only, and none may follow a version-2 event.
	var t3 uuid.UUID
	_ = pool.QueryRow(ctx, `insert into tenants (name) values ('Legacy test') returning id`).Scan(&t3)
	tamper(`insert into audit_events (tenant_id, action, resource_type, resource_id, detail, prev_hash, hash, hash_version)
		values ($1, 'old.a', 'x', '', '{}', '\x'::bytea, $2, 1)`, t3, legacyHash(nil, t3, "old.a", "x", "", "{}"))
	var h1 []byte
	_ = pool.QueryRow(ctx, `select hash from audit_events where tenant_id = $1`, t3).Scan(&h1)
	tamper(`insert into audit_events (tenant_id, action, resource_type, resource_id, detail, prev_hash, hash, hash_version)
		values ($1, 'old.b', 'x', '', '{"b":1,"a":2}', $2, $3, 1)`, t3, h1, legacyHash(h1, t3, "old.b", "x", "", `{"b":1,"a":2}`))
	// The old head lock let two events written at once share a parent: old.b2
	// also links to old.a. Accepted for version 1, and counted.
	tamper(`insert into audit_events (tenant_id, action, resource_type, resource_id, detail, prev_hash, hash, hash_version)
		values ($1, 'old.b2', 'x', '', '{}', $2, $3, 1)`, t3, h1, legacyHash(h1, t3, "old.b2", "x", "", "{}"))
	record(t, pool, audit.Event{TenantID: t3, Action: "new.c", ResourceType: "x", Detail: json.RawMessage(`{"k": "v"}`)})
	if res, err := audit.Verify(ctx, pool, t3); err != nil || res.BrokenAt != 0 || res.Events != 4 || res.LinkOnly != 1 || res.Forked != 1 {
		t.Errorf("legacy (with a fork) then v2: %+v %v", res, err)
	}

	// A version-1 link to a hash that isn't in the chain (a deleted event)
	// still breaks it.
	var t5 uuid.UUID
	_ = pool.QueryRow(ctx, `insert into tenants (name) values ('Deleted test') returning id`).Scan(&t5)
	tamper(`insert into audit_events (tenant_id, action, resource_type, resource_id, detail, prev_hash, hash, hash_version)
		values ($1, 'old.a', 'x', '', '{}', '\x'::bytea, $2, 1)`, t5, legacyHash(nil, t5, "old.a", "x", "", "{}"))
	missing := legacyHash([]byte("gone"), t5, "old.gone", "x", "", "{}")
	tamper(`insert into audit_events (tenant_id, action, resource_type, resource_id, detail, prev_hash, hash, hash_version)
		values ($1, 'old.c', 'x', '', '{}', $2, $3, 1)`, t5, missing, legacyHash(missing, t5, "old.c", "x", "", "{}"))
	if res, _ := audit.Verify(ctx, pool, t5); res.BrokenAt == 0 || res.Events != 2 {
		t.Errorf("legacy link to a missing event: %+v", res)
	}

	// Version-2 events may not fork.
	var t6 uuid.UUID
	_ = pool.QueryRow(ctx, `insert into tenants (name) values ('V2 fork test') returning id`).Scan(&t6)
	record(t, pool, audit.Event{TenantID: t6, Action: "a", ResourceType: "x"})
	record(t, pool, audit.Event{TenantID: t6, Action: "b", ResourceType: "x"})
	var genesisHash []byte
	_ = pool.QueryRow(ctx, `select hash from audit_events where tenant_id = $1 and action = 'a'`, t6).Scan(&genesisHash)
	record(t, pool, audit.Event{TenantID: t6, Action: "c", ResourceType: "x"})
	tamper(`update audit_events set prev_hash = $2 where tenant_id = $1 and action = 'c'`, t6, genesisHash)
	if res, _ := audit.Verify(ctx, pool, t6); res.BrokenAt == 0 {
		t.Error("a version-2 fork passed")
	}
	var head []byte
	_ = pool.QueryRow(ctx, `select hash from audit_events where tenant_id = $1 order by seq desc limit 1`, t3).Scan(&head)
	tamper(`insert into audit_events (tenant_id, action, resource_type, resource_id, detail, prev_hash, hash, hash_version)
		values ($1, 'old.d', 'x', '', '{}', $2, $3, 1)`, t3, head, legacyHash(head, t3, "old.d", "x", "", "{}"))
	if res, _ := audit.Verify(ctx, pool, t3); res.BrokenAt == 0 {
		t.Error("a version-1 event after a version-2 one passed")
	}

	// Concurrent first events for a new tenant don't fork the chain.
	var t4 uuid.UUID
	_ = pool.QueryRow(ctx, `insert into tenants (name) values ('Race test') returning id`).Scan(&t4)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				return audit.Record(ctx, tx, audit.Event{TenantID: t4, Action: "race", ResourceType: "x", ResourceID: fmt.Sprint(i)})
			})
		}(i)
	}
	wg.Wait()
	if res, err := audit.Verify(ctx, pool, t4); err != nil || res.BrokenAt != 0 || res.Events != 8 {
		t.Errorf("concurrent first events: %+v %v", res, err)
	}
}

// legacyHash is the pre-#28 formula over the given detail text.
func legacyHash(prev []byte, tenant uuid.UUID, action, rtype, rid, detail string) []byte {
	h := sha256.New()
	h.Write(prev)
	h.Write(tenant[:])
	h.Write([]byte(action))
	h.Write([]byte(rtype))
	h.Write([]byte(rid))
	h.Write([]byte(detail))
	return h.Sum(nil)
}
