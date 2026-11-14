package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestWebFallbackKeepsAPI404s(t *testing.T) {
	s := &Server{}
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})
	s.webRoutes(r)

	cases := []struct {
		path     string
		status   int
		ctPrefix string
	}{
		{"/", 200, "text/html"},
		{"/some/deep/link", 200, "text/html"},
		{"/api/v1/ping", 200, ""},
		{"/api/v1/does-not-exist", 404, "application/json"},
		{"/api/nope", 404, "application/json"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d", c.path, rec.Code, c.status)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, c.ctPrefix) {
			t.Errorf("%s: content-type %q, want prefix %q", c.path, ct, c.ctPrefix)
		}
	}
}
