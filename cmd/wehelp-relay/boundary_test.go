package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestImportBoundary fails the build if the Relay links any Back Office
// data package. The Relay sees only ciphertext and holds no patient data (ARCHITECTURE.md,
// "Relay internals"); this keeps patient data code out of it.
func TestImportBoundary(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	const mod = "github.com/zacharyelston/wehelp/internal/"
	forbidden := []string{"store", "audit", "ledger", "seed", "server", "auth", "office", "registry", "tunnel"}
	for _, dep := range strings.Fields(string(out)) {
		for _, f := range forbidden {
			if dep == mod+f || strings.HasPrefix(dep, mod+f+"/") {
				t.Errorf("wehelp-relay imports Back Office package %s", dep)
			}
		}
	}
}
