//go:build demo

// The seed command and its demo passwords exist only in demo builds
// (go build -tags demo; `make build` does this), never in the field image
// (deploy/Dockerfile builds without the tag) (#161).

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zacharyelston/wehelp/internal/config"
	"github.com/zacharyelston/wehelp/internal/seed"
	"github.com/zacharyelston/wehelp/internal/store"
)

var seedCmd = &cobra.Command{
	Use:   "seed",
	Short: "Seed a demo tenant, provider, two patients, a link, messages, and an appointment",
	Long: `Seed creates a demo dataset so devs and agents can exercise the API
instantly. It is idempotent: re-running against an already-seeded database
performs no writes and appends no duplicate audit events. Demo credentials
are printed to stdout.

Requires an already-migrated database: 'make db-migrate' or the compose
migrate service (which runs automatically before 'server' starts).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		cfg := config.C()
		if cfg.Env != "dev" && cfg.Env != "demo" {
			return fmt.Errorf("seed writes demo accounts with known passwords: run it only with WEHELP_ENV=dev or WEHELP_ENV=demo (now %q), and never against a field Back Office", cfg.Env)
		}

		pool, err := store.NewPool(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer pool.Close()

		_, err = seed.Run(ctx, pool, os.Stdout)
		return err
	},
}

func init() { demoCommands = append(demoCommands, seedCmd) }
