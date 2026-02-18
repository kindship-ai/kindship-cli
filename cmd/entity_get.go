package cmd

import (
	"fmt"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/spf13/cobra"
)

var entityGetCmd = &cobra.Command{
	Use:   "get <entity-id>",
	Short: "Get a planning entity by ID",
	Long: `Retrieve a single planning entity with optional includes.

Examples:
  # Get basic entity info
  kindship entity get 550e8400-e29b-41d4-a716-446655440000

  # Include children
  kindship entity get 550e8400-... --children

  # Include runs and dependencies
  kindship entity get 550e8400-... --details

  # JSON output
  kindship entity get 550e8400-... --format json`,
	Args: cobra.ExactArgs(1),
	RunE: runEntityGet,
}

var (
	entityGetChildren bool
	entityGetDetails  bool
	entityGetFormat   string
)

func runEntityGet(cmd *cobra.Command, args []string) error {
	entityID := args[0]

	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	client := api.NewClient(ctx.APIBaseURL, verbose)

	var include []string
	if entityGetChildren {
		include = append(include, "children")
	}
	if entityGetDetails {
		include = append(include, "runs", "dependencies")
	}

	resp, err := client.GetEntity(ctx, entityID, include)
	if err != nil {
		return fmt.Errorf("failed to get entity: %w", err)
	}

	if entityGetFormat == "json" {
		return printJSON(resp)
	}

	printEntityDetail(resp)
	return nil
}

func printEntityDetail(resp *api.EntityGetResponse) {
	e := resp.Entity

	fmt.Printf("%s: %s\n", e.Type, e.Title)
	fmt.Printf("  ID:     %s\n", e.ID)
	fmt.Printf("  Status: %s\n", e.Status)

	if e.ParentID != nil {
		if resp.Parent != nil {
			fmt.Printf("  Parent: %s \"%s\" (%s)\n", resp.Parent.Type, resp.Parent.Title, *e.ParentID)
		} else {
			fmt.Printf("  Parent: %s\n", *e.ParentID)
		}
	}

	if e.ExecutionMode != "" {
		fmt.Printf("  Mode:   %s\n", e.ExecutionMode)
	}

	fmt.Printf("  Seq:    %d\n", e.SequenceOrder)

	if len(e.Tags) > 0 {
		fmt.Printf("  Tags:   %v\n", e.Tags)
	}

	if e.Description != "" {
		fmt.Printf("  Desc:   %s\n", truncate(e.Description, 80))
	}

	if len(resp.Dependencies) > 0 {
		met := 0
		pending := 0
		for _, d := range resp.Dependencies {
			if d.Status == "COMPLETED" || d.Status == "completed" {
				met++
			} else {
				pending++
			}
		}
		fmt.Printf("  Deps:   %d met, %d pending\n", met, pending)
	}

	if len(resp.Children) > 0 {
		fmt.Printf("\n  Children (%d):\n", len(resp.Children))
		for _, child := range resp.Children {
			status := child.Status
			marker := " "
			if status == "COMPLETED" {
				marker = "+"
			} else if status == "ACTIVE" {
				marker = "*"
			}
			fmt.Printf("    [%s] %s: %s [%s]\n", marker, child.Type, child.Title, status)
		}
	}

	if len(resp.Runs) > 0 {
		fmt.Printf("\n  Runs (%d):\n", len(resp.Runs))
		for _, r := range resp.Runs {
			fmt.Printf("    #%d %s (%s)\n", r.AttemptNumber, r.Status, r.StartedAt)
		}
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func init() {
	entityGetCmd.Flags().BoolVar(&entityGetChildren, "children", false, "Include direct children")
	entityGetCmd.Flags().BoolVar(&entityGetDetails, "details", false, "Include runs and dependencies")
	entityGetCmd.Flags().StringVar(&entityGetFormat, "format", "text", "Output format: text|json")
}
