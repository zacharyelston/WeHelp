// Package seed creates a demo tenant, provider, two patients, a provider
// link, messages, and an appointment so devs and agents can exercise the
// API immediately. It reuses the service-layer helpers (auth.HashPassword,
// audit.Record) and mirrors the validated SQL in internal/server rather
// than reaching around it.
//
// Seed is idempotent: every entity is created with a check-then-insert, so
// re-running against an already-seeded database is a no-op that appends no
// duplicate rows and no duplicate audit events. Credentials for the demo
// accounts are printed to stdout.
package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/auth"
)

// Demo credentials. Passwords meet the 12-character minimum enforced by the
// register endpoint. They are intentionally documented constants — the seed
// is a dev/demo tool, never production.
const (
	DemoTenant = "Demo Clinic"

	ProviderEmail    = "dr.ada.shaw@demo.wehelp"
	ProviderPassword = "wehelp-demo-provider"
	ProviderName     = "Dr. Ada Shaw"

	Patient1Email    = "jordan.lee@demo.wehelp"
	Patient1Password = "wehelp-demo-patient"
	Patient1Name     = "Jordan Lee"

	Patient2Email    = "sam.rivera@demo.wehelp"
	Patient2Password = "wehelp-demo-patient"
	Patient2Name     = "Sam Rivera"
)

// Result captures the IDs and creation state of a seed run, for callers that
// want to drive the API programmatically after seeding.
type Result struct {
	TenantID  uuid.UUID
	Provider  Account
	Patient1  Account
	Patient2  Account
	Link1     string // status of the provider<->patient1 link
	Link2     string // status of the provider<->patient2 link
	Messages  int
	Appointed bool
}

// Account is one seeded user plus whether it was newly created this run.
type Account struct {
	ID      uuid.UUID
	Email   string
	Role    string
	Created bool
}

// Run seeds the demo dataset inside a single transaction. Every PHI-touching
// write is accompanied by an audit.Record in the same transaction, matching
// the architecture invariant. Idempotent: a second run against the same data
// performs no writes and appends no audit events.
func Run(ctx context.Context, pool *pgxpool.Pool, out io.Writer) (*Result, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("seed: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// Tenant: insert-or-get by name (unique index from migration 00002).
	var tenantID uuid.UUID
	err = tx.QueryRow(ctx,
		`insert into tenants (name) values ($1)
		 on conflict (name) do update set name = excluded.name
		 returning id`, DemoTenant).Scan(&tenantID)
	if err != nil {
		return nil, fmt.Errorf("seed: tenant: %w", err)
	}

	provider, err := ensureUser(ctx, tx, tenantID, ProviderEmail, ProviderPassword, "provider", ProviderName)
	if err != nil {
		return nil, err
	}
	patient1, err := ensureUser(ctx, tx, tenantID, Patient1Email, Patient1Password, "patient", Patient1Name)
	if err != nil {
		return nil, err
	}
	patient2, err := ensureUser(ctx, tx, tenantID, Patient2Email, Patient2Password, "patient", Patient2Name)
	if err != nil {
		return nil, err
	}

	// Links: provider invites both patients. Patient1 accepts (active);
	// patient2 stays pending. provider_patients is mutable, so an existing
	// pending link can be promoted to active idempotently.
	link1, err := ensureLink(ctx, tx, tenantID, provider.ID, patient1.ID, "active", "link.accept")
	if err != nil {
		return nil, err
	}
	link2, err := ensureLink(ctx, tx, tenantID, provider.ID, patient2.ID, "pending", "")
	if err != nil {
		return nil, err
	}

	// Messages: a short thread between provider and patient1. Guarded by
	// (sender, recipient, body, kind) so re-runs don't duplicate.
	msgs, err := ensureMessages(ctx, tx, tenantID, provider.ID, patient1.ID)
	if err != nil {
		return nil, err
	}

	// Appointment: provider meets patient1, ~2 days out. Guarded by the
	// existence of any appointment between the pair, so the timestamp can
	// move with "now" without creating duplicates across runs.
	appointed, err := ensureAppointment(ctx, tx, tenantID, provider.ID, patient1.ID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("seed: commit: %w", err)
	}

	res := &Result{
		TenantID:  tenantID,
		Provider:  *provider,
		Patient1:  *patient1,
		Patient2:  *patient2,
		Link1:     link1,
		Link2:     link2,
		Messages:  msgs,
		Appointed: appointed,
	}
	printCredentials(out, res)
	return res, nil
}

// ensureUser inserts a user if absent and records an audit event for the
// creation. Existing users are returned unchanged (no audit event), keeping
// the seed idempotent and the audit chain free of duplicate seed events.
func ensureUser(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, email, password, role, name string) (*Account, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx,
		`select id from users where tenant_id = $1 and email = $2`, tenantID, email).Scan(&id)
	if err == nil {
		return &Account{ID: id, Email: email, Role: role, Created: false}, nil
	}
	if !isNoRows(err) {
		return nil, fmt.Errorf("seed: lookup user %s: %w", email, err)
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("seed: hash password: %w", err)
	}
	err = tx.QueryRow(ctx,
		`insert into users (tenant_id, email, password_hash, role, display_name)
		 values ($1, $2, $3, $4, $5) returning id`,
		tenantID, email, hash, role, name).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("seed: insert user %s: %w", email, err)
	}

	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: tenantID, ActorID: &id,
		Action: "auth.register", ResourceType: "user", ResourceID: id.String(),
		Detail: json.RawMessage(`{"email":"` + email + `","role":"` + role + `","seed":true}`),
	}); err != nil {
		return nil, fmt.Errorf("seed: audit user %s: %w", email, err)
	}
	slog.Info("seed: created user", "email", email, "role", role)
	return &Account{ID: id, Email: email, Role: role, Created: true}, nil
}

