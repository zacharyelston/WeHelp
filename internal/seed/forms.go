package seed

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/forms"
	"github.com/zacharyelston/wehelp/internal/instrumentsync"
)

// DemoFormName is the seeded demo form (#196): three pages, a branch on the
// first page's answer, and two scores. Synthetic, not a clinical instrument.
const DemoFormName = "Demo visit check-in"

//go:embed demo_form.json
var demoFormJSON []byte

// DemoFormsResult reports what RunDemoForms created.
type DemoFormsResult struct {
	FormID, VersionID, AssignmentID, OccurrenceID uuid.UUID
	// The PHQ-9 assignment (#199), when this run made it.
	PHQ9VersionID, PHQ9AssignmentID, PHQ9OccurrenceID uuid.UUID
	Created                                           bool
}

// RunDemoForms turns the forms switch on for the demo practice and gives the
// first demo patient the demo form, published, assigned once and open now
// (since yesterday 09:00, until 09:00 a week from today, local time in loc),
// so a paired phone has a form to open. It also syncs the built-in
// instruments and gives Jordan a PHQ-9 the same way (#199). It runs after
// Run, in its own transaction, and each part is idempotent: what exists is
// left alone, so re-running it on an older demo host adds only the PHQ-9.
// It isn't part of Run, so database tests keep forms off by default.
func RunDemoForms(ctx context.Context, pool *pgxpool.Pool, out io.Writer, now time.Time, loc *time.Location) (*DemoFormsResult, error) {
	def, err := forms.Parse(demoFormJSON)
	if err != nil {
		return nil, fmt.Errorf("seed: demo form: %w", err)
	}
	canon, err := forms.Canonical(demoFormJSON)
	if err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("seed: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var tenant, admin, provider, patient uuid.UUID
	err = tx.QueryRow(ctx, `select t.id,
		(select id from users where tenant_id = t.id and email = $2),
		(select id from users where tenant_id = t.id and email = $3),
		(select id from users where tenant_id = t.id and email = $4)
		from tenants t where t.name = $1`, DemoTenant, AdminEmail, ProviderEmail, Patient1Email).Scan(&tenant, &admin, &provider, &patient)
	if err != nil {
		return nil, fmt.Errorf("seed: demo forms need the demo practice (run the default seed first): %w", err)
	}
	res := &DemoFormsResult{}
	today := now.In(loc)
	start := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	at9 := func(d time.Time) time.Time { return time.Date(d.Year(), d.Month(), d.Day(), 9, 0, 0, 0, loc) }
	const openBefore, closeAfter = 1, 7
	// assignOnce gives Jordan the form version once, open since yesterday
	// 09:00 until 09:00 a week from today, with its audit events.
	assignOnce := func(formID, versionID uuid.UUID) (uuid.UUID, uuid.UUID, error) {
		var aid, oid uuid.UUID
		if err := tx.QueryRow(ctx, `insert into form_assignments (tenant_id, patient_id, form_id, version_id, schedule_kind, start_date,
			open_before_days, close_after_days, timezone, channel, assigned_by)
			values ($1, $2, $3, $4, 'once', $5, $6, $7, $8, 'app', $9) returning id`,
			tenant, patient, formID, versionID, start, openBefore, closeAfter, loc.String(), provider).Scan(&aid); err != nil {
			return aid, oid, fmt.Errorf("seed: assignment: %w", err)
		}
		if err := tx.QueryRow(ctx, `insert into form_occurrences (tenant_id, assignment_id, patient_id, form_id, version_id, due_date,
			opens_at, due_at, closes_at) values ($1, $2, $3, $4, $5, $6, $7, $8, $9) returning id`,
			tenant, aid, patient, formID, versionID, start,
			at9(start.AddDate(0, 0, -openBefore)), at9(start), at9(start.AddDate(0, 0, closeAfter))).Scan(&oid); err != nil {
			return aid, oid, fmt.Errorf("seed: occurrence: %w", err)
		}
		for _, e := range []audit.Event{
			{TenantID: tenant, ActorID: &provider, Action: "form_assignment.create", ResourceType: "form_assignment", ResourceID: aid.String(),
				Detail: mustJSON(map[string]any{"patient_id": patient, "form_id": formID, "schedule": "once", "seed": true})},
			{TenantID: tenant, Action: "form_occurrence.create", ResourceType: "form_occurrence", ResourceID: oid.String(),
				Detail: mustJSON(map[string]any{"assignment_id": aid, "due_date": start.Format("2006-01-02"), "version_id": versionID, "seed": true})},
		} {
			if err := audit.Record(ctx, tx, e); err != nil {
				return aid, oid, fmt.Errorf("seed: audit %s: %w", e.Action, err)
			}
		}
		return aid, oid, nil
	}

	// Forms on.
	if _, err := tx.Exec(ctx, `insert into practice_settings (tenant_id, features, updated_by) values ($1, '{"forms": true}', $2)
		on conflict (tenant_id) do update set features = practice_settings.features || '{"forms": true}', updated_at = now()`, tenant, admin); err != nil {
		return nil, fmt.Errorf("seed: forms switch: %w", err)
	}

	// The demo check-in form.
	err = tx.QueryRow(ctx, `select id from forms where tenant_id = $1 and name = $2`, tenant, DemoFormName).Scan(&res.FormID)
	switch {
	case err == nil:
		fmt.Fprintln(out, "Demo form already present:", DemoFormName)
	case isNoRows(err):
		if err := tx.QueryRow(ctx, `insert into forms (tenant_id, name, created_by) values ($1, $2, $3) returning id`,
			tenant, DemoFormName, admin).Scan(&res.FormID); err != nil {
			return nil, fmt.Errorf("seed: form: %w", err)
		}
		if err := tx.QueryRow(ctx, `insert into form_versions (form_id, version, status, definition, digest, created_by, published_at, published_by)
			values ($1, 1, 'published', $2, $3, $4, now(), $4) returning id`, res.FormID, canon, forms.Digest(canon), admin).Scan(&res.VersionID); err != nil {
			return nil, fmt.Errorf("seed: form version: %w", err)
		}
		for _, e := range []audit.Event{
			{TenantID: tenant, ActorID: &admin, Action: "practice.features", ResourceType: "practice", ResourceID: tenant.String(),
				Detail: json.RawMessage(`{"features":{"forms":true},"seed":true}`)},
			{TenantID: tenant, ActorID: &admin, Action: "form.create", ResourceType: "form", ResourceID: res.FormID.String(),
				Detail: mustJSON(map[string]any{"form_id": res.FormID, "version_id": res.VersionID, "version": 1, "seed": true})},
			{TenantID: tenant, ActorID: &admin, Action: "form_version.publish", ResourceType: "form_version", ResourceID: res.VersionID.String(),
				Detail: mustJSON(map[string]any{"form_id": res.FormID, "version": 1, "seed": true})},
		} {
			if err := audit.Record(ctx, tx, e); err != nil {
				return nil, fmt.Errorf("seed: audit %s: %w", e.Action, err)
			}
		}
		if res.AssignmentID, res.OccurrenceID, err = assignOnce(res.FormID, res.VersionID); err != nil {
			return nil, err
		}
		res.Created = true
		pages := 0
		for _, s := range def.Sections {
			pages += len(s.Pages)
		}
		fmt.Fprintf(out, "Demo form: %q (%d pages), assigned to %s, open until %s. Forms are switched on.\n",
			def.Title, pages, Patient1Email, at9(start.AddDate(0, 0, closeAfter)).Format("Mon Jan 2 15:04 MST"))
	default:
		return nil, err
	}

	// The built-in instruments (#199; demo builds have them) and a PHQ-9 for
	// Jordan, so the demo can show the staff-side self-harm flag on a
	// synthetic answer.
	if _, err := instrumentsync.Sync(ctx, tx, tenant); err != nil {
		return nil, fmt.Errorf("seed: instruments: %w", err)
	}
	var phq9Form uuid.UUID
	var phq9Assigned bool
	if err := tx.QueryRow(ctx, `select f.id, f.id is not null and exists (select 1 from form_assignments a where a.form_id = f.id and a.patient_id = $2)
		from forms f where f.tenant_id = $1 and f.instrument_key = 'phq9'`, tenant, patient).Scan(&phq9Form, &phq9Assigned); err != nil {
		return nil, fmt.Errorf("seed: phq9: %w", err)
	}
	if !phq9Assigned {
		if err := tx.QueryRow(ctx, `select id from form_versions where form_id = $1 and status = 'published' order by version desc limit 1`,
			phq9Form).Scan(&res.PHQ9VersionID); err != nil {
			return nil, err
		}
		if res.PHQ9AssignmentID, res.PHQ9OccurrenceID, err = assignOnce(phq9Form, res.PHQ9VersionID); err != nil {
			return nil, err
		}
		res.Created = true
		fmt.Fprintf(out, "PHQ-9 assigned to %s, open until %s (built-in instruments are on in demo builds).\n",
			Patient1Email, at9(start.AddDate(0, 0, closeAfter)).Format("Mon Jan 2 15:04 MST"))
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
