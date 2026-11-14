// Command wehelp-registry is the WeHelp Registry (#60): the corporate control
// plane that registers practices, issues install tokens and signs the list of
// offices each Relay may accept. It holds no patient data and imports no Back
// Office data package (boundary_test.go).
//
// In V1-M2 WeHelp support drives it with these subcommands on the Registry
// host; the web console with staff sign-in is #61.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"syscall"
	"text/tabwriter"

	"crypto/ed25519"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"github.com/zacharyelston/wehelp/internal/config"
	"github.com/zacharyelston/wehelp/internal/keyfile"
	"github.com/zacharyelston/wehelp/internal/officelist"
	"github.com/zacharyelston/wehelp/internal/pgurl"
	"github.com/zacharyelston/wehelp/internal/registry"
)

var (
	cfgFile string
	actor   string
)

func main() {
	root := &cobra.Command{
		Use:          "wehelp-registry",
		Short:        "WeHelp Registry: practices, install tokens and the signed office list",
		SilenceUsage: true,
	}
	cobra.OnInitialize(func() {
		if err := config.Load(cfgFile); err != nil {
			fmt.Fprintln(os.Stderr, "config:", err)
			os.Exit(1)
		}
	})
	root.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: ./wehelp.yaml or /etc/wehelp/wehelp.yaml)")
	root.PersistentFlags().StringVar(&actor, "actor", defaultActor(), "who is acting, recorded in the audit log")
	root.AddCommand(serveCmd(), migrateCmd(), keygenCmd(), pubkeyCmd(), relayCmd(), practiceCmd(), tokenCmd(), auditCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func defaultActor() string {
	name := "unknown"
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	return "cli:" + name
}

// open connects to the Registry database. withKey loads the signing key,
// which every command that can change the office list needs.
func open(ctx context.Context, withKey bool) (*registry.Store, func(), error) {
	cfg := config.C()
	var key ed25519.PrivateKey
	if withKey {
		var err error
		if key, err = keyfile.ReadEd25519(cfg.RegistrySigningKey); err != nil {
			return nil, nil, fmt.Errorf("%w (create one with: wehelp-registry keygen)", err)
		}
	}
	pool, err := pgxpool.New(ctx, cfg.RegistryDatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("registry database: %w", err)
	}
	return registry.New(pool, key), pool.Close, nil
}

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Serve the install API and the signed office list",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			st, closeDB, err := open(ctx, true)
			if err != nil {
				return err
			}
			defer closeDB()
			if err := st.EnsureList(ctx); err != nil {
				return err
			}
			cfg := config.C()
			return registry.NewServer(st).ListenAndServe(ctx, cfg.RegistryListenAddr, cfg.RegistryTLSCert, cfg.RegistryTLSKey)
		},
	}
}

func migrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply Registry migrations (db/registry) via Flyway",
		RunE: func(cmd *cobra.Command, _ []string) error {
			jdbc, err := pgurl.JDBC(config.C().RegistryDatabaseURL)
			if err != nil {
				return err
			}
			flyway, err := exec.LookPath("flyway")
			if err != nil {
				return fmt.Errorf("flyway CLI not found on PATH (brew install flyway), or run: make registry-migrate")
			}
			c := exec.Command(flyway, "-url="+jdbc, "-locations=filesystem:db/registry", "-connectRetries=10", "migrate")
			c.Stdout, c.Stderr, c.Stdin = os.Stdout, os.Stderr, os.Stdin
			return c.Run()
		},
	}
}

func keygenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keygen",
		Short: "Create the Registry's ed25519 signing key (never overwrites)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := config.C().RegistrySigningKey
			priv, err := keyfile.NewEd25519(path)
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "wrote", path)
			fmt.Println(officelist.PublicKeyString(priv.Public().(ed25519.PublicKey)))
			return nil
		},
	}
}

func pubkeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pubkey",
		Short: "Print the signing public key that Relays and offices pin",
		RunE: func(cmd *cobra.Command, _ []string) error {
			priv, err := keyfile.ReadEd25519(config.C().RegistrySigningKey)
			if err != nil {
				return err
			}
			fmt.Println(officelist.PublicKeyString(priv.Public().(ed25519.PublicKey)))
			return nil
		},
	}
}

func relayCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "relay", Short: "Manage Relays"}
	var r registry.Relay
	var hostKeyFile string
	add := &cobra.Command{
		Use:   "add",
		Short: "Register a Relay",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if hostKeyFile != "" {
				b, err := os.ReadFile(hostKeyFile)
				if err != nil {
					return err
				}
				r.SSHHostKey = string(b)
			}
			st, closeDB, err := open(cmd.Context(), false)
			if err != nil {
				return err
			}
			defer closeDB()
			out, err := st.AddRelay(cmd.Context(), actor, r)
			if err != nil {
				return err
			}
			fmt.Println(out.ID)
			return nil
		},
	}
	add.Flags().StringVar(&r.Name, "name", "", "unique Relay name")
	add.Flags().StringVar(&r.SSHAddress, "ssh-address", "", "host:port offices dial over SSH")
	add.Flags().StringVar(&r.ConnectAddress, "connect-address", "", "host:port phones dial with CONNECT")
	add.Flags().StringVar(&r.SSHHostKey, "host-key", "", "Relay SSH host public key (authorized_keys line)")
	add.Flags().StringVar(&hostKeyFile, "host-key-file", "", "file holding the Relay SSH host public key")
	list := &cobra.Command{
		Use:   "list",
		Short: "List Relays",
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, closeDB, err := open(cmd.Context(), false)
			if err != nil {
				return err
			}
			defer closeDB()
			rs, err := st.ListRelays(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tSSH\tCONNECT")
			for _, r := range rs {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.ID, r.Name, r.SSHAddress, r.ConnectAddress)
			}
			return tw.Flush()
		},
	}
	cmd.AddCommand(add, list)
	return cmd
}

func practiceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "practice", Short: "Manage practices"}
	var name, relay string
	add := &cobra.Command{
		Use:   "add",
		Short: "Register a practice on a Relay",
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, closeDB, err := open(cmd.Context(), false)
			if err != nil {
				return err
			}
			defer closeDB()
			p, err := st.AddPractice(cmd.Context(), actor, name, relay)
			if err != nil {
				return err
			}
			fmt.Println(p.ID)
			return nil
		},
	}
	add.Flags().StringVar(&name, "name", "", "practice name")
	add.Flags().StringVar(&relay, "relay", "", "name of the Relay to assign")
	list := &cobra.Command{
		Use:   "list",
		Short: "List practices",
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, closeDB, err := open(cmd.Context(), false)
			if err != nil {
				return err
			}
			defer closeDB()
			ps, err := st.ListPractices(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tRELAY\tOFFICE")
			for _, p := range ps {
				office := "-"
				if p.OfficeID != nil {
					office = p.OfficeID.String()
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.ID, p.Name, p.Status, p.Relay, office)
			}
			return tw.Flush()
		},
	}
	status := func(use, short, to string) *cobra.Command {
		return &cobra.Command{
			Use:   use + " <practice-id>",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				id, err := uuid.Parse(args[0])
				if err != nil {
					return err
				}
				st, closeDB, err := open(cmd.Context(), true)
				if err != nil {
					return err
				}
				defer closeDB()
				return st.SetPracticeStatus(cmd.Context(), actor, id, to)
			},
		}
	}
	cmd.AddCommand(add, list,
		status("suspend", "Suspend a practice; its office drops from the next office list", "suspended"),
		status("reactivate", "Reactivate a suspended practice", "active"))
	return cmd
}

func tokenCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "token", Short: "Install tokens"}
	issue := &cobra.Command{
		Use:   "issue <practice-id>",
		Short: "Issue a single-use, 24-hour install token (shown once)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := uuid.Parse(args[0])
			if err != nil {
				return err
			}
			st, closeDB, err := open(cmd.Context(), false)
			if err != nil {
				return err
			}
			defer closeDB()
			tok, exp, err := st.IssueInstallToken(cmd.Context(), actor, id)
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "expires", exp.Format("2006-01-02 15:04 MST"), "- shown once; it cannot be retrieved later")
			fmt.Println(tok)
			return nil
		},
	}
	cmd.AddCommand(issue)
	return cmd
}

func auditCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "audit", Short: "Registry audit log"}
	verify := &cobra.Command{
		Use:   "verify",
		Short: "Verify the hash chain",
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, closeDB, err := open(cmd.Context(), false)
			if err != nil {
				return err
			}
			defer closeDB()
			broken, n, err := st.VerifyAudit(cmd.Context())
			if err != nil {
				return err
			}
			out := map[string]any{"events": n, "intact": broken == 0}
			if broken != 0 {
				out["first_broken_seq"] = broken
			}
			_ = json.NewEncoder(os.Stdout).Encode(out)
			if broken != 0 {
				return fmt.Errorf("audit chain broken at seq %d", broken)
			}
			return nil
		},
	}
	cmd.AddCommand(verify)
	return cmd
}
