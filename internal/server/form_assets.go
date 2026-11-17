package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // DecodeConfig for uploaded form images
	_ "image/png"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/forms"
)

// Form images (#195): a display item shows an image uploaded to its form
// version. PNG and JPEG only, sniffed and decoded far enough to check the
// format and size, within the V2_CONTRACTS budgets. Nothing is fetched from
// elsewhere and nothing executable is accepted. A published version's
// images are immutable (trigger in V25); a new draft gets copies.
//
// internal/filetype (#170) will own upload checks for every kind of file
// when it lands; until then this is the form-image check.

const (
	formAssetMaxBytes   = 10 << 20 // per image
	formAssetMaxCount   = 20       // per version
	formAssetMaxTotal   = 64 << 20 // per version
	formAssetMaxPixels  = 20_000_000
	formAssetUploadSlop = 64 << 10 // multipart overhead
)

type formAssetView struct {
	Name        string    `json:"name"`
	ContentType string    `json:"content_type"`
	Size        int       `json:"size"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"created_at"`
}

var assetNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var assetUnsafe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// formAssetName turns an uploaded file name into an asset name: safe
// characters only, starting with a letter or digit, with a .png, .jpg or
// .jpeg extension.
func formAssetName(filename string) (string, string, error) {
	base := path.Base(strings.ReplaceAll(filename, "\\", "/"))
	ext := strings.ToLower(path.Ext(base))
	var ct string
	switch ext {
	case ".png":
		ct = "image/png"
	case ".jpg", ".jpeg":
		ct = "image/jpeg"
	default:
		return "", "", errors.New("form images must be PNG or JPEG")
	}
	stem := strings.Trim(assetUnsafe.ReplaceAllString(strings.TrimSuffix(base, path.Ext(base)), "-"), "-._")
	if stem == "" {
		stem = "image"
	}
	if len(stem) > 120 {
		stem = stem[:120]
	}
	name := stem + ext
	if !assetNameRe.MatchString(name) {
		return "", "", errors.New("unusable file name")
	}
	return name, ct, nil
}

// checkFormImage sniffs the bytes, decodes the header and checks the
// format matches the name and the image is at most 20 megapixels.
func checkFormImage(data []byte, ct string) error {
	if len(data) == 0 || len(data) > formAssetMaxBytes {
		return fmt.Errorf("an image must be 1 byte to %d MB", formAssetMaxBytes>>20)
	}
	if sniffed := http.DetectContentType(data); sniffed != ct {
		return errors.New("the file's contents don't match its type")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return errors.New("the image can't be read")
	}
	if (format == "png") != (ct == "image/png") || (format == "jpeg") != (ct == "image/jpeg") {
		return errors.New("the file's contents don't match its type")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > formAssetMaxPixels {
		return errors.New("images are limited to 20 megapixels")
	}
	return nil
}

func loadFormAssets(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, vid uuid.UUID) ([]formAssetView, error) {
	rows, err := q.Query(ctx, `select name, content_type, octet_length(data), sha256, created_at
		from form_assets where form_version_id = $1 order by name`, vid)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (formAssetView, error) {
		var a formAssetView
		err := row.Scan(&a.Name, &a.ContentType, &a.Size, &a.SHA256, &a.CreatedAt)
		return a, err
	})
	if list == nil {
		list = []formAssetView{}
	}
	return list, err
}

// copyLatestAssets gives a new draft (to) copies of the images of version
// latest of the form, so its definition's media keep resolving.
func copyLatestAssets(ctx context.Context, tx pgx.Tx, fid uuid.UUID, latest int, to uuid.UUID) error {
	if latest < 1 {
		return nil
	}
	_, err := tx.Exec(ctx, `insert into form_assets (form_version_id, name, content_type, data, sha256, created_by)
		select $3, a.name, a.content_type, a.data, a.sha256, a.created_by
		from form_assets a join form_versions v on v.id = a.form_version_id
		where v.form_id = $1 and v.version = $2`, fid, latest, to)
	return err
}

