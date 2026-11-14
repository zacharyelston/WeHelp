package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/site"
	"github.com/zacharyelston/wehelp/internal/tunnel"
)

// Practice website and library (#97). Staff edit the site on the office
// LAN; Publish renders it (internal/site) from site_profiles, site_files and
// products only, never a patient table, and sends it to the Relay. Paired
// phones read the library through the pinned tunnel.

// SitePublisher sends a rendered site to the Relay (tunnel.Pusher).
type SitePublisher interface {
	PublishSite(ctx context.Context, bundle []byte) (tunnel.SiteResult, error)
}

// SetSitePublisher enables Publish. Without it (no Relay tunnel) Publish
// reports that the office is not connected.
func (s *Server) SetSitePublisher(p SitePublisher) { s.sitePub = p }

type siteFileView struct {
	ID          uuid.UUID `json:"id"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	Size        int       `json:"size"`
	CreatedAt   time.Time `json:"created_at"`
}

type siteView struct {
	Content      site.Content   `json:"content"`
	Files        []siteFileView `json:"files"`
	UpdatedAt    *time.Time     `json:"updated_at"`
	PublishedAt  *time.Time     `json:"published_at"`
	PublishedURL string         `json:"published_url"`
	// Bios that come from staff profiles (#102); staff edit those under My
	// profile, and they replace a typed bio with the same name.
	Profiles []websiteProfile `json:"profiles"`
	// The website themes to pick from (#104).
	Themes []site.Theme `json:"themes"`
}

// loadContent returns the saved content, or starter content naming the
// practice if staff haven't saved any yet.
func (s *Server) loadContent(ctx context.Context, q pgx.Tx, tenant uuid.UUID) (site.Content, *time.Time, *time.Time, string, error) {
	var raw []byte
	var updated, published *time.Time
	var url string
	err := q.QueryRow(ctx, `select content, updated_at, published_at, published_url from site_profiles where tenant_id = $1`, tenant).
		Scan(&raw, &updated, &published, &url)
	if errors.Is(err, pgx.ErrNoRows) {
		var name string
		if err := q.QueryRow(ctx, `select name from tenants where id = $1`, tenant).Scan(&name); err != nil {
			return site.Content{}, nil, nil, "", err
		}
		return site.DemoContent(name), nil, nil, "", nil
	}
	if err != nil {
		return site.Content{}, nil, nil, "", err
	}
	var c site.Content
	err = json.Unmarshal(raw, &c)
	return c, updated, published, url, err
}

func (s *Server) listSiteFiles(ctx context.Context, q pgx.Tx, tenant uuid.UUID, kind string) ([]siteFileView, error) {
	rows, err := q.Query(ctx, `
		select id, kind, title, filename, content_type, octet_length(data), created_at from site_files
		where tenant_id = $1 and ($2 = '' or kind = $2) order by kind, title`, tenant, kind)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (siteFileView, error) {
		var f siteFileView
		err := row.Scan(&f.ID, &f.Kind, &f.Title, &f.Filename, &f.ContentType, &f.Size, &f.CreatedAt)
		return f, err
	})
	if list == nil {
		list = []siteFileView{}
	}
	return list, err
}

// readTx runs fn in a read-only transaction.
func (s *Server) readTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	return fn(tx)
}

// GetSite returns the website content and uploaded files. Staff, LAN only.
// Route: GET /api/v1/site
func (s *Server) GetSite(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	var v siteView
	v.Themes = site.Themes
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		if v.Content, v.UpdatedAt, v.PublishedAt, v.PublishedURL, err = s.loadContent(r.Context(), tx, id.TenantID); err != nil {
			return err
		}
		if v.Files, err = s.listSiteFiles(r.Context(), tx, id.TenantID, ""); err != nil {
			return err
		}
		bios, _, err := profileBios(r.Context(), tx, id.TenantID)
		v.Profiles = []websiteProfile{}
		for _, b := range bios {
			v.Profiles = append(v.Profiles, websiteProfile{Name: b.Name, Credentials: b.Credentials, HasPhoto: b.PhotoFile != ""})
		}
		return err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// PutSite saves the website content. Staff, LAN only.
// Route: PUT /api/v1/site
func (s *Server) PutSite(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	var c site.Content
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	trimContent(&c)
	if err := c.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, _ := json.Marshal(c)
	if _, err := s.pool.Exec(r.Context(), `
		insert into site_profiles (tenant_id, content, updated_by) values ($1, $2, $3)
		on conflict (tenant_id) do update set content = excluded.content, updated_by = excluded.updated_by, updated_at = now()`,
		id.TenantID, raw, id.UserID); err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"content": c})
}

func trimContent(c *site.Content) {
	for _, p := range []*string{&c.PracticeName, &c.Tagline, &c.About, &c.Hours, &c.Address, &c.Phone, &c.Email, &c.AppURL, &c.Join, &c.LogoFile, &c.Theme} {
		*p = strings.TrimSpace(*p)
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		p.Name, p.Credentials, p.Bio, p.PhotoFile = strings.TrimSpace(p.Name), strings.TrimSpace(p.Credentials), strings.TrimSpace(p.Bio), strings.TrimSpace(p.PhotoFile)
	}
	for i := range c.Services {
		c.Services[i].Name, c.Services[i].Description = strings.TrimSpace(c.Services[i].Name), strings.TrimSpace(c.Services[i].Description)
	}
	for i := range c.Podcasts {
		p := &c.Podcasts[i]
		p.Title, p.URL, p.Description = strings.TrimSpace(p.Title), strings.TrimSpace(p.URL), strings.TrimSpace(p.Description)
	}
}

// UploadSiteFile stores a library download or an image (multipart: kind,
// title, file). Staff, LAN only.
// Route: POST /api/v1/site/files
func (s *Server) UploadSiteFile(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, site.MaxFileBytes+64<<10)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "send a multipart form with a file of at most 10 MB")
		return
	}
	defer r.MultipartForm.RemoveAll()
	kind := r.FormValue("kind")
	title := strings.TrimSpace(r.FormValue("title"))
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "file is required")
		return
	}
	defer f.Close()
	if kind != "library" && kind != "image" {
		writeErr(w, http.StatusBadRequest, "kind must be library or image")
		return
	}
	name, err := site.CleanFilename(hdr.Filename)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ext := path.Ext(name)
	if kind == "image" && ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".webp" && ext != ".ico" {
		writeErr(w, http.StatusBadRequest, "images must be PNG, JPEG, WebP or ICO")
		return
	}
	if title == "" {
		title = strings.TrimSuffix(hdr.Filename, path.Ext(hdr.Filename))
	}
	if len(title) > 200 {
		writeErr(w, http.StatusBadRequest, "title is limited to 200 characters")
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, site.MaxFileBytes+1))
	if err != nil || len(data) == 0 || len(data) > site.MaxFileBytes {
		writeErr(w, http.StatusBadRequest, "file must be 1 byte to 10 MB")
		return
	}
	if !contentMatches(ext, data) {
		writeErr(w, http.StatusBadRequest, "the file's contents don't match its type")
		return
	}
	var v siteFileView
	err = s.pool.QueryRow(r.Context(), `
		insert into site_files (tenant_id, kind, title, filename, content_type, data) values ($1, $2, $3, $4, $5, $6)
		returning id, kind, title, filename, content_type, octet_length(data), created_at`,
		id.TenantID, kind, title, name, site.AllowedTypes[ext], data).
		Scan(&v.ID, &v.Kind, &v.Title, &v.Filename, &v.ContentType, &v.Size, &v.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		writeErr(w, http.StatusConflict, "a file named "+name+" already exists; delete it first or rename yours")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "upload failed")
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// contentMatches sniffs the file so a script can't be uploaded as a .pdf.
func contentMatches(ext string, data []byte) bool {
	got := http.DetectContentType(data)
	switch ext {
	case ".pdf":
		return got == "application/pdf"
	case ".png":
		return got == "image/png"
	case ".jpg", ".jpeg":
		return got == "image/jpeg"
	case ".webp":
		return got == "image/webp"
	case ".ico":
		return got == "image/x-icon" || got == "image/vnd.microsoft.icon" || got == "image/png"
	case ".txt":
		return strings.HasPrefix(got, "text/plain")
	}
	return false
}

// DeleteSiteFile removes an upload. Staff, LAN only.
// Route: DELETE /api/v1/site/files/{fileID}
func (s *Server) DeleteSiteFile(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	fileID, err := uuid.Parse(chi.URLParam(r, "fileID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid file id")
		return
	}
	tag, err := s.pool.Exec(r.Context(), `delete from site_files where id = $1 and tenant_id = $2`, fileID, id.TenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, http.StatusNotFound, "file not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PublishSite renders the site and sends it to the Relay. Staff, LAN only.
// Route: POST /api/v1/site/publish
func (s *Server) PublishSite(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	if featureOff(w, r.Context(), s.pool, id.TenantID, "website") {
		return
	}
	if s.sitePub == nil {
		writeErr(w, http.StatusServiceUnavailable, "this office is not connected to a Relay")
		return
	}
	url, files, size, status, err := s.publishSite(r.Context(), id.TenantID, &id.UserID)
	if err != nil {
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url, "files": files, "bytes": size})
}

// publishSite builds the site, sends it to the Relay and records the
// publish, audited. actor is nil when the scheduler publishes (#121). On
// failure it returns the HTTP status for a handler to answer with.
func (s *Server) publishSite(ctx context.Context, tenant uuid.UUID, actor *uuid.UUID) (string, int, int, int, error) {
	bundle, files, err := s.buildSite(ctx, tenant)
	if err != nil {
		return "", 0, 0, http.StatusBadRequest, err
	}
	pctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	res, err := s.sitePub.PublishSite(pctx, bundle)
	if errors.Is(err, tunnel.ErrNotConnected) {
		return "", 0, 0, http.StatusServiceUnavailable, errors.New("the Relay connection is down; try again shortly")
	}
	if err != nil {
		return "", 0, 0, http.StatusBadGateway, err
	}
	url := res.URL
	if url == "" {
		url = res.Path
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", 0, 0, http.StatusInternalServerError, errors.New("tx failed")
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		insert into site_profiles (tenant_id, published_at, published_url) values ($1, now(), $2)
		on conflict (tenant_id) do update set published_at = now(), published_url = excluded.published_url`, tenant, url); err != nil {
		return "", 0, 0, http.StatusInternalServerError, errors.New("save failed")
	}
	detail, _ := json.Marshal(map[string]any{"files": files, "bytes": len(bundle), "scheduled": actor == nil})
	if err := audit.Record(ctx, tx, audit.Event{TenantID: tenant, ActorID: actor,
		Action: "site.publish", ResourceType: "site", ResourceID: tenant.String(), Detail: detail}); err != nil {
		return "", 0, 0, http.StatusInternalServerError, errors.New("audit failed")
	}
	if err := tx.Commit(ctx); err != nil {
		return "", 0, 0, http.StatusInternalServerError, errors.New("commit failed")
	}
	return url, files, len(bundle), 0, nil
}

