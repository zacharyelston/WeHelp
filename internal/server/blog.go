package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/site"
)

// Practice blog (#121). Staff write timely or seasonal posts in the Website
// tab; the website shows those that are due (not drafts, publish time
// passed), newest first. Practice content only, never patient data. Once a
// minute the Back Office republishes a site whose scheduled post has come
// due since its last publish, so a post set for tomorrow morning goes out
// by itself.

type postView struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	ImageFile string    `json:"image_file"`
	Author    string    `json:"author"`
	Draft     bool      `json:"draft"`
	PublishAt time.Time `json:"publish_at"`
	Status    string    `json:"status"` // draft, scheduled, live
	UpdatedAt time.Time `json:"updated_at"`
}

// duePosts are the posts the site shows at now, newest first.
func duePosts(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, tenant uuid.UUID, now time.Time) ([]site.Post, error) {
	rows, err := q.Query(ctx, `
		select p.id::text, p.title, p.body, p.image_file, u.display_name, p.publish_at
		from site_posts p join users u on u.id = p.author_id
		where p.tenant_id = $1 and not p.draft and p.publish_at <= $2
		order by p.publish_at desc limit $3`, tenant, now, site.MaxPosts)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (site.Post, error) {
		var p site.Post
		err := row.Scan(&p.ID, &p.Title, &p.Body, &p.ImageFile, &p.Author, &p.Date)
		return p, err
	})
}

// ListPosts returns every post, newest first, with its status. Staff, LAN.
// Route: GET /api/v1/site/posts
func (s *Server) ListPosts(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		select p.id, p.title, p.body, p.image_file, u.display_name, p.draft, p.publish_at, p.updated_at
		from site_posts p join users u on u.id = p.author_id
		where p.tenant_id = $1 order by p.publish_at desc`, id.TenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	now := time.Now()
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (postView, error) {
		var p postView
		err := row.Scan(&p.ID, &p.Title, &p.Body, &p.ImageFile, &p.Author, &p.Draft, &p.PublishAt, &p.UpdatedAt)
		switch {
		case p.Draft:
			p.Status = "draft"
		case p.PublishAt.After(now):
			p.Status = "scheduled"
		default:
			p.Status = "live"
		}
		return p, err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	if list == nil {
		list = []postView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"posts": list})
}

type postRequest struct {
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	ImageFile string  `json:"image_file"`
	Draft     bool    `json:"draft"`
	PublishAt *string `json:"publish_at"` // RFC 3339; empty = now
}

func (req *postRequest) check() (time.Time, error) {
	req.Title, req.Body, req.ImageFile = strings.TrimSpace(req.Title), strings.TrimSpace(req.Body), strings.TrimSpace(req.ImageFile)
	switch {
	case req.Title == "" || utf8.RuneCountInString(req.Title) > 200:
		return time.Time{}, errors.New("a title is required, at most 200 characters")
	case req.Body == "" || utf8.RuneCountInString(req.Body) > 20000:
		return time.Time{}, errors.New("the post needs text, at most 20000 characters")
	case len(req.ImageFile) > 100:
		return time.Time{}, errors.New("image file name too long")
	}
	if req.PublishAt == nil || *req.PublishAt == "" {
		return time.Now(), nil
	}
	t, err := time.Parse(time.RFC3339, *req.PublishAt)
	if err != nil {
		return t, errors.New("publish_at must be RFC 3339")
	}
	return t, nil
}

// CreatePost adds a post. Staff, LAN; audited.
// Route: POST /api/v1/site/posts
func (s *Server) CreatePost(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	var req postRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	at, err := req.check()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.postChange(w, r, id, "site_post.create", http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (uuid.UUID, error) {
		var pid uuid.UUID
		err := tx.QueryRow(ctx, `insert into site_posts (tenant_id, title, body, image_file, author_id, draft, publish_at)
			values ($1, $2, $3, $4, $5, $6, $7) returning id`, id.TenantID, req.Title, req.Body, req.ImageFile, id.UserID, req.Draft, at).Scan(&pid)
		return pid, err
	})
}

// UpdatePost changes a post. Staff, LAN; audited.
// Route: PUT /api/v1/site/posts/{postID}
func (s *Server) UpdatePost(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	pid, err := uuid.Parse(chi.URLParam(r, "postID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid post id")
		return
	}
	var req postRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	at, err := req.check()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.postChange(w, r, id, "site_post.update", http.StatusOK, func(ctx context.Context, tx pgx.Tx) (uuid.UUID, error) {
		tag, err := tx.Exec(ctx, `update site_posts set title = $3, body = $4, image_file = $5, draft = $6, publish_at = $7, updated_at = now()
			where id = $1 and tenant_id = $2`, pid, id.TenantID, req.Title, req.Body, req.ImageFile, req.Draft, at)
		if err == nil && tag.RowsAffected() == 0 {
			err = pgx.ErrNoRows
		}
		return pid, err
	})
}

// DeletePost removes a post (practice content, not a record). Staff, LAN;
// audited. The site drops it at the next publish.
// Route: DELETE /api/v1/site/posts/{postID}
func (s *Server) DeletePost(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	pid, err := uuid.Parse(chi.URLParam(r, "postID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid post id")
		return
	}
	s.postChange(w, r, id, "site_post.delete", http.StatusOK, func(ctx context.Context, tx pgx.Tx) (uuid.UUID, error) {
		tag, err := tx.Exec(ctx, `delete from site_posts where id = $1 and tenant_id = $2`, pid, id.TenantID)
		if err == nil && tag.RowsAffected() == 0 {
			err = pgx.ErrNoRows
		}
		return pid, err
	})
}

// postChange runs one post change with its audit event and answers with
// the post's id.
func (s *Server) postChange(w http.ResponseWriter, r *http.Request, id Identity, action string, status int,
	change func(ctx context.Context, tx pgx.Tx) (uuid.UUID, error)) {
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	pid, err := change(ctx, tx)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "post not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{TenantID: id.TenantID, ActorID: &id.UserID, Action: action,
		ResourceType: "site_post", ResourceID: pid.String()}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, status, map[string]any{"id": pid})
}

// publishDuePosts republishes each site that has a post come due since its
// last publish (#121). Only sites published before, with the website on.
// It returns how many sites it published.
func (s *Server) publishDuePosts(ctx context.Context, now time.Time) (int, error) {
	if s.sitePub == nil {
		return 0, nil
	}
	rows, err := s.pool.Query(ctx, `
		select sp.tenant_id from site_profiles sp
		where sp.published_at is not null
		  and exists (select 1 from site_posts p where p.tenant_id = sp.tenant_id and not p.draft
		              and p.publish_at > sp.published_at and p.publish_at <= $1)`, now)
	if err != nil {
		return 0, err
	}
	tenants, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range tenants {
		if _, on, err := practiceConfig(ctx, s.pool, t); err != nil || !on["website"] {
			continue
		}
		if _, _, _, _, err := s.publishSite(ctx, t, nil); err != nil {
			slog.Warn("blog: scheduled publish failed", "tenant", t, "err", err)
			continue
		}
		n++
	}
	return n, nil
}
