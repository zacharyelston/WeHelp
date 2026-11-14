package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zacharyelston/wehelp/internal/registry"
	"golang.org/x/term"
)

// staffCmd manages Registry console accounts (#61). The first sign-in sets
// up the authenticator; there is no way to sign in without one after that.
func staffCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "staff", Short: "Registry console accounts"}
	var email, name, role string
	add := &cobra.Command{
		Use:   "add",
		Short: "Create a console account; the password is read from the terminal or WEHELP_CONSOLE_PASSWORD",
		RunE: func(cmd *cobra.Command, _ []string) error {
			password, err := readPassword()
			if err != nil {
				return err
			}
			st, closeDB, err := open(cmd.Context(), false)
			if err != nil {
				return err
			}
			defer closeDB()
			id, err := st.AddStaff(cmd.Context(), actor, email, name, role, password)
			if err != nil {
				return err
			}
			fmt.Println(id)
			fmt.Fprintln(os.Stderr, "Sign in at /console/; the first sign-in sets up the authenticator.")
			return nil
		},
	}
	add.Flags().StringVar(&email, "email", "", "sign-in email")
	add.Flags().StringVar(&name, "name", "", "display name")
	add.Flags().StringVar(&role, "role", registry.RoleSupport, "support or registry-admin")
	cmd.AddCommand(add)
	return cmd
}

func readPassword() (string, error) {
	if p := os.Getenv("WEHELP_CONSOLE_PASSWORD"); p != "" {
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
