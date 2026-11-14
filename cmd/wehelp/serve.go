package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/zacharyelston/wehelp/internal/config"
	"github.com/zacharyelston/wehelp/internal/server"
	"github.com/zacharyelston/wehelp/internal/store"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the WeHelp API server",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		cfg := config.C()

		pool, err := store.NewPool(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer pool.Close()

		return server.New(cfg, pool).ListenAndServe(ctx)
	},
}
