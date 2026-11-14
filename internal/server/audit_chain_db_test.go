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
	record(t, pool, audit.Event{TenantID: t3, Action: "new.c", ResourceType: "x", Detail: json.RawMessage(`{"k": "v"}`)})
	if res, err := audit.Verify(ctx, pool, t3); err != nil || res.BrokenAt != 0 || res.Events != 3 || res.LinkOnly != 1 {
		t.Errorf("legacy then v2: %+v %v", res, err)
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
