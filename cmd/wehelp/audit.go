package main

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/config"
	"github.com/zacharyelston/wehelp/internal/store"
)

// errChainBroken makes the command exit non-zero.
var errChainBroken = errors.New("audit chain broken")

// auditCmd checks the hash-chained audit log on the Back Office host (#24).
var auditCmd = func() *cobra.Command {
	cmd := &cobra.Command{Use: "audit", Short: "Check the audit log (run on the Back Office host)"}
	var tenant string
	verify := &cobra.Command{
		Use:   "verify",
		Short: "Recompute each practice's audit hash chain; exits 1 if any is broken",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			pool, err := store.NewPool(ctx, config.C().DatabaseURL)
			if err != nil {
				return err
			}
			defer pool.Close()
			sql, args := `select id, name from tenants order by name`, []any{}
			if tenant != "" {
				if id, err := uuid.Parse(tenant); err == nil {
					sql, args = `select id, name from tenants where id = $1`, []any{id}
				} else {
					sql, args = `select id, name from tenants where name = $1`, []any{tenant}
				}
			}
			rows, err := pool.Query(ctx, sql, args...)
			if err != nil {
				return err
			}
			type practice struct {
				id   uuid.UUID
				name string
			}
			var list []practice
			for rows.Next() {
				var p practice
				if err := rows.Scan(&p.id, &p.name); err != nil {
					rows.Close()
					return err
				}
				list = append(list, p)
			}
			rows.Close()
			if len(list) == 0 {
				return fmt.Errorf("no practice matches %q", tenant)
			}
			out := cmd.OutOrStdout()
			broken := false
			for _, p := range list {
				res, err := audit.Verify(ctx, pool, p.id)
				if err != nil {
					return err
				}
				switch {
				case res.BrokenAt != 0:
					broken = true
					fmt.Fprintf(out, "BROKEN  %s (%s): first bad event seq %d, after %d events\n", p.name, p.id, res.BrokenAt, res.Events-1)
				case res.LinkOnly > 0:
					fmt.Fprintf(out, "ok      %s (%s): %d events; %d from before #28 checked by link only\n", p.name, p.id, res.Events, res.LinkOnly)
				default:
					fmt.Fprintf(out, "ok      %s (%s): %d events\n", p.name, p.id, res.Events)
				}
			}
			if broken {
				cmd.SilenceUsage = true
				return errChainBroken
			}
			return nil
		},
	}
	verify.Flags().StringVar(&tenant, "tenant", "", "one practice, by name or ID (default: all)")
	cmd.AddCommand(verify)
	return cmd
}()
