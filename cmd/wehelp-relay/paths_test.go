package main

import (
	"testing"

	"github.com/zacharyelston/wehelp/internal/config"
)

func TestInRelayDir(t *testing.T) {
	got := inRelayDir(config.Config{RelayDir: "/var/lib/wehelp-relay",
		RelayAPNsKey: "apns.p8", RelaySiteCert: "/etc/ssl/site.crt", RelayLogDir: "-"})
	if got.RelayAPNsKey != "/var/lib/wehelp-relay/apns.p8" {
		t.Errorf("relative APNs key = %q", got.RelayAPNsKey)
	}
	if got.RelaySiteCert != "/etc/ssl/site.crt" {
		t.Errorf("absolute path changed: %q", got.RelaySiteCert)
	}
	if got.RelayLogDir != "-" || got.RelayTURNSecret != "" {
		t.Errorf("stderr or empty changed: %q %q", got.RelayLogDir, got.RelayTURNSecret)
	}
}