// ensureLink creates a provider->patient link if absent, then promotes it to
// the desired status when possible. The acceptAction (if non-empty) is
// audited only when a pending link is actually promoted to active.
func ensureLink(ctx context.Context, tx pgx.Tx, tenantID, providerID, patientID uuid.UUID, wantStatus, acceptAction string) (string, error) {
	var status string
	err := tx.QueryRow(ctx,
		`insert into provider_patients (tenant_id, provider_id, patient_id)
		 values ($1, $2, $3)
		 on conflict (provider_id, patient_id) do update set provider_id = excluded.provider_id
		 returning status`, tenantID, providerID, patientID).Scan(&status)
	if err != nil {
		return "", fmt.Errorf("seed: upsert link: %w", err)
	}

	if wantStatus == "active" && status != "active" {
		tag, err := tx.Exec(ctx,
			`update provider_patients set status = 'active'
			 where tenant_id = $1 and provider_id = $2 and patient_id = $3
			   and status <> 'revoked'`,
			tenantID, providerID, patientID)
		if err != nil {
			return "", fmt.Errorf("seed: activate link: %w", err)
		}
		if tag.RowsAffected() > 0 {
			status = "active"
			if acceptAction != "" {
				if err := audit.Record(ctx, tx, audit.Event{
					TenantID: tenantID, ActorID: &patientID,
					Action: acceptAction, ResourceType: "provider_patient",
					ResourceID: providerID.String() + ":" + patientID.String(),
				}); err != nil {
					return "", fmt.Errorf("seed: audit link accept: %w", err)
				}
			}
		}
	}
	return status, nil
}

