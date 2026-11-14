package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	_ "time/tzdata" // WEHELP_TIMEZONE works in slim containers (#19)

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
			pusher := &tunnel.Pusher{}
			srv.SetPusher(pusher) // push through the Relay (#7)
			startTunnel(ctx, cfg, id, srv.Router(), pusher)
			if err := startStaffTLS(ctx, cfg, id, srv); err != nil {
				return err
			}
		}
		go srv.RunReminders(ctx, cfg.ReminderInterval) // stops with ctx (#19)
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
func startTunnel(ctx context.Context, cfg config.Config, id *office.Identity, handler http.Handler, pusher *tunnel.Pusher) {
	if !cfg.RelayTunnel {
		return
	}
	go func() {
		if err := tunnel.Run(ctx, id, handler, tunnel.Options{RelayAddress: cfg.RelayAddressOverride, Pusher: pusher}); err != nil {
			slog.Error("relay tunnel stopped", "err", err)
		}
	}()
	slog.Info("relay tunnel starting", "office", id.Registration.OfficeID, "relay", id.Registration.Relay.Name)
}

// startStaffTLS serves the staff web UI over HTTPS with a certificate from
// the office's local CA (#38). Outside dev, plain HTTP then only answers
// health checks and redirects to HTTPS, so staff never sign in in the clear.
func startStaffTLS(ctx context.Context, cfg config.Config, id *office.Identity, srv *server.Server) error {
	if cfg.StaffTLSAddr == "" {
		return nil
	}
	hosts := strings.Split(cfg.StaffHosts, ",")
	if h, err := os.Hostname(); err == nil {
		hosts = append(hosts, h)
	}
	cert, err := id.StaffCert(hosts)
	if err != nil {
		return err
	}
	go func() {
		if err := srv.ServeStaffTLS(ctx, cfg.StaffTLSAddr, cert); err != nil {
			slog.Error("staff HTTPS listener stopped", "err", err)
		}
	}()
	if cfg.Env != "dev" {
		_, port, err := net.SplitHostPort(cfg.StaffTLSAddr)
		if err != nil {
			return err
		}
		srv.RedirectHTTPTo(port)
	}
	return nil
}
