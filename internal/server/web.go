package server

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/zacharyelston/wehelp/internal/tunnel"
	"github.com/zacharyelston/wehelp/web"
)

// webRoutes serves the embedded static SPA at /. API routes under /api and
// health endpoints take priority; everything else falls through to the SPA.
// The SPA handles its own routing client-side, so unknown paths return
// index.html (no server-side 404 for deep links).
func (s *Server) webRoutes(r chi.Router) {
	static, _ := fs.Sub(web.Files, ".")
	fileServer := http.FileServer(http.FS(static))

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		serveStatic(w, r, static, "index.html")
	})
	r.Get("/app.js", func(w http.ResponseWriter, r *http.Request) {
		serveStatic(w, r, static, "app.js")
	})
	r.Get("/demo.js", serveDemoJS) // demo logins on demo hosts only (#161)
	r.Get("/staff.js", func(w http.ResponseWriter, r *http.Request) {
		serveStatic(w, r, static, "staff.js")
	})
	// The form builder (#195) and its evaluator (#193).
	r.Get("/forms.js", func(w http.ResponseWriter, r *http.Request) {
		serveStatic(w, r, static, "forms.js")
	})
	r.Get("/forms-eval.js", func(w http.ResponseWriter, r *http.Request) {
		serveStatic(w, r, static, "forms-eval.js")
	})
	r.Get("/styles.css", func(w http.ResponseWriter, r *http.Request) {
		serveStatic(w, r, static, "styles.css")
	})
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		// Unknown API paths are real 404s in JSON. Serving the SPA here would
		// hand API clients an HTML page with status 200.
		if isAPIPath(r.URL.Path) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		// SPA fallback: unknown non-API paths get index.html.
		serveStatic(w, r, static, "index.html")
	})
	_ = fileServer
}

// staffWebHeaders lock the staff web down (#159, #160). Scripts come only
// from this origin: no inline script or event handlers (actions use
// data-act, see web/app.js), so an injected handler doesn't run. Inline style
// attributes are allowed; they can't run script. QR codes are data: images.
var staffWebHeaders = map[string]string{
	"Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
	"X-Content-Type-Options": "nosniff",
	"Referrer-Policy":        "no-referrer",
	"X-Frame-Options":        "DENY",
}

func isAPIPath(p string) bool {
	return p == "/api" || strings.HasPrefix(p, "/api/") ||
		p == "/healthz" || p == "/readyz"
}

func serveStatic(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	// The staff web UI exists only on the office LAN (#38).
	if tunnel.ViaRelay(r.Context()) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	for k, v := range staffWebHeaders {
		h.Set(k, v)
	}
	switch name {
	case "index.html":
		h.Set("content-type", "text/html; charset=utf-8")
	case "app.js", "staff.js", "forms.js", "forms-eval.js":
		h.Set("content-type", "text/javascript; charset=utf-8")
	case "styles.css":
		h.Set("content-type", "text/css; charset=utf-8")
	}
	w.Write(data)
}
