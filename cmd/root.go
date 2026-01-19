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
  kindship plan        Submit and manage plans
  kindship run next    Get and execute the next work item

For agent containers:
  kindship auth        Inject secrets into subprocess environment
  kindship run <id>    Execute a specific planning entity`,
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
