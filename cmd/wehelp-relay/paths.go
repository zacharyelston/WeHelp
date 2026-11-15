package main

import (
	"path/filepath"

	"github.com/zacharyelston/wehelp/internal/config"
)

// inRelayDir resolves relative file settings against relay_dir, so one
// relay.env works wherever the Relay's folder is mounted: a launchd job on a
// Mac mini (~/.wehelp/relay) or a container (/var/lib/wehelp-relay) (#180).
func inRelayDir(cfg config.Config) config.Config {
	for _, p := range []*string{
		&cfg.RelayAPNsKey, &cfg.RelaySiteCert, &cfg.RelaySiteKey, &cfg.RelayTURNSecret,
		&cfg.RelayTURNTLSCert, &cfg.RelayTURNTLSKey, &cfg.RelayACMEDir, &cfg.RelayLogDir,
	} {
		if *p != "" && *p != "-" && !filepath.IsAbs(*p) {
			*p = filepath.Join(cfg.RelayDir, *p)
		}
	}
	return cfg
}