// missingFormAsset returns the path of the first media item whose asset
// isn't uploaded to the version, or "".
func missingFormAsset(def *forms.Definition, have map[string]bool) (string, string) {
	for i, s := range def.Sections {
		for j, p := range s.Pages {
			for k, it := range p.Items {
				if it.Media != nil && !have[it.Media.Asset] {
					return fmt.Sprintf("sections[%d].pages[%d].items[%d].media.asset", i, j, k), it.Media.Asset
				}
			}
		}
	}
	return "", ""
}

// UploadFormAsset adds an image to a draft. Multipart, field "file".
// Admin, LAN; audited as form_asset.upload.
// Route: POST /api/v1/form-versions/{versionID}/assets
func (s *Server) UploadFormAsset(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, true)
	if !ok {
		return
	}
	vid, ok := uuidParam(w, r, "versionID", "version")
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, formAssetMaxBytes+formAssetUploadSlop)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "send a multipart form with a PNG or JPEG of at most 10 MB")
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "file is required")
		return
	}
	defer f.Close()
	name, ct, err := formAssetName(hdr.Filename)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, formAssetMaxBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "upload failed")
		return
	}
	if err := checkFormImage(data, ct); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	s.formTx(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
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
		var count, total int
		if err := tx.QueryRow(ctx, `select count(*), coalesce(sum(octet_length(data)), 0) from form_assets where form_version_id = $1`, vid).Scan(&count, &total); err != nil {
			return nil, err
		}
		if count >= formAssetMaxCount || total+len(data) > formAssetMaxTotal {
			writeErr(w, http.StatusConflict, fmt.Sprintf("a form version holds at most %d images and %d MB", formAssetMaxCount, formAssetMaxTotal>>20))
			return nil, errHandled
		}
		var a formAssetView
		err = tx.QueryRow(ctx, `insert into form_assets (form_version_id, name, content_type, data, sha256, created_by)
			values ($1, $2, $3, $4, $5, $6) returning name, content_type, octet_length(data), sha256, created_at`,
			vid, name, ct, data, digest, id.UserID).Scan(&a.Name, &a.ContentType, &a.Size, &a.SHA256, &a.CreatedAt)
		if isUniqueViolation(err) {
			writeErr(w, http.StatusConflict, "an image named "+name+" is already on this version; remove it or rename yours")
			return nil, errHandled
		}
		if err != nil {
			return nil, err
		}
		if err := formAudit(ctx, tx, id, "form_asset.upload", "form_version", vid, map[string]any{"form_id": v.FormID, "version": v.Version, "asset": name, "sha256": digest}); err != nil {
			return nil, err
		}
		return a, nil
	})
}

// GetFormAsset serves an image of a version. Staff on the LAN; a patient
// for a version on one of their open occurrences (#196).
// Route: GET /api/v1/form-versions/{versionID}/assets/{name}
func (s *Server) GetFormAsset(w http.ResponseWriter, r *http.Request) {
	vid, ok := uuidParam(w, r, "versionID", "version")
	if !ok {
		return
	}
	id, ok := s.formVersionReader(w, r, vid)
	if !ok {
		return
	}
	var ct string
	var data []byte
	err := s.pool.QueryRow(r.Context(), `select a.content_type, a.data from form_assets a
		join form_versions v on v.id = a.form_version_id join forms f on f.id = v.form_id
		where a.form_version_id = $1 and a.name = $2 and f.tenant_id = $3`, vid, chi.URLParam(r, "name"), id.TenantID).Scan(&ct, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "image not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// DeleteFormAsset removes an image from a draft. Admin, LAN; audited as
// form_asset.delete.
// Route: DELETE /api/v1/form-versions/{versionID}/assets/{name}
func (s *Server) DeleteFormAsset(w http.ResponseWriter, r *http.Request) {
	id, ok := s.formsOn(w, r, true)
	if !ok {
		return
	}
	vid, ok := uuidParam(w, r, "versionID", "version")
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
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
		tag, err := tx.Exec(ctx, `delete from form_assets where form_version_id = $1 and name = $2`, vid, name)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			writeErr(w, http.StatusNotFound, "image not found")
			return nil, errHandled
		}
		if err := formAudit(ctx, tx, id, "form_asset.delete", "form_version", vid, map[string]any{"form_id": v.FormID, "version": v.Version, "asset": name}); err != nil {
			return nil, err
		}
		return map[string]any{"name": name}, nil
	})
}
