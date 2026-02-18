package cmd

import (
	"fmt"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/spf13/cobra"
)

var entityMoveCmd = &cobra.Command{
	Use:   "move <entity-id>",
	Short: "Move an entity to a new parent",
	Long: `Move a planning entity to a new parent and optionally set its sequence position.

Examples:
  # Move to a new parent
  kindship entity move 550e8400-... --parent 660e8400-...

  # Move and set sequence order
  kindship entity move 550e8400-... --parent 660e8400-... --seq 3`,
	Args: cobra.ExactArgs(1),
	RunE: runEntityMove,
}

var (
	entityMoveParent string
	entityMoveSeq    int
	entityMoveSeqSet bool
)

func runEntityMove(cmd *cobra.Command, args []string) error {
	entityID := args[0]

	if entityMoveParent == "" {
		return fmt.Errorf("--parent is required")
	}

	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	client := api.NewClient(ctx.APIBaseURL, verbose)

	moveReq := api.EntityMoveRequest{
		ParentID: entityMoveParent,
	}

	if cmd.Flags().Changed("seq") {
		moveReq.SequenceOrder = &entityMoveSeq
	}

	resp, err := client.MoveEntity(ctx, entityID, moveReq)
	if err != nil {
		return fmt.Errorf("failed to move entity: %w", err)
	}

	fmt.Printf("Moved %s: %s\n", resp.Entity.Type, resp.Entity.Title)
	fmt.Printf("  ID:     %s\n", resp.Entity.ID)
	if resp.Entity.ParentID != nil {
		fmt.Printf("  Parent: %s\n", *resp.Entity.ParentID)
	}
	fmt.Printf("  Seq:    %d\n", resp.Entity.SequenceOrder)
	return nil
}

func init() {
	entityMoveCmd.Flags().StringVar(&entityMoveParent, "parent", "", "New parent entity ID (required)")
	entityMoveCmd.Flags().IntVar(&entityMoveSeq, "seq", 0, "New sequence order")
}
