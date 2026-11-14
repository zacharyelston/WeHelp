// Command wehelp-relay is the WeHelp Relay (#4). Offices connect out to it
// over SSH; phones reach their office through it with HTTP CONNECT. It only
// ever sees TLS ciphertext between phone and office, holds no patient data,
// and imports no Back Office package (boundary_test.go). See docs/RELAY.md.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/zacharyelston/wehelp/internal/config"
	"github.com/zacharyelston/wehelp/internal/officelist"
	"github.com/zacharyelston/wehelp/internal/relay"
	"golang.org/x/crypto/ssh"
)

var cfgFile string

func main() {
	root := &cobra.Command{
		Use:          "wehelp-relay",
		Short:        "WeHelp Relay: routes phones to their office over SSH, seeing only ciphertext",
		SilenceUsage: true,
	}
	cobra.OnInitialize(func() {
		if err := config.Load(cfgFile); err != nil {
			fmt.Fprintln(os.Stderr, "config:", err)
			os.Exit(1)
		}
	})
	root.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: ./wehelp.yaml or /etc/wehelp/wehelp.yaml)")
	root.AddCommand(initCmd(), serveCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create the Relay's SSH host key and TLS key (never replaces one) and print what to register",
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := config.C().RelayDir
			keys, created, err := relay.EnsureKeys(dir)
			if err != nil {
				return err
			}
			for _, f := range created {
				fmt.Fprintln(os.Stderr, "created", f)
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]string{
				"relay_dir":     dir,
				"ssh_host_key":  strings.TrimSpace(string(ssh.MarshalAuthorizedKey(keys.HostKey.PublicKey()))),
				"host_key_file": filepath.Join(dir, relay.HostPubFile),
				"tls_key_pin":   keys.TLSPin,
				"register_with": "wehelp-registry relay add --name <name> --ssh-address <host:port> --connect-address <host:port> --host-key-file " + filepath.Join(dir, relay.HostPubFile),
			})
		},
	}
}

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Accept offices over SSH and phones over CONNECT",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := config.C()
			if cfg.RelayID == "" || cfg.RelayRegistryKey == "" {
				return errors.New("set relay_id (from wehelp-registry relay add) and relay_registry_key (from wehelp-registry pubkey)")
			}
			regKey, err := officelist.ParsePublicKey(cfg.RelayRegistryKey)
			if err != nil {
				return err
			}
			keys, _, err := relay.EnsureKeys(cfg.RelayDir)
			if err != nil {
				return err
			}
			var apns *relay.APNs
			if cfg.RelayAPNsKey != "" {
				if apns, err = relay.LoadAPNs(cfg.RelayAPNsKey, cfg.RelayAPNsKeyID, cfg.RelayAPNsTeam, cfg.RelayAPNsTopic); err != nil {
					return fmt.Errorf("push: %w", err)
				}
				fmt.Fprintf(os.Stderr, "push on: APNs key %s, topic %s\n", cfg.RelayAPNsKeyID, cfg.RelayAPNsTopic)
			}
			r, err := relay.New(relay.Config{
				APNs:         apns,
				RelayID:      cfg.RelayID,
				HostKey:      keys.HostKey,
				TLSCert:      keys.TLSCert,
				RegistryURL:  cfg.RegistryURL,
				RegistryKey:  regKey,
				CachePath:    filepath.Join(cfg.RelayDir, relay.CacheFile),
				ListInterval: cfg.RelayListInterval,
			})
			if err != nil {
				return err
			}
			sshLn, err := net.Listen("tcp", cfg.RelaySSHAddr)
			if err != nil {
				return err
			}
			connectLn, err := net.Listen("tcp", cfg.RelayConnectAddr)
			if err != nil {
				_ = sshLn.Close()
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			fmt.Fprintf(os.Stderr, "wehelp-relay %s: ssh %s, connect %s, tls pin %s\n", cfg.RelayID, cfg.RelaySSHAddr, cfg.RelayConnectAddr, keys.TLSPin)
			return r.Serve(ctx, sshLn, connectLn)
		},
	}
}