// buildSite renders the bundle. It reads site_profiles, site_files and
// products, and nothing else: no patient data can reach the public site.
func (s *Server) buildSite(ctx context.Context, tenant uuid.UUID) ([]byte, int, error) {
	var c site.Content
	var products []site.Product
	var files []site.File
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		var err error
		if c, _, _, _, err = s.loadContent(ctx, tx, tenant); err != nil {
			return err
		}
		pt, on, err := practiceConfig(ctx, tx, tenant) // words (#108) and switches (#109)
		if err != nil {
			return err
		}
		c.Words = pt.Words
		c.Off = map[string]bool{"store": !on["store"], "library": !on["library"]}
		if c.Posts, err = duePosts(ctx, tx, tenant, time.Now()); err != nil { // the blog (#121)
			return err
		}
		rows, err := tx.Query(ctx, `
			select sku, name, description, category, unit, price_cents from products
			where tenant_id = $1 and active order by category, sort, name`, tenant)
		if err != nil {
			return err
		}
		if products, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (site.Product, error) {
			var p site.Product
			err := row.Scan(&p.SKU, &p.Name, &p.Description, &p.Category, &p.Unit, &p.PriceCents)
			return p, err
		}); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `select kind, title, filename, content_type, data from site_files where tenant_id = $1 order by title`, tenant)
		if err != nil {
			return err
		}
		files, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (site.File, error) {
			var f site.File
			err := row.Scan(&f.Kind, &f.Title, &f.Filename, &f.ContentType, &f.Data)
			return f, err
		})
		if err != nil {
			return err
		}
		// Staff profiles shown on the website (#102).
		bios, photos, err := profileBios(ctx, tx, tenant)
		c.Providers = mergeBios(bios, c.Providers)
		files = append(files, photos...)
		return err
	})
	if err != nil {
		return nil, 0, errors.New("could not load the site content")
	}
	return site.Bundle(c, products, files, time.Now().In(s.location()))
}

