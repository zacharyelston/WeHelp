package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/forms"
)

// Forms and form versions (#194): the practice's form library. A form is
// practice content, never patient data; a version is one wehelp.form.v1
// definition, validated by internal/forms on every save. Drafts are
// editable, published versions are immutable (a trigger enforces it), and
// every change is audited with ids and the version number only.
//
// Placement: wehelpd for now (owner, 2026-11-16). Everything forms-related
// is in this file, its own route group in server.go and its own migration
// (V24), so it can move to the data module (#227) as a unit.
//
// Access: staff read on the office LAN; admins on the LAN change. Built-in
// instruments (kind "instrument") refuse every change with 409. With the
// practice's "forms" switch off (the default), every route answers 403.

type formVersionSummary struct {
	ID          uuid.UUID  `json:"id"`
	Version     int        `json:"version"`
	Status      string     `json:"status"` // draft or published
	Digest      string     `json:"digest"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
}

type formSummary struct {
	ID            uuid.UUID           `json:"id"`
	Name          string              `json:"name"`
	Kind          string              `json:"kind"` // custom or instrument
	InstrumentKey *string             `json:"instrument_key,omitempty"`
	CreatedAt     time.Time           `json:"created_at"`
	ArchivedAt    *time.Time          `json:"archived_at,omitempty"`
	Latest        *formVersionSummary `json:"latest,omitempty"`    // the highest version
	Published     *int                `json:"published,omitempty"` // the highest published version number
}

// formsOn checks the caller (staff, or admin when admin is set) and the
// forms switch. It writes the error and returns false when the request
// must stop.
func (s *Server) formsOn(w http.ResponseWriter, r *http.Request, admin bool) (Identity, bool) {
	var id Identity
	var ok bool
	if admin {
		id, ok = adminOnLAN(w, r)
	} else {
		id, ok = staffOnLAN(w, r)
	}
	if !ok {
		return id, false
	}
	_, on, err := practiceConfig(r.Context(), s.pool, id.TenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "settings lookup failed")
		return id, false
	}
	if !on["forms"] {
		writeErr(w, http.StatusForbidden, "Forms are turned off for this practice")
		return id, false
	}
	return id, true
}

// readDefinition reads a request body that is a definition, at most one
// byte over the limit so Parse reports the size.
func readDefinition(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, forms.MaxBytes+1))
}

// validDefinition parses raw and returns its canonical bytes, or writes 422
// with the validator's path and kind.
func validDefinition(w http.ResponseWriter, raw []byte) (*forms.Definition, []byte, bool) {
	def, err := forms.Parse(raw)
	if err != nil {
		var fe *forms.Error
		if errors.As(err, &fe) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error": fe.Error(), "code": "invalid_definition", "path": fe.Path, "kind": fe.Kind})
			return nil, nil, false
		}
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return nil, nil, false
	}
	canon, err := forms.Canonical(raw)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid JSON")
		return nil, nil, false
	}
	return def, canon, true
}

// starterDefinition is a new form's first draft: valid, one welcome page.
func starterDefinition(name string) []byte {
	b, _ := json.Marshal(map[string]any{
		"format": forms.Format,
		"title":  name,
		"sections": []any{map[string]any{
			"id": "s1",
			"pages": []any{map[string]any{
				"id":    "p1",
				"items": []any{map[string]any{"id": "intro", "type": "display", "text": "Welcome"}},
			}},
		}},
	})
	return b
}

// formAudit records one forms change in tx.
func formAudit(ctx context.Context, tx pgx.Tx, id Identity, action, rtype string, rid uuid.UUID, detail map[string]any) error {
	d, _ := json.Marshal(detail)
	return audit.Record(ctx, tx, audit.Event{TenantID: id.TenantID, ActorID: &id.UserID, Action: action,
		ResourceType: rtype, ResourceID: rid.String(), Detail: d})
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

func uuidParam(w http.ResponseWriter, r *http.Request, name, what string) (uuid.UUID, bool) {
	u, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid "+what+" id")
		return u, false
	}
	return u, true
}

// --- reads ------------------------------------------------------------------

const formSummarySQL = `
	select f.id, f.name, f.kind, f.instrument_key, f.created_at, f.archived_at,
	       l.id, l.version, l.status, l.digest, l.created_at, l.updated_at, l.published_at,
	       (select max(version) from form_versions p where p.form_id = f.id and p.status = 'published')
	from forms f
	left join lateral (select * from form_versions v where v.form_id = f.id order by v.version desc limit 1) l on true`

func scanFormSummary(row pgx.Row) (formSummary, error) {
	var f formSummary
	var lid *uuid.UUID
	var lv *int
	var ls, ld *string
	var lc, lu *time.Time
	var lp *time.Time
	err := row.Scan(&f.ID, &f.Name, &f.Kind, &f.InstrumentKey, &f.CreatedAt, &f.ArchivedAt,
		&lid, &lv, &ls, &ld, &lc, &lu, &lp, &f.Published)
	if err == nil && lid != nil {
		f.Latest = &formVersionSummary{ID: *lid, Version: *lv, Status: *ls, Digest: *ld, CreatedAt: *lc, UpdatedAt: *lu, PublishedAt: lp}
	}
	return f, err
}

// ListForms lists the practice's forms, custom and built-in, with each
// one's latest version. Staff, LAN.
// Route: GET /api/v1/forms
func (s *Server) ListForms(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, false)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), formSummarySQL+` where f.tenant_id = $1 order by f.archived_at nulls first, f.kind, lower(f.name)`, id.TenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (formSummary, error) { return scanFormSummary(row) })
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	if list == nil {
		list = []formSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"forms": list})
}

// GetForm returns a form and all its versions (without definitions). Staff, LAN.
// Route: GET /api/v1/forms/{formID}
func (s *Server) GetForm(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, false)
	if !ok {
		return
	}
	fid, ok := uuidParam(w, r, "formID", "form")
	if !ok {
		return
	}
	ctx := r.Context()
	f, err := scanFormSummary(s.pool.QueryRow(ctx, formSummarySQL+` where f.id = $1 and f.tenant_id = $2`, fid, id.TenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "form not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	rows, err := s.pool.Query(ctx, `select id, version, status, digest, created_at, updated_at, published_at
		from form_versions where form_id = $1 order by version desc`, fid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	versions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (formVersionSummary, error) {
		var v formVersionSummary
		err := row.Scan(&v.ID, &v.Version, &v.Status, &v.Digest, &v.CreatedAt, &v.UpdatedAt, &v.PublishedAt)
		return v, err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"form": f, "versions": versions})
}

type formVersionRow struct {
	ID         uuid.UUID
	FormID     uuid.UUID
	FormName   string
	Kind       string
	ArchivedAt *time.Time
	Version    int
	Status     string
	Digest     string
	Definition []byte
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Published  *time.Time
}

// loadVersion reads a version of the tenant's forms; lock adds FOR UPDATE
// on the version and its form (callers in a transaction).
func loadVersion(ctx context.Context, q rowQuerier, tenant, vid uuid.UUID, lock bool) (formVersionRow, error) {
	sql := `select v.id, f.id, f.name, f.kind, f.archived_at, v.version, v.status, v.digest, v.definition,
		       v.created_at, v.updated_at, v.published_at
		from form_versions v join forms f on f.id = v.form_id
		where v.id = $1 and f.tenant_id = $2`
	if lock {
		sql += ` for update`
	}
	var v formVersionRow
	err := q.QueryRow(ctx, sql, vid, tenant).Scan(&v.ID, &v.FormID, &v.FormName, &v.Kind, &v.ArchivedAt, &v.Version,
		&v.Status, &v.Digest, &v.Definition, &v.CreatedAt, &v.UpdatedAt, &v.Published)
	return v, err
}

// GetFormVersion returns one version with its definition. Staff, LAN.
// (Patients get the versions assigned to them with the assignments issue.)
// Route: GET /api/v1/form-versions/{versionID}
func (s *Server) GetFormVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, false)
	if !ok {
		return
	}
	vid, ok := uuidParam(w, r, "versionID", "version")
	if !ok {
		return
	}
	v, err := loadVersion(r.Context(), s.pool, id.TenantID, vid, false)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "form version not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	canon, err := forms.Canonical(v.Definition)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "stored definition unreadable")
		return
	}
	assets, err := loadFormAssets(r.Context(), s.pool, vid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": v.ID, "form_id": v.FormID, "form_name": v.FormName, "kind": v.Kind, "version": v.Version,
		"status": v.Status, "digest": v.Digest, "created_at": v.CreatedAt, "updated_at": v.UpdatedAt,
		"published_at": v.Published, "definition": json.RawMessage(canon), "assets": assets,
	})
}

var fileSlug = regexp.MustCompile(`[^a-z0-9]+`)

// ExportFormVersion downloads a version's definition as canonical JSON:
// importing the file and exporting it again gives the same bytes. Admin, LAN.
// Route: GET /api/v1/form-versions/{versionID}/export
func (s *Server) ExportFormVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, true)
	if !ok {
		return
	}
	vid, ok := uuidParam(w, r, "versionID", "version")
	if !ok {
		return
	}
	v, err := loadVersion(r.Context(), s.pool, id.TenantID, vid, false)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "form version not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	canon, err := forms.Canonical(v.Definition)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "stored definition unreadable")
		return
	}
	slug := strings.Trim(fileSlug.ReplaceAllString(strings.ToLower(v.FormName), "-"), "-")
	if slug == "" {
		slug = "form"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-v%d.json"`, slug, v.Version))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(canon)
}

