package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/zacharyelston/wehelp/internal/config"
	"github.com/zacharyelston/wehelp/internal/office"
	"github.com/zacharyelston/wehelp/internal/server"
	"github.com/zacharyelston/wehelp/internal/store"
	"github.com/zacharyelston/wehelp/internal/tunnel"
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

		srv := server.New(cfg, pool)
		if err := startTunnel(ctx, cfg, srv.Router()); err != nil {
			return err
		}
		return srv.ListenAndServe(ctx)
	},
}

// startTunnel connects this office to its Relay (#4) once it has been
// installed with the Registry (#35). Before install, or with relay_tunnel
// off, the server is reachable on the office LAN only.
func startTunnel(ctx context.Context, cfg config.Config, handler http.Handler) error {
	if !cfg.RelayTunnel {
		return nil
	}
	if _, err := os.Stat(filepath.Join(cfg.OfficeDir, office.RegistrationFile)); err != nil {
		slog.Info("relay tunnel off: office not installed with the Registry", "office_dir", cfg.OfficeDir)
		return nil
	}
	id, _, err := office.Ensure(cfg.OfficeDir)
	if err != nil {
		return err
	}
	go func() {
		if err := tunnel.Run(ctx, id, handler, tunnel.Options{RelayAddress: cfg.RelayAddressOverride}); err != nil {
			slog.Error("relay tunnel stopped", "err", err)
		}
	}()
	slog.Info("relay tunnel starting", "office", id.Registration.OfficeID, "relay", id.Registration.Relay.Name)
	return nil
}
