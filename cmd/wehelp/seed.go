//go:build demo

// The seed command and its demo passwords exist only in demo builds
// (go build -tags demo; `make build` does this), never in the field image
// (deploy/Dockerfile builds without the tag) (#161).

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/zacharyelston/wehelp/internal/config"
	"github.com/zacharyelston/wehelp/internal/seed"
	"github.com/zacharyelston/wehelp/internal/store"
)

var seedProfile string

var seedCmd = &cobra.Command{
	Use:   "seed",
	Short: "Seed demo data: the default demo practice, or a training week (--profile training)",
	Long: `Seed creates demo data so devs, agents and trainers can use the API and
the apps at once. Profiles:

  default   Demo Clinic: an admin, a front desk, a provider, two patients,
            a link, messages and an appointment.
  training  the default, plus a synthetic week at a naturopath office for
            staff training (#80): 3 providers, 2 staff, 25 patients, this
            week's schedule, requests and message threads.

It is idempotent: re-running in the same week writes nothing and appends no
audit events. Credentials are printed to stdout. There is no reset: audit
events can't be deleted, so starting over means a fresh demo database.

Runs only in a demo build with WEHELP_ENV=dev or demo, never against a field
Back Office. Requires an already-migrated database.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		cfg := config.C()
		if cfg.Env != "dev" && cfg.Env != "demo" {
			return fmt.Errorf("seed writes demo accounts with known passwords: run it only with WEHELP_ENV=dev or WEHELP_ENV=demo (now %q), and never against a field Back Office", cfg.Env)
		}
		if seedProfile != "default" && seedProfile != "training" {
			return fmt.Errorf("unknown profile %q: want default or training", seedProfile)
		}

		pool, err := store.NewPool(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer pool.Close()

		if seedProfile == "training" {
			loc := time.Local
			if cfg.Timezone != "" {
				if l, err := time.LoadLocation(cfg.Timezone); err == nil {
					loc = l
				}
			}
			_, err = seed.RunTraining(ctx, pool, os.Stdout, time.Now(), loc)
			return err
		}
		_, err = seed.Run(ctx, pool, os.Stdout)
		return err
	},
}

func init() {
	seedCmd.Flags().StringVar(&seedProfile, "profile", "default", "default or training")
	demoCommands = append(demoCommands, seedCmd)
}