// --- changes ----------------------------------------------------------------

// formTx runs one forms change in a transaction. change writes its own
// error responses for expected refusals and returns errHandled; any other
// error is a 500 (a unique violation, a race for the one draft, is a 409).
func (s *Server) formTx(w http.ResponseWriter, r *http.Request, status int, change func(ctx context.Context, tx pgx.Tx) (any, error)) {
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	out, err := change(ctx, tx)
	switch {
	case errors.Is(err, errHandled):
		return
	case isUniqueViolation(err):
		writeErr(w, http.StatusConflict, "this form already has a draft")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, status, out)
}

var errHandled = errors.New("response written")

// refuseChange writes 409 for a version or form that can't change.
func refuseChange(w http.ResponseWriter, kind string, archived *time.Time) bool {
	switch {
	case kind == "instrument":
		writeErr(w, http.StatusConflict, "built-in instruments can't be changed")
	case archived != nil:
		writeErr(w, http.StatusConflict, "this form is archived")
	default:
		return false
	}
	return true
}

type createFormRequest struct {
	Name       string          `json:"name"`
	Definition json.RawMessage `json:"definition"` // optional; a starter draft if absent
}

// CreateForm adds a custom form with its first draft. Admin, LAN; audited
// as form.create.
// Route: POST /api/v1/forms
func (s *Server) CreateForm(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, true)
	if !ok {
		return
	}
	var req createFormRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, forms.MaxBytes+4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || utf8.RuneCountInString(req.Name) > 200 {
		writeErr(w, http.StatusBadRequest, "a name is required, at most 200 characters")
		return
	}
	raw := []byte(req.Definition)
	if len(raw) == 0 || string(raw) == "null" {
		raw = starterDefinition(req.Name)
	}
	_, canon, ok := validDefinition(w, raw)
	if !ok {
		return
	}
	s.formTx(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		fid, vid, err := insertForm(ctx, tx, id, req.Name, canon)
		if err != nil {
			return nil, err
		}
		if err := formAudit(ctx, tx, id, "form.create", "form", fid, map[string]any{"form_id": fid, "version_id": vid, "version": 1}); err != nil {
			return nil, err
		}
		return map[string]any{"form_id": fid, "version_id": vid, "version": 1}, nil
	})
}

