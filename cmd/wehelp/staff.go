package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/auth"
	"github.com/zacharyelston/wehelp/internal/config"
	"github.com/zacharyelston/wehelp/internal/store"
	"golang.org/x/term"
)

// staffCmd manages staff accounts from the Back Office host (#38). Outside
// dev, self-registration over the API is off: whoever can run this command
// on the office machine creates staff accounts. Each person sets up TOTP at
// their first sign-in to the staff web UI.
var staffCmd = func() *cobra.Command {
	cmd := &cobra.Command{Use: "staff", Short: "Manage staff accounts (run on the Back Office host)"}
	var tenant, email, name, role string
	add := &cobra.Command{
		Use:   "add",
		Short: "Create a staff account; the password is read from the terminal or WEHELP_STAFF_PASSWORD",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if role != "provider" && role != "admin" {
				return errors.New("--role must be provider or admin")
			}
			if tenant == "" || !strings.Contains(email, "@") {
				return errors.New("--tenant and a valid --email are required")
			}
			password, err := readPassword()
			if err != nil {
				return err
			}
			if len(password) < 12 {
				return errors.New("password must be at least 12 characters")
			}
			hash, err := auth.HashPassword(password)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			pool, err := store.NewPool(ctx, config.C().DatabaseURL)
			if err != nil {
				return err
			}
			defer pool.Close()
			tx, err := pool.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)
			var tenantID, userID [16]byte
			if err := tx.QueryRow(ctx, `select id from tenants where name = $1`, tenant).Scan(&tenantID); err != nil {
				return fmt.Errorf("practice %q not found: %w", tenant, err)
			}
			if err := tx.QueryRow(ctx, `
				insert into users (tenant_id, email, password_hash, role, display_name)
				values ($1, $2, $3, $4, $5) returning id`, tenantID, email, hash, role, name).Scan(&userID); err != nil {
				return fmt.Errorf("create %s: %w", email, err)
			}
			detail, _ := json.Marshal(map[string]string{"email": email, "role": role, "via": "cli"})
			if err := audit.Record(ctx, tx, audit.Event{TenantID: tenantID, Action: "staff.create",
				ResourceType: "user", ResourceID: fmt.Sprintf("%x", userID), Detail: detail}); err != nil {
				return err
			}
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "created %s (%s). They set up TOTP at their first sign-in to the staff web UI.\n", email, role)
			return nil
		},
	}
	add.Flags().StringVar(&tenant, "tenant", "", "practice name")
	add.Flags().StringVar(&email, "email", "", "staff email (sign-in name)")
	add.Flags().StringVar(&name, "name", "", "display name")
	add.Flags().StringVar(&role, "role", "provider", "provider or admin")
	cmd.AddCommand(add)
	return cmd
}()

func readPassword() (string, error) {
	if p := os.Getenv("WEHELP_STAFF_PASSWORD"); p != "" {
		return p, nil
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "Password (12+ characters): ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}
