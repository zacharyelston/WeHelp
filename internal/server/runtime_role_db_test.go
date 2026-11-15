package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
)

// TestRuntimeRole is #161: the server's database role (wehelp_app) can read
// and write data and append audit events, but can't change the schema, and
// can't UPDATE or DELETE the append-only tables even with their triggers off.
func TestRuntimeRole(t *testing.T) {
	_, pool := testBackOffice(t)
	ctx := context.Background()
	tenant := demoTenantID(t, pool)

	asApp := func(fn func(tx pgx.Tx) error) error {
		return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `set local role wehelp_app`); err != nil {
				return err
			}
			return fn(tx)
		})
	}

	// Ordinary work: read, write, and append to the audit chain.
	if err := asApp(func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from users`).Scan(&n); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update tenants set name = name where id = $1`, tenant); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{TenantID: tenant, Action: "test.runtime_role", ResourceType: "test",
			Detail: json.RawMessage(`{}`)})
	}); err != nil {
		t.Fatalf("runtime role can't do ordinary work: %v", err)
	}

	// Refused: schema changes, and changing audit or ledger rows.
	for name, sql := range map[string]string{
		"create table":        `create table runtime_role_probe (id int)`,
		"alter table":         `alter table users add column probe int`,
		"disable trigger":     `alter table audit_events disable trigger user`,
		"update audit_events": `update audit_events set action = action`,
		"delete audit_events": `delete from audit_events where false`,
		"delete ledger":       `delete from ledger_entries where false`,
		"truncate audit":      `truncate audit_events`,
	} {
		if err := asApp(func(tx pgx.Tx) error { _, err := tx.Exec(ctx, sql); return err }); err == nil {
			t.Errorf("runtime role could %s", name)
		}
	}
}