// ensureMessages seeds a short provider<->patient thread. Each message is
// guarded by (sender, recipient, body, kind) so re-runs append nothing.
func ensureMessages(ctx context.Context, tx pgx.Tx, tenantID, providerID, patientID uuid.UUID) (int, error) {
	type seedMsg struct {
		from, to   uuid.UUID
		kind, body string
	}
	msgs := []seedMsg{
		{providerID, patientID, "message", "Hi Jordan, welcome to Demo Clinic. Reply here anytime."},
		{patientID, providerID, "message", "Thanks, Dr. Shaw! Looking forward to our appointment."},
		{providerID, patientID, "memo", "Reminder: please log a weigh-in before Friday."},
	}
	count := 0
	for _, m := range msgs {
		var inserted bool
		err := tx.QueryRow(ctx,
			`insert into messages (tenant_id, sender_id, recipient_id, kind, body)
			select $1, $2, $3, $4, $5
			where not exists (
				select 1 from messages
				where tenant_id = $1 and sender_id = $2 and recipient_id = $3
				  and kind = $4 and body = $5)
			returning true`, tenantID, m.from, m.to, m.kind, m.body).Scan(&inserted)
		if isNoRows(err) {
			continue // already present
		}
		if err != nil {
			return 0, fmt.Errorf("seed: insert message: %w", err)
		}
		if err := audit.Record(ctx, tx, audit.Event{
			TenantID: tenantID, ActorID: &m.from,
			Action: "message.create", ResourceType: "message",
			Detail: json.RawMessage(`{"kind":"` + m.kind + `","seed":true}`),
		}); err != nil {
			return 0, fmt.Errorf("seed: audit message: %w", err)
		}
		count++
	}
	return count, nil
}

// ensureAppointment schedules a ~2-day-out visit if no appointment exists
// between the pair yet. The moving timestamp is safe because the guard is on
// pair existence, not on the timestamp.
func ensureAppointment(ctx context.Context, tx pgx.Tx, tenantID, providerID, patientID uuid.UUID) (bool, error) {
	start := time.Now().Add(48 * time.Hour).Truncate(time.Hour)
	end := start.Add(30 * time.Minute)

	var inserted bool
	err := tx.QueryRow(ctx,
		`insert into appointments (tenant_id, provider_id, patient_id, starts_at, ends_at)
		select $1, $2, $3, $4, $5
		where not exists (
			select 1 from appointments
			where tenant_id = $1 and provider_id = $2 and patient_id = $3)
		returning true`, tenantID, providerID, patientID, start, end).Scan(&inserted)
	if isNoRows(err) {
		return false, nil // already present
	}
	if err != nil {
		return false, fmt.Errorf("seed: insert appointment: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		TenantID: tenantID, ActorID: &providerID,
		Action: "appointment.create", ResourceType: "appointment",
		Detail: json.RawMessage(`{"starts_at":"` + start.Format(time.RFC3339) + `","seed":true}`),
	}); err != nil {
		return false, fmt.Errorf("seed: audit appointment: %w", err)
	}
	return true, nil
}

func printCredentials(out io.Writer, r *Result) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "=== WeHelp seed complete ===")
	fmt.Fprintln(out, "Tenant:", DemoTenant, "("+r.TenantID.String()+")")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Provider (login with these):")
	fmt.Fprintln(out, "  email:    ", ProviderEmail)
	fmt.Fprintln(out, "  password: ", ProviderPassword)
	fmt.Fprintln(out, "  role:     ", r.Provider.Role)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Patient 1 (login with these):")
	fmt.Fprintln(out, "  email:    ", Patient1Email)
	fmt.Fprintln(out, "  password: ", Patient1Password)
	fmt.Fprintln(out, "  role:     ", r.Patient1.Role)
	fmt.Fprintln(out, "  link:     ", r.Link1)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Patient 2 (login with these):")
	fmt.Fprintln(out, "  email:    ", Patient2Email)
	fmt.Fprintln(out, "  password: ", Patient2Password)
	fmt.Fprintln(out, "  role:     ", r.Patient2.Role)
	fmt.Fprintln(out, "  link:     ", r.Link2)
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Seeded %d new message(s); appointment created this run: %v\n", r.Messages, r.Appointed)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Try:")
	fmt.Fprintln(out, `  curl -s localhost:8080/api/v1/auth/login -d '{"tenant":"`+DemoTenant+`","email":"`+ProviderEmail+`","password":"`+ProviderPassword+`"}' -H 'content-type: application/json'`)
	fmt.Fprintln(out, "  # then: curl -s localhost:8080/api/v1/me -H 'authorization: Bearer <access_token>'")
	fmt.Fprintln(out, "=== end seed ===")
}

func isNoRows(err error) bool { return err == pgx.ErrNoRows }
