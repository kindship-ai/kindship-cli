package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/spf13/cobra"
)

var entityDeleteCmd = &cobra.Command{
	Use:   "delete <entity-id>",
	Short: "Delete a planning entity",
	Long: `Soft-delete a planning entity.

Requires confirmation unless --force is provided.

Examples:
  # Delete with confirmation prompt
  kindship entity delete 550e8400-...

  # Delete without confirmation
  kindship entity delete 550e8400-... --force`,
	Args: cobra.ExactArgs(1),
	RunE: runEntityDelete,
}

var entityDeleteForce bool

func runEntityDelete(cmd *cobra.Command, args []string) error {
	entityID := args[0]

	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	if !entityDeleteForce {
		fmt.Printf("Are you sure you want to delete entity %s? [y/N] ", entityID)
		reader := bufio.NewReader(os.Stdin)
		response, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	client := api.NewClient(ctx.APIBaseURL, verbose)

	resp, err := client.DeleteEntity(ctx, entityID, entityDeleteForce)
	if err != nil {
		return fmt.Errorf("failed to delete entity: %w", err)
	}

	if resp.Message != "" {
		fmt.Println(resp.Message)
	} else {
		fmt.Printf("Deleted entity %s\n", entityID)
	}

	return nil
}

func init() {
	entityDeleteCmd.Flags().BoolVar(&entityDeleteForce, "force", false, "Skip confirmation prompt")
}
