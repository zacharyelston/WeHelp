package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/site"
)

// Staff profiles (#102). Each staff member edits their own name as shown,
// credentials, bio and photo, and chooses whether the practice website shows
// them. Publishing the site turns the chosen profiles into provider bios
// (see profileBios). Only the signed-in person's own profile is reachable
// here; all routes are LAN only.

const maxProfilePhoto = 2 << 20

// photoTypes are the accepted photo types and the extension each publishes as.
var photoTypes = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp"}

type staffProfile struct {
	DisplayName   string     `json:"display_name"`
	Credentials   string     `json:"credentials"`
	Bio           string     `json:"bio"`
	ShowOnWebsite bool       `json:"show_on_website"`
	HasPhoto      bool       `json:"has_photo"`
	Role          string     `json:"role"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
}

func loadProfile(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, userID uuid.UUID) (staffProfile, error) {
	var p staffProfile
	err := q.QueryRow(ctx, `
		select u.display_name, u.role::text, coalesce(sp.credentials, ''), coalesce(sp.bio, ''),
		       coalesce(sp.show_on_website, false), sp.photo is not null, sp.updated_at
		from users u left join staff_profiles sp on sp.user_id = u.id
		where u.id = $1`, userID).
		Scan(&p.DisplayName, &p.Role, &p.Credentials, &p.Bio, &p.ShowOnWebsite, &p.HasPhoto, &p.UpdatedAt)
	return p, err
}

// GetStaffProfile returns the signed-in staff member's profile.
func (s *Server) GetStaffProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	p, err := loadProfile(r.Context(), s.pool, id.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, p)
}

type profileUpdate struct {
	DisplayName   string `json:"display_name"`
	Credentials   string `json:"credentials"`
	Bio           string `json:"bio"`
	ShowOnWebsite bool   `json:"show_on_website"`
}

// PutStaffProfile saves the signed-in staff member's own profile.
func (s *Server) PutStaffProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	var req profileUpdate
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.DisplayName, req.Credentials, req.Bio = strings.TrimSpace(req.DisplayName), strings.TrimSpace(req.Credentials), strings.TrimSpace(req.Bio)
	switch {
	case req.DisplayName == "" || utf8.RuneCountInString(req.DisplayName) > 120:
		writeErr(w, http.StatusBadRequest, "name is required, at most 120 characters")
		return
	case utf8.RuneCountInString(req.Credentials) > 120:
		writeErr(w, http.StatusBadRequest, "credentials are limited to 120 characters")
		return
	case utf8.RuneCountInString(req.Bio) > 3000:
		writeErr(w, http.StatusBadRequest, "the bio is limited to 3000 characters")
		return
	}
	s.profileChange(w, r, id, map[string]any{"fields": "text", "show_on_website": req.ShowOnWebsite},
		func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `update users set display_name = $2 where id = $1`, id.UserID, req.DisplayName); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `
				insert into staff_profiles (user_id, tenant_id, credentials, bio, show_on_website) values ($1, $2, $3, $4, $5)
				on conflict (user_id) do update set credentials = excluded.credentials, bio = excluded.bio,
					show_on_website = excluded.show_on_website, updated_at = now()`,
				id.UserID, id.TenantID, req.Credentials, req.Bio, req.ShowOnWebsite)
			return err
		})
}

// PutStaffProfilePhoto replaces the signed-in staff member's photo. The body
// is the image itself (PNG, JPEG or WebP, at most 2 MB), checked by content.
func (s *Server) PutStaffProfilePhoto(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxProfilePhoto+1))
	if err != nil || len(data) == 0 || len(data) > maxProfilePhoto {
		writeErr(w, http.StatusBadRequest, "the photo must be at most 2 MB")
		return
	}
	ctype := http.DetectContentType(data)
	if _, ok := photoTypes[ctype]; !ok {
		writeErr(w, http.StatusBadRequest, "the photo must be a PNG, JPEG or WebP image")
		return
	}
	s.profileChange(w, r, id, map[string]any{"fields": "photo", "bytes": len(data)},
		func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				insert into staff_profiles (user_id, tenant_id, photo, photo_type) values ($1, $2, $3, $4)
				on conflict (user_id) do update set photo = excluded.photo, photo_type = excluded.photo_type, updated_at = now()`,
				id.UserID, id.TenantID, data, ctype)
			return err
		})
}

// DeleteStaffProfilePhoto removes the signed-in staff member's photo.
func (s *Server) DeleteStaffProfilePhoto(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	s.profileChange(w, r, id, map[string]any{"fields": "photo_removed"},
		func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `update staff_profiles set photo = null, photo_type = null, updated_at = now() where user_id = $1`, id.UserID)
			return err
		})
}

// profileChange runs one change to the caller's own profile with its audit
// event and answers with the profile.
func (s *Server) profileChange(w http.ResponseWriter, r *http.Request, id Identity, detail map[string]any,
	change func(ctx context.Context, tx pgx.Tx) error) {
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	if err := change(ctx, tx); err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{TenantID: id.TenantID, ActorID: &id.UserID, Action: "staff.profile_update",
		ResourceType: "user", ResourceID: id.UserID.String(), Detail: mustJSON(detail)}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	p, err := loadProfile(ctx, tx, id.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// GetStaffProfilePhoto serves the signed-in staff member's photo, for the
// preview in the staff web.
func (s *Server) GetStaffProfilePhoto(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	var data []byte
	var ctype string
	err := s.pool.QueryRow(r.Context(), `select photo, photo_type from staff_profiles where user_id = $1 and photo is not null`, id.UserID).
		Scan(&data, &ctype)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "no photo")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(data)
}

// websiteProfile is a profile shown on the website, as the Website tab
// lists it.
type websiteProfile struct {
	Name        string `json:"name"`
	Credentials string `json:"credentials"`
	HasPhoto    bool   `json:"has_photo"`
}

// profileBios returns the bios and photos of enabled staff who chose to be
// on the website, in name order.
func profileBios(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) ([]site.Provider, []site.File, error) {
	rows, err := tx.Query(ctx, `
		select u.id, u.display_name, sp.credentials, sp.bio, sp.photo, sp.photo_type
		from staff_profiles sp join users u on u.id = sp.user_id
		where sp.tenant_id = $1 and sp.show_on_website and u.disabled_at is null and u.role <> 'patient'
		order by u.display_name`, tenant)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var bios []site.Provider
	var files []site.File
	for rows.Next() {
		var uid uuid.UUID
		var p site.Provider
		var photo []byte
		var ctype *string
		if err := rows.Scan(&uid, &p.Name, &p.Credentials, &p.Bio, &photo, &ctype); err != nil {
			return nil, nil, err
		}
		if ctype != nil {
			p.PhotoFile = "staff-photo-" + strings.ReplaceAll(uid.String(), "-", "")[:12] + photoTypes[*ctype]
			files = append(files, site.File{Kind: "image", Title: "Photo of " + p.Name, Filename: p.PhotoFile, ContentType: *ctype, Data: photo})
		}
		bios = append(bios, p)
	}
	return bios, files, rows.Err()
}

// mergeBios puts the profile bios first and keeps the typed bios whose
// names no profile uses.
func mergeBios(profiles, typed []site.Provider) []site.Provider {
	seen := map[string]bool{}
	for _, p := range profiles {
		seen[strings.ToLower(strings.TrimSpace(p.Name))] = true
	}
	out := append([]site.Provider{}, profiles...)
	for _, p := range typed {
		if !seen[strings.ToLower(strings.TrimSpace(p.Name))] {
			out = append(out, p)
		}
	}
	return out
}
