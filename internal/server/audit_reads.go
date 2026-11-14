package server

import (
	"bytes"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/zacharyelston/wehelp/internal/audit"
)

// Reads of patient data are audited too (#40): who (actor and device) looked
// at what (the resource, or the practice for a list), one event per request
// and never per row. The response is held until the event is written; if it
// can't be written, the caller gets a 500 and the data is not sent, so no
// read of patient data goes unrecorded.
//
// The event holds IDs and the request's filters only: no names, no message
// text. Polling screens record each poll, which is the point: the log shows
// every time the data was on someone's screen.

// auditQueryKeys are the filters worth recording; all are IDs, dates or
// flags, never free text.
var auditQueryKeys = []string{"with", "provider_id", "from", "to", "status", "unread"}

type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header         { return b.header }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }
func (b *bufferedResponse) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

// auditRead records action for a successful read. param names the URL
// parameter that identifies the resource; empty means the practice (a list).
func (s *Server) auditRead(action, resourceType, param string) func(http.Handler) http.Handler {
	return s.auditReadUnless(action, resourceType, param, nil)
}

// auditReadUnless is auditRead for a route that audits some requests itself
// (skip reports those), so nothing is recorded twice.
func (s *Server) auditReadUnless(action, resourceType, param string, skip func(*http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skip != nil && skip(r) {
				next.ServeHTTP(w, r)
				return
			}
			buf := &bufferedResponse{header: http.Header{}}
			next.ServeHTTP(buf, r)
			if buf.status == 0 {
				buf.status = http.StatusOK
			}
			if buf.status == http.StatusOK {
				id, ok := identityFrom(r.Context())
				if !ok {
					writeErr(w, http.StatusUnauthorized, "not signed in")
					return
				}
				rid := id.TenantID.String()
				if param != "" {
					rid = chi.URLParam(r, param)
				}
				detail := map[string]string{}
				if id.DeviceID != nil {
					detail["device_id"] = id.DeviceID.String()
				} else {
					detail["via"] = "staff_web"
				}
				q := r.URL.Query()
				for _, k := range auditQueryKeys {
					if v := q.Get(k); v != "" && len(v) <= 64 {
						detail[k] = v
					}
				}
				ctx := r.Context()
				if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
					return audit.Record(ctx, tx, audit.Event{TenantID: id.TenantID, ActorID: &id.UserID, Action: action,
						ResourceType: resourceType, ResourceID: rid, Detail: mustJSON(detail)})
				}); err != nil {
					slog.Error("audit read failed; response withheld", "action", action, "err", err)
					writeErr(w, http.StatusInternalServerError, "audit failed")
					return
				}
			}
			for k, v := range buf.header {
				w.Header()[k] = v
			}
			w.WriteHeader(buf.status)
			_, _ = w.Write(buf.body.Bytes())
		})
	}
}

// threadRead: GET /messages?with= audits itself as messages.read_thread.
func threadRead(r *http.Request) bool { return r.URL.Query().Get("with") != "" }
