package main

import (
	"context"
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

Migrations are applied first so 'wehelp seed' works against a fresh database.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		cfg := config.C()

		if err := store.Migrate(ctx, cfg.DatabaseURL); err != nil {
			return err
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