func insertForm(ctx context.Context, tx pgx.Tx, id Identity, name string, canon []byte) (uuid.UUID, uuid.UUID, error) {
	var fid, vid uuid.UUID
	if err := tx.QueryRow(ctx, `insert into forms (tenant_id, name, created_by) values ($1, $2, $3) returning id`,
		id.TenantID, name, id.UserID).Scan(&fid); err != nil {
		return fid, vid, err
	}
	err := tx.QueryRow(ctx, `insert into form_versions (form_id, version, definition, digest, created_by)
		values ($1, 1, $2, $3, $4) returning id`, fid, canon, forms.Digest(canon), id.UserID).Scan(&vid)
	return fid, vid, err
}

// SaveFormVersion replaces a draft's definition. The body is the
// definition; an invalid one is 422 with the validator's path. Admin, LAN;
// audited as form_version.save.
// Route: PUT /api/v1/form-versions/{versionID}
func (s *Server) SaveFormVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, true)
	if !ok {
		return
	}
	vid, ok := uuidParam(w, r, "versionID", "version")
	if !ok {
		return
	}
	raw, err := readDefinition(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	s.formTx(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		v, err := loadVersion(ctx, tx, id.TenantID, vid, true)
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "form version not found")
			return nil, errHandled
		}
		if err != nil {
			return nil, err
		}
		if refuseChange(w, v.Kind, v.ArchivedAt) {
			return nil, errHandled
		}
		if v.Status != "draft" {
			writeErr(w, http.StatusConflict, "published versions can't be changed; start a new draft")
			return nil, errHandled
		}
		_, canon, ok := validDefinition(w, raw)
		if !ok {
			return nil, errHandled
		}
		digest := forms.Digest(canon)
		if _, err := tx.Exec(ctx, `update form_versions set definition = $2, digest = $3, updated_at = now() where id = $1`,
			vid, canon, digest); err != nil {
			return nil, err
		}
		if err := formAudit(ctx, tx, id, "form_version.save", "form_version", vid, map[string]any{"form_id": v.FormID, "version": v.Version}); err != nil {
			return nil, err
		}
		return map[string]any{"id": vid, "version": v.Version, "digest": digest}, nil
	})
}