type libraryView struct {
	Podcasts []site.Podcast `json:"podcasts"`
	Files    []siteFileView `json:"files"`
	SiteURL  string         `json:"site_url"`
}

// GetLibrary lists podcast links and downloads for the app. Any signed-in
// user, including through the Relay.
// Route: GET /api/v1/library
func (s *Server) GetLibrary(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	if featureOff(w, r.Context(), s.pool, id.TenantID, "library") {
		return
	}
	var v libraryView
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		c, _, published, url, err := s.loadContent(r.Context(), tx, id.TenantID)
		if err != nil {
			return err
		}
		v.Podcasts = c.Podcasts
		if published != nil {
			v.SiteURL = url
		}
		v.Files, err = s.listSiteFiles(r.Context(), tx, id.TenantID, "library")
		return err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	if v.Podcasts == nil {
		v.Podcasts = []site.Podcast{}
	}
	writeJSON(w, http.StatusOK, v)
}

// GetLibraryFile returns one library download. Any signed-in user.
// Route: GET /api/v1/library/files/{fileID}
func (s *Server) GetLibraryFile(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	if featureOff(w, r.Context(), s.pool, id.TenantID, "library") {
		return
	}
	fileID, err := uuid.Parse(chi.URLParam(r, "fileID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid file id")
		return
	}
	var name, ctype string
	var data []byte
	err = s.pool.QueryRow(r.Context(), `select filename, content_type, data from site_files
		where id = $1 and tenant_id = $2 and kind = 'library'`, fileID, id.TenantID).Scan(&name, &ctype, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "file not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}
