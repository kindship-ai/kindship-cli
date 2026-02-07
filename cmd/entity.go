package cmd

import (
	"fmt"
	"os"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/spf13/cobra"
)

var entityCmd = &cobra.Command{
	Use:   "entity",
	Short: "Planning entity commands",
	Long:  `Commands for managing planning entities.`,
}

// recursiveFlag controls whether entity activation cascades to descendants
var recursiveFlag bool

var activateCmd = &cobra.Command{
	Use:   "activate <entity-id>",
	Short: "Activate a planning entity",
	Long: `Activate a DRAFT planning entity, transitioning it to ACTIVE status.

With --recursive, all descendant entities in DRAFT status are also activated.

Examples:
  # Activate a single entity
  kindship entity activate 550e8400-e29b-41d4-a716-446655440000

  # Activate entity and all descendants
  kindship entity activate 550e8400-e29b-41d4-a716-446655440000 --recursive`,
	Args: cobra.ExactArgs(1),
	RunE: runActivate,
}

func runActivate(cmd *cobra.Command, args []string) error {
	entityID := args[0]

	// Read from flags first, fall back to environment variables
	if serviceKey == "" {
		serviceKey = os.Getenv("KINDSHIP_SERVICE_KEY")
	}
	if apiURL == "" {
		apiURL = os.Getenv("KINDSHIP_API_URL")
	}
	if apiURL == "" {
		apiURL = "https://kindship.ai"
	}

	if serviceKey == "" {
		return fmt.Errorf("KINDSHIP_SERVICE_KEY is required (use --service-key flag or KINDSHIP_SERVICE_KEY environment variable)")
	}

	client := api.NewClient(apiURL, verbose)

	resp, err := client.ActivateEntity(entityID, serviceKey, recursiveFlag)
	if err != nil {
		return fmt.Errorf("failed to activate entity: %w", err)
	}

	fmt.Printf("Activated %d entities\n", resp.ActivatedCount)
	for _, id := range resp.ActivatedIDs {
		fmt.Printf("  - %s\n", id)
	}

	return nil
}

func init() {
	activateCmd.Flags().BoolVar(&recursiveFlag, "recursive", false, "Activate all descendant entities")
	activateCmd.Flags().StringVar(&serviceKey, "service-key", "", "Service key for authentication (defaults to KINDSHIP_SERVICE_KEY env var)")
	activateCmd.Flags().StringVar(&apiURL, "api-url", "", "API base URL (defaults to KINDSHIP_API_URL env var or https://kindship.ai)")
	activateCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose logging")

	entityCmd.AddCommand(activateCmd)
	rootCmd.AddCommand(entityCmd)
}
