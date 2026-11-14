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
		id, err := loadOffice(cfg)
		if err != nil {
			return err
		}
		if id != nil {
			if p, err := id.Pairing(); err == nil {
				srv.SetPairing(p) // QR pairing (#37)
			}
			startTunnel(ctx, cfg, id, srv.Router())
		}
		return srv.ListenAndServe(ctx)
	},
}

// loadOffice returns the office identity once the office is installed with
// the Registry (#35), or nil before that.
func loadOffice(cfg config.Config) (*office.Identity, error) {
	if _, err := os.Stat(filepath.Join(cfg.OfficeDir, office.RegistrationFile)); err != nil {
		slog.Info("office not installed with the Registry: no relay tunnel, no QR pairing", "office_dir", cfg.OfficeDir)
		return nil, nil
	}
	id, _, err := office.Ensure(cfg.OfficeDir)
	return id, err
}

// startTunnel connects this office to its Relay (#4). With relay_tunnel off,
// the server is reachable on the office LAN only.
func startTunnel(ctx context.Context, cfg config.Config, id *office.Identity, handler http.Handler) {
	if !cfg.RelayTunnel {
		return
	}
	go func() {
		if err := tunnel.Run(ctx, id, handler, tunnel.Options{RelayAddress: cfg.RelayAddressOverride}); err != nil {
			slog.Error("relay tunnel stopped", "err", err)
		}
	}()
	slog.Info("relay tunnel starting", "office", id.Registration.OfficeID, "relay", id.Registration.Relay.Name)
}
