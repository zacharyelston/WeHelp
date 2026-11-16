package main

import (
	"context"
	"net"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/zacharyelston/wehelp/internal/config"
	"github.com/zacharyelston/wehelp/internal/doctor"
	"github.com/zacharyelston/wehelp/internal/store"
)

var doctorJSON bool

// doctorCmd checks the go-live criteria a machine can check (#81). On an
// appliance run it through deploy/appliance.sh doctor, which adds the host's
// checks (FileVault, automatic login, how Postgres is published) through
// WEHELP_DOCTOR_HOST.
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check whether this Back Office is ready for real patient data",
	Long: `Doctor checks the go-live criteria a machine can check
(docs/onboarding/RUNBOOK.md): office keys, the Registry install, the Relay
tunnel, the audit chain, a recent backup, demo mode off and no seed accounts,
and staff HTTPS from the office CA. One line per check, PASS, FAIL or SKIP.
It exits non-zero if any check fails, and prints no patient data. It fixes
nothing.

Run it on the appliance with 'deploy/appliance.sh doctor', which adds the
host checks.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cfg := config.C()

		var checks []doctor.Func
		checks = append(checks, doctor.ParseHost(os.Getenv("WEHELP_DOCTOR_HOST"))...)

		_, port, _ := net.SplitHostPort(cfg.ListenAddr)
		if port == "" {
			port = "8080"
		}
		d := doctor.Deps{
			OfficeDir:    cfg.OfficeDir,
			RelayTunnel:  cfg.RelayTunnel,
			ReadyURL:     "http://127.0.0.1:" + port + "/readyz",
			StaffTLSAddr: cfg.StaffTLSAddr,
			DemoBuild:    len(demoCommands) > 0, // the seed command exists only in demo builds (#161)
			Env:          cfg.Env,
		}
		pool, err := store.NewPool(ctx, cfg.DatabaseURL)
		if err != nil {
			failed := func(name string) doctor.Func {
				return func(context.Context) doctor.Check {
					return doctor.Check{Name: name, Status: doctor.Fail, Detail: "database unreachable"}
				}
			}
			d.VerifyAudit = func(context.Context) (int64, int, error) { return 0, 0, err }
			d.LastBackup = func(context.Context) (time.Time, bool, error) { return time.Time{}, false, err }
			d.SeedAccounts = func(context.Context) (int, error) { return 0, err }
			checks = append(checks, failed("database"))
		} else {
			defer pool.Close()
			d.VerifyAudit, d.LastBackup, d.SeedAccounts = doctor.FromDB(pool)
		}
		checks = append(checks, doctor.Checks(d)...)

		r := doctor.Run(ctx, checks)
		if doctorJSON {
			if err := r.JSON(cmd.OutOrStdout()); err != nil {
				return err
			}
		} else {
			r.Text(cmd.OutOrStdout())
		}
		if !r.OK {
			os.Exit(1)
		}
		return nil
	},
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorJSON, "json", false, "print the report as JSON, for the go-live record")
	rootCmd.AddCommand(doctorCmd)
}
