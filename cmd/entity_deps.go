package cmd

import (
	"fmt"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/spf13/cobra"
)

var entityDepsCmd = &cobra.Command{
	Use:   "deps <entity-id> <add|remove|check>",
	Short: "Manage entity dependencies",
	Long: `Add, remove, or check dependencies on a planning entity.

Examples:
  # Add a dependency
  kindship entity deps 550e8400-... add --on 660e8400-...

  # Remove a dependency
  kindship entity deps 550e8400-... remove --on 660e8400-...

  # Check all dependencies
  kindship entity deps 550e8400-... check`,
	Args: cobra.ExactArgs(2),
	RunE: runEntityDeps,
}

var entityDepsOn string

func runEntityDeps(cmd *cobra.Command, args []string) error {
	entityID := args[0]
	op := args[1]

	if op != "add" && op != "remove" && op != "check" {
		return fmt.Errorf("operation must be one of: add, remove, check (got %q)", op)
	}

	if (op == "add" || op == "remove") && entityDepsOn == "" {
		return fmt.Errorf("--on <dep-id> is required for %s operation", op)
	}

	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	client := api.NewClient(ctx.APIBaseURL, verbose)

	resp, err := client.ManageDeps(ctx, entityID, op, entityDepsOn)
	if err != nil {
		return fmt.Errorf("failed to %s dependency: %w", op, err)
	}

	if resp.Message != "" {
		fmt.Println(resp.Message)
	}

	if op == "check" && len(resp.Dependencies) > 0 {
		fmt.Printf("Dependencies (%d):\n", len(resp.Dependencies))
		for _, d := range resp.Dependencies {
			title := d.DependsOnTitle
			if title == "" {
				title = d.DependsOnID
			}
			status := d.Status
			if status == "" {
				status = "unknown"
			}
			fmt.Printf("  %s [%s]\n", title, status)
		}
	} else if op == "check" {
		fmt.Println("No dependencies.")
	}

	return nil
}

func init() {
	entityDepsCmd.Flags().StringVar(&entityDepsOn, "on", "", "Dependency entity ID (required for add/remove)")
}
