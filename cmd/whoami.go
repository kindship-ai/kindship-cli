package cmd

import (
	"github.com/spf13/cobra"
)

var whoamiCmd = &cobra.Command{
	Use:    "whoami",
	Short:  "Display current authentication status",
	Long:   `Alias for 'kindship status'. Use 'kindship status' instead.`,
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Forward --json flag to status
		if whoamiJSON {
			statusJSON = true
		}
		return runStatus(cmd, args)
	},
}

var (
	whoamiJSON bool
)

func init() {
	whoamiCmd.Flags().BoolVar(&whoamiJSON, "json", false, "Output in JSON format")
	rootCmd.AddCommand(whoamiCmd)
}