// PublishFormVersion freezes a draft. Admin, LAN; audited as
// form_version.publish.
// Route: POST /api/v1/form-versions/{versionID}/publish
func (s *Server) PublishFormVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, true)
	if !ok {
		return
	}
	vid, ok := uuidParam(w, r, "versionID", "version")
	if !ok {
		return
	}
	s.formTx(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		v, err := loadVersion(ctx, tx, id.TenantID, vid, true)
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "form version not found")
			return nil, errHandled
		}
		if err != nil {
			return nil, err
		}
		if refuseChange(w, v.Kind, v.ArchivedAt) {
			return nil, errHandled
		}
		if v.Status != "draft" {
			writeErr(w, http.StatusConflict, "this version is already published")
			return nil, errHandled
		}
		// The stored draft was valid when saved; check it again against the
		// validator that is running now, before freezing it.
		def, _, ok := validDefinition(w, v.Definition)
		if !ok {
			return nil, errHandled
		}
		// Every image the definition names must be uploaded to this version (#195).
		assets, err := loadFormAssets(ctx, tx, vid)
		if err != nil {
			return nil, err
		}
		have := map[string]bool{}
		for _, a := range assets {
			have[a.Name] = true
		}
		if p, name := missingFormAsset(def, have); p != "" {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": p + ": no image " + name + " on this version",
				"code": "invalid_definition", "path": p, "kind": forms.KindReference})
			return nil, errHandled
		}
		var at time.Time
		if err := tx.QueryRow(ctx, `update form_versions set status = 'published', published_at = now(), published_by = $2
			where id = $1 returning published_at`, vid, id.UserID).Scan(&at); err != nil {
			return nil, err
		}
		if err := formAudit(ctx, tx, id, "form_version.publish", "form_version", vid, map[string]any{"form_id": v.FormID, "version": v.Version}); err != nil {
			return nil, err
		}
		return map[string]any{"id": vid, "version": v.Version, "digest": v.Digest, "published_at": at}, nil
	})
}

// lockForm locks one of the tenant's forms for a new version.
func lockForm(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, tenant, fid uuid.UUID) (int, error) {
	var kind string
	var archived *time.Time
	var latest int
	err := tx.QueryRow(ctx, `select kind, archived_at,
		       coalesce((select max(version) from form_versions where form_id = forms.id), 0)
		from forms where id = $1 and tenant_id = $2 for update`, fid, tenant).Scan(&kind, &archived, &latest)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "form not found")
		return 0, errHandled
	}
	if err != nil {
		return 0, err
	}
	if refuseChange(w, kind, archived) {
		return 0, errHandled
	}
	var drafts int
	if err := tx.QueryRow(ctx, `select count(*) from form_versions where form_id = $1 and status = 'draft'`, fid).Scan(&drafts); err != nil {
		return 0, err
	}
	if drafts > 0 {
		writeErr(w, http.StatusConflict, "this form already has a draft")
		return 0, errHandled
	}
	return latest, nil
}

