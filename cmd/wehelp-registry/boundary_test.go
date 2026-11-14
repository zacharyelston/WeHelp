package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestImportBoundary fails the build if the Registry links any Back Office
// data package. The Registry holds business data only (ARCHITECTURE.md,
// "Registry internals"); this keeps patient data code out of it.
func TestImportBoundary(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	const mod = "github.com/zacharyelston/wehelp/internal/"
	forbidden := []string{"store", "audit", "ledger", "seed", "server", "auth"}
	for _, dep := range strings.Fields(string(out)) {
		for _, f := range forbidden {
			if dep == mod+f || strings.HasPrefix(dep, mod+f+"/") {
				t.Errorf("wehelp-registry imports Back Office package %s", dep)
			}
		}
	}
}
