package cmd

import (
	"fmt"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/spf13/cobra"
)

var entityRestoreCmd = &cobra.Command{
	Use:   "restore <entity-id>",
	Short: "Restore a deleted planning entity",
	Long: `Restore a soft-deleted planning entity back to its previous state.

Examples:
  kindship entity restore 550e8400-e29b-41d4-a716-446655440000`,
	Args: cobra.ExactArgs(1),
	RunE: runEntityRestore,
}

func runEntityRestore(cmd *cobra.Command, args []string) error {
	entityID := args[0]

	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	client := api.NewClient(ctx.APIBaseURL, verbose)

	resp, err := client.RestoreEntity(ctx, entityID)
	if err != nil {
		return fmt.Errorf("failed to restore entity: %w", err)
	}

	fmt.Printf("Restored %s: %s\n", resp.Entity.Type, resp.Entity.Title)
	fmt.Printf("  ID:     %s\n", resp.Entity.ID)
	fmt.Printf("  Status: %s\n", resp.Entity.Status)
	return nil
}
