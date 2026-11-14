package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/zacharyelston/wehelp/internal/config"
	"github.com/zacharyelston/wehelp/internal/office"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create the office keys and, with --install-token, install with the Registry",
	Long: `Creates any missing office key in office_dir (never replacing one):
the office SSH key (to the Relay), the office TLS key (pinned by phones) and
the local CA (staff UI). Safe to run again.

With --install-token, also installs this office with the WeHelp Registry and
stores the office ID, Relay address and host key, and Registry signing key.
See docs/OFFICE_KEYS.md.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg := config.C()
		id, created, err := office.Ensure(cfg.OfficeDir)
		if err != nil {
			return err
		}
		for _, f := range created {
			fmt.Fprintln(os.Stderr, "created", f)
		}
		token, _ := cmd.Flags().GetString("install-token")
		if token == "" {
			token = os.Getenv("WEHELP_INSTALL_TOKEN") // avoids the token in shell history
		}
		if token != "" {
			regURL := cfg.RegistryURL
			if u, err := url.Parse(regURL); err == nil && u.Scheme == "http" {
				slog.Warn("installing over plain HTTP; use an https registry_url outside development", "registry_url", regURL)
			}
			r, err := id.Install(cmd.Context(), &http.Client{Timeout: 30 * time.Second}, regURL, token)
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "installed as office", r.OfficeID, "on relay", r.Relay.Name)
		}

		summary := map[string]any{
			"office_dir":     id.Dir,
			"ssh_public_key": id.SSHAuthorizedKey(),
			"key_pin":        id.KeyPin(),
			"installed":      id.Registration != nil,
		}
		if p, err := id.Pairing(); err == nil {
			summary["pairing"] = p
			summary["office_host"] = id.Host()
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(summary)
	},
}

func init() {
	initCmd.Flags().String("install-token", "", "single-use install token from WeHelp support (or set WEHELP_INSTALL_TOKEN)")
}
