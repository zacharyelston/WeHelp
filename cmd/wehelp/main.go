package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zacharyelston/wehelp/internal/config"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "wehelpd",
	Short: "WeHelp — open source software suite for healthcare providers",
	// Print the error alone on runtime failures, not the usage text.
	SilenceUsage: true,
}

func init() {
	cobra.OnInitialize(func() {
		if err := config.Load(cfgFile); err != nil {
			fmt.Fprintln(os.Stderr, "config:", err)
			os.Exit(1)
		}
	})
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: ./wehelp.yaml or /etc/wehelp/wehelp.yaml)")
	rootCmd.AddCommand(serveCmd, migrateCmd, seedCmd, initCmd, staffCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