// NewFormDraft starts a draft copied from the form's latest version, with
// the next version number. Admin, LAN; audited as form_version.draft.
// Route: POST /api/v1/forms/{formID}/draft
func (s *Server) NewFormDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, true)
	if !ok {
		return
	}
	fid, ok := uuidParam(w, r, "formID", "form")
	if !ok {
		return
	}
	s.formTx(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		latest, err := lockForm(ctx, tx, w, id.TenantID, fid)
		if err != nil {
			return nil, err
		}
		var vid uuid.UUID
		if err := tx.QueryRow(ctx, `insert into form_versions (form_id, version, definition, digest, created_by)
			select form_id, version + 1, definition, digest, $3 from form_versions where form_id = $1 and version = $2
			returning id`, fid, latest, id.UserID).Scan(&vid); err != nil {
			return nil, err
		}
		if err := copyLatestAssets(ctx, tx, fid, latest, vid); err != nil {
			return nil, err
		}
		if err := formAudit(ctx, tx, id, "form_version.draft", "form_version", vid, map[string]any{"form_id": fid, "version": latest + 1}); err != nil {
			return nil, err
		}
		return map[string]any{"id": vid, "form_id": fid, "version": latest + 1}, nil
	})
}

// ImportForm takes a definition file. Without form_id it becomes a new
// custom form (named by its title) with that draft; with form_id, a new
// draft of that form. Admin, LAN; audited as form.import.
// Route: POST /api/v1/forms/import
func (s *Server) ImportForm(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, true)
	if !ok {
		return
	}
	var target *uuid.UUID
	if q := r.URL.Query().Get("form_id"); q != "" {
		u, err := uuid.Parse(q)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid form id")
			return
		}
		target = &u
	}
	raw, err := readDefinition(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	def, canon, ok := validDefinition(w, raw)
	if !ok {
		return
	}
	s.formTx(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var fid, vid uuid.UUID
		version := 1
		if target == nil {
			name := strings.TrimSpace(def.Title)
			if utf8.RuneCountInString(name) > 200 {
				name = string([]rune(name)[:200])
			}
			if fid, vid, err = insertForm(ctx, tx, id, name, canon); err != nil {
				return nil, err
			}
		} else {
			fid = *target
			latest, err := lockForm(ctx, tx, w, id.TenantID, fid)
			if err != nil {
				return nil, err
			}
			version = latest + 1
			if err := tx.QueryRow(ctx, `insert into form_versions (form_id, version, definition, digest, created_by)
				values ($1, $2, $3, $4, $5) returning id`, fid, version, canon, forms.Digest(canon), id.UserID).Scan(&vid); err != nil {
				return nil, err
			}
			if err := copyLatestAssets(ctx, tx, fid, latest, vid); err != nil {
				return nil, err
			}
		}
		if err := formAudit(ctx, tx, id, "form.import", "form", fid, map[string]any{"form_id": fid, "version_id": vid, "version": version}); err != nil {
			return nil, err
		}
		return map[string]any{"form_id": fid, "version_id": vid, "version": version}, nil
	})
}

// ArchiveForm hides a form from new use; its versions stay. Admin, LAN;
// audited as form.archive.
// Route: POST /api/v1/forms/{formID}/archive
func (s *Server) ArchiveForm(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, true)
	if !ok {
		return
	}
	fid, ok := uuidParam(w, r, "formID", "form")
	if !ok {
		return
	}
	s.formTx(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var kind string
		var archived *time.Time
		err := tx.QueryRow(ctx, `select kind, archived_at from forms where id = $1 and tenant_id = $2 for update`, fid, id.TenantID).Scan(&kind, &archived)
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "form not found")
			return nil, errHandled
		}
		if err != nil {
			return nil, err
		}
		if refuseChange(w, kind, archived) {
			return nil, errHandled
		}
		var at time.Time
		if err := tx.QueryRow(ctx, `update forms set archived_at = now() where id = $1 returning archived_at`, fid).Scan(&at); err != nil {
			return nil, err
		}
		if err := formAudit(ctx, tx, id, "form.archive", "form", fid, map[string]any{"form_id": fid}); err != nil {
			return nil, err
		}
		return map[string]any{"id": fid, "archived_at": at}, nil
	})
}
