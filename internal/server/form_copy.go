package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/forms"
	"github.com/zacharyelston/wehelp/internal/forms/templates"
)

// Starting points (#258): duplicate a form, copy a built-in instrument into
// an editable custom form, or start from a bundled template. Every copy is
// a new custom form with one draft, audited as form.create with a
// copied_from detail.

// instrumentCopyNote is added to the description of a copied built-in.
const instrumentCopyNote = "Copied from the built-in %s. Once edited, this is no longer the validated instrument; keep the attribution."

// retitle sets a definition's title (canonical JSON in and out).
func retitle(canon []byte, title string) []byte {
	return editDefinition(canon, func(m map[string]any) { m["title"] = title })
}

func editDefinition(canon []byte, edit func(map[string]any)) []byte {
	var m map[string]any
	d := json.NewDecoder(strings.NewReader(string(canon)))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		return canon
	}
	edit(m)
	b, err := json.Marshal(m)
	if err != nil {
		return canon
	}
	out, err := forms.Canonical(b)
	if err != nil {
		return canon
	}
	return out
}

// ListFormTemplates lists the starter templates for "New form". Admin, LAN.
// Route: GET /api/v1/form-templates
func (s *Server) ListFormTemplates(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.formsOn(w, r, true); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": templates.All()})
}

type copyFormRequest struct {
	VersionID *uuid.UUID `json:"version_id"` // default: the latest published version, else the latest
	Name      string     `json:"name"`       // default: "<name> (copy)"
}

// CopyForm makes a new custom form from a version of another: a duplicate
// of a custom form, or an editable copy of a built-in instrument (keeping
// its copyright, with a note that edits make it no longer the validated
// instrument). Images come too. Admin, LAN; audited as form.create with
// copied_from.
// Route: POST /api/v1/forms/{formID}/copy
func (s *Server) CopyForm(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, true)
	if !ok {
		return
	}
	fid, ok := uuidParam(w, r, "formID", "form")
	if !ok {
		return
	}
	var req copyFormRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	req.Name = strings.TrimSpace(req.Name)
	if utf8.RuneCountInString(req.Name) > 200 {
		writeErr(w, http.StatusBadRequest, "a name is at most 200 characters")
		return
	}
	s.formTx(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var name, kind string
		var key *string
		err := tx.QueryRow(ctx, `select name, kind, instrument_key from forms where id = $1 and tenant_id = $2`, fid, id.TenantID).Scan(&name, &kind, &key)
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "form not found")
			return nil, errHandled
		}
		if err != nil {
			return nil, err
		}
		if kind == "instrument" {
			// The field gate (#199) covers copies too: a copy of PHQ-9
			// carries its self-harm flag.
			if ok, err := s.instrumentsAvailable(ctx, id.TenantID); err != nil {
				return nil, err
			} else if !ok {
				writeErr(w, http.StatusConflict, "built-in instruments aren't available on this practice")
				return nil, errHandled
			}
		}
		var vid uuid.UUID
		var version int
		var def []byte
		q := `select id, version, definition from form_versions where form_id = $1 and ($2::uuid is null or id = $2)
			order by (status = 'published') desc, version desc limit 1`
		if err := tx.QueryRow(ctx, q, fid, req.VersionID).Scan(&vid, &version, &def); errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "form version not found")
			return nil, errHandled
		} else if err != nil {
			return nil, err
		}
		canon, err := forms.Canonical(def)
		if err != nil {
			return nil, err
		}
		newName := req.Name
		if newName == "" {
			newName = name + " (copy)"
			if utf8.RuneCountInString(newName) > 200 {
				newName = string([]rune(newName)[:194]) + " (copy)"
			}
		}
		copiedFrom := map[string]any{"form_id": fid, "version_id": vid, "version": version}
		if kind == "instrument" && key != nil {
			copiedFrom["instrument"] = *key
			canon = editDefinition(canon, func(m map[string]any) {
				note := strings.Replace(instrumentCopyNote, "%s", name, 1)
				if d, _ := m["description"].(string); d != "" {
					note += " " + d
				}
				m["description"] = note
			})
		}
		if _, err := forms.Parse(canon); err != nil {
			return nil, err
		}
		newFID, newVID, err := insertForm(ctx, tx, id, newName, canon)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `insert into form_assets (form_version_id, name, content_type, data, sha256, created_by)
			select $2, name, content_type, data, sha256, created_by from form_assets where form_version_id = $1`, vid, newVID); err != nil {
			return nil, err
		}
		if err := formAudit(ctx, tx, id, "form.create", "form", newFID, map[string]any{
			"form_id": newFID, "version_id": newVID, "version": 1, "copied_from": copiedFrom}); err != nil {
			return nil, err
		}
		return map[string]any{"form_id": newFID, "version_id": newVID, "version": 1, "name": newName}, nil
	})
}
