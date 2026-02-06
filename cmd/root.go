package cmd

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "kindship",
	Short: "Kindship CLI for agent operations",
	Long: `Kindship CLI provides utilities for local development and agent containers,
including authentication, planning, and execution management.

For local development:
  kindship login       Authenticate with your Kindship account
  kindship setup       Link a repository to an agent
  kindship status      Show current configuration
  kindship plan next   Get the next executable task
  kindship plan submit Submit a plan

For agent containers:
  kindship auth        Inject secrets into subprocess environment
  kindship run <id>    Execute a planning entity (auto-detects type)
  kindship agent loop  Run autonomous execution loop`,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	// Container commands
	rootCmd.AddCommand(authCmd)
	rootCmd.AddCommand(runCmd)

	// Note: login, logout, whoami, version commands are registered in their respective files
}
