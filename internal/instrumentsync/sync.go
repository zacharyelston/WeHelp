// Package instrumentsync puts the built-in instruments (#199) into a
// practice's form library as read-only kind=instrument forms. The Back
// Office calls it from the forms runner and the forms list; the demo seed
// calls it too. It is idempotent: a definition already there is left alone,
// and a changed definition publishes a new version, never touching an old one.
package instrumentsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/forms/instruments"
)

// Sync adds or updates the built-ins for tenant inside tx and returns how
// many form versions it published. Changes are audited with no actor (the
// system): form.create the first time, form_version.publish for a new
// definition.
func Sync(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) (int, error) {
	// One sync per practice at a time.
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('wehelp-instruments:' || $1::text))`, tenant); err != nil {
		return 0, err
	}
	n := 0
	for _, in := range instruments.All() {
		var fid uuid.UUID
		created := false
		err := tx.QueryRow(ctx, `select id from forms where tenant_id = $1 and instrument_key = $2`, tenant, in.Key).Scan(&fid)
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.QueryRow(ctx, `insert into forms (tenant_id, name, kind, instrument_key) values ($1, $2, 'instrument', $3) returning id`,
				tenant, in.Definition.Title, in.Key).Scan(&fid); err != nil {
				return n, fmt.Errorf("instrument %s: %w", in.Key, err)
			}
			created = true
		} else if err != nil {
			return n, err
		}
		var latest int
		var digest *string
		if err := tx.QueryRow(ctx, `select coalesce(max(version), 0),
			(select digest from form_versions where form_id = $1 and status = 'published' order by version desc limit 1)
			from form_versions where form_id = $1`, fid).Scan(&latest, &digest); err != nil {
			return n, err
		}
		if digest != nil && *digest == in.Digest {
			continue
		}
		var vid uuid.UUID
		if err := tx.QueryRow(ctx, `insert into form_versions (form_id, version, status, definition, digest, published_at)
			values ($1, $2, 'published', $3, $4, now()) returning id`, fid, latest+1, in.Canonical, in.Digest).Scan(&vid); err != nil {
			return n, fmt.Errorf("instrument %s version: %w", in.Key, err)
		}
		action, rtype, rid := "form_version.publish", "form_version", vid
		if created {
			action, rtype, rid = "form.create", "form", fid
		}
		detail, _ := json.Marshal(map[string]any{"form_id": fid, "version_id": vid, "version": latest + 1, "instrument": in.Key})
		if err := audit.Record(ctx, tx, audit.Event{TenantID: tenant, Action: action, ResourceType: rtype, ResourceID: rid.String(), Detail: detail}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
