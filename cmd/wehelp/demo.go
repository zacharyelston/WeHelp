package main

import "github.com/spf13/cobra"

// demoCommands are commands that exist only in demo builds (seed.go, build
// tag demo). Field builds have none (#161).
var demoCommands []*cobra.Command
