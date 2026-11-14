package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/zacharyelston/wehelp/internal/config"
	"github.com/zacharyelston/wehelp/internal/store"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Apply pending database migrations via Flyway",
	Long: `Runs Flyway against the configured database. Requires the flyway CLI on
PATH (brew install flyway), or use the containerized equivalent:

    make db-migrate`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := config.C()
		jdbc, err := store.JDBCURL(cfg.DatabaseURL)
		if err != nil {
			return err
		}
		flyway, err := exec.LookPath("flyway")
		if err != nil {
			fmt.Fprintln(os.Stderr, "flyway CLI not found on PATH.")
			fmt.Fprintln(os.Stderr, "Install it (brew install flyway) or run: make db-migrate")
			return err
		}
		c := exec.Command(flyway,
			"-url="+jdbc,
			"-locations=filesystem:db/migrations",
			"-connectRetries=10",
			"migrate")
		c.Stdout, c.Stderr, c.Stdin = os.Stdout, os.Stderr, os.Stdin
		return c.Run()
	},
}
