package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.yaml.in/yaml/v3"
)

// TestOpenAPICoversRoutes keeps api/openapi.yaml and the router in step:
// every API route the server registers must be in the spec, and every
// operation in the spec must exist on the server.
func TestOpenAPICoversRoutes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	methods := map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}
	inSpec := map[string]bool{}
	for path, ops := range spec.Paths {
		for m := range ops {
			if methods[m] {
				inSpec[strings.ToUpper(m)+" "+path] = true
			}
		}
	}

	inRouter := map[string]bool{}
	s := &Server{}
	err = chi.Walk(s.Router(), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/")
		if strings.HasPrefix(route, "/api/") || route == "/healthz" || route == "/readyz" {
			inRouter[method+" "+route] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var missing, phantom []string
	for k := range inRouter {
		if !inSpec[k] {
			missing = append(missing, k)
		}
	}
	for k := range inSpec {
		if !inRouter[k] {
			phantom = append(phantom, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(phantom)
	if len(missing) > 0 {
		t.Errorf("routes missing from api/openapi.yaml:\n  %s", strings.Join(missing, "\n  "))
	}
	if len(phantom) > 0 {
		t.Errorf("api/openapi.yaml documents routes the server does not have:\n  %s", strings.Join(phantom, "\n  "))
	}
}
