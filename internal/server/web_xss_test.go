package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/zacharyelston/wehelp/web"
)

// TestStaffWebNoInlineHandlers is #159: the staff web uses no inline event
// handlers (onclick=, onchange=, …) or javascript: URLs. Values once went
// into handlers with HTML escaping only, and the browser decodes &#39;
// before running the handler, so a patient name could run script. Actions
// now use data-act with JSON arguments; this keeps it that way.
func TestStaffWebNoInlineHandlers(t *testing.T) {
	inline := regexp.MustCompile(`(?i)\son[a-z]+\s*=\s*["']|javascript:`)
	err := fs.WalkDir(web.Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !(strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".html")) {
			return err
		}
		b, _ := fs.ReadFile(web.Files, path)
		for i, line := range strings.Split(string(b), "\n") {
			if inline.MatchString(line) {
				t.Errorf("%s:%d: inline handler or javascript: URL: %s", path, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestStaffWebHeaders: every staff web file carries a CSP that allows no
// inline script, plus nosniff, no-referrer, no framing and a charset.
func TestStaffWebHeaders(t *testing.T) {
	s := &Server{visits: newVisitHub()}
	h := s.Router()
	for _, path := range []string{"/", "/app.js", "/staff.js", "/forms.js", "/forms-eval.js", "/styles.css", "/some/deep/link"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: %d", path, rec.Code)
			continue
		}
		csp := rec.Header().Get("Content-Security-Policy")
		script := ""
		for _, d := range strings.Split(csp, ";") {
			if d = strings.TrimSpace(d); strings.HasPrefix(d, "script-src") {
				script = d
			}
		}
		if script != "script-src 'self'" || !strings.Contains(csp, "frame-ancestors 'none'") ||
			!strings.Contains(csp, "object-src 'none'") || !strings.Contains(csp, "base-uri 'none'") {
			t.Errorf("GET %s: CSP %q", path, csp)
		}
		for k, want := range map[string]string{"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY"} {
			if got := rec.Header().Get(k); got != want {
				t.Errorf("GET %s: %s = %q, want %q", path, k, got, want)
			}
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), "charset=utf-8") {
			t.Errorf("GET %s: content type %q has no charset", path, rec.Header().Get("Content-Type"))
		}
	}
}
