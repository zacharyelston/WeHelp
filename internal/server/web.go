package server

import (
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
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
	r.Get("/styles.css", func(w http.ResponseWriter, r *http.Request) {
		serveStatic(w, r, static, "styles.css")
	})
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		// SPA fallback: unknown non-API paths get index.html.
		serveStatic(w, r, static, "index.html")
	})
	_ = fileServer
}

func serveStatic(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch name {
	case "index.html":
		w.Header().Set("content-type", "text/html; charset=utf-8")
	case "app.js":
		w.Header().Set("content-type", "application/javascript")
	case "styles.css":
		w.Header().Set("content-type", "text/css")
	}
	w.Write(data)
}
