package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/spf13/cobra"
)

var entityUpdateCmd = &cobra.Command{
	Use:   "update <entity-id>",
	Short: "Update a planning entity",
	Long: `Update fields on an existing planning entity.

Examples:
  # Update title
  kindship entity update 550e8400-... --title "New title"

  # Update status
  kindship entity update 550e8400-... --status ACTIVE

  # Update from JSON file
  kindship entity update 550e8400-... -f updates.json

  # File + flag overrides
  kindship entity update 550e8400-... -f base.json --title "Override"`,
	Args: cobra.ExactArgs(1),
	RunE: runEntityUpdate,
}

var (
	entityUpdateTitle    string
	entityUpdateDesc     string
	entityUpdateStatus   string
	entityUpdateMode     string
	entityUpdateCode     string
	entityUpdateCodeFile string
	entityUpdateTags     []string
	entityUpdateFile     string
	entityUpdateFormat   string
)

func runEntityUpdate(cmd *cobra.Command, args []string) error {
	entityID := args[0]

	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	client := api.NewClient(ctx.APIBaseURL, verbose)

	var updateReq api.EntityUpdateRequest

	// Load from file if provided
	if entityUpdateFile != "" {
		var data []byte
		if entityUpdateFile == "-" {
			data, err = readStdin()
			if err != nil {
				return fmt.Errorf("failed to read stdin: %w", err)
			}
		} else {
			data, err = os.ReadFile(entityUpdateFile)
			if err != nil {
				return fmt.Errorf("failed to read file %s: %w", entityUpdateFile, err)
			}
		}
		if err := json.Unmarshal(data, &updateReq); err != nil {
			return fmt.Errorf("failed to parse JSON: %w", err)
		}
	}

	// Flags override file values
	if cmd.Flags().Changed("title") {
		updateReq.Title = &entityUpdateTitle
	}
	if cmd.Flags().Changed("description") {
		updateReq.Description = &entityUpdateDesc
	}
	if cmd.Flags().Changed("status") {
		updateReq.Status = &entityUpdateStatus
	}
	if cmd.Flags().Changed("mode") {
		updateReq.ExecutionMode = &entityUpdateMode
	}
	if cmd.Flags().Changed("code") {
		updateReq.Code = &entityUpdateCode
	}
	if cmd.Flags().Changed("code-file") {
		codeData, err := os.ReadFile(entityUpdateCodeFile)
		if err != nil {
			return fmt.Errorf("failed to read code file %s: %w", entityUpdateCodeFile, err)
		}
		codeStr := string(codeData)
		updateReq.Code = &codeStr
	}
	if cmd.Flags().Changed("tags") {
		updateReq.Tags = entityUpdateTags
	}

	resp, err := client.UpdateEntity(ctx, entityID, updateReq)
	if err != nil {
		return fmt.Errorf("failed to update entity: %w", err)
	}

	if entityUpdateFormat == "json" {
		return printJSON(resp)
	}

	fmt.Printf("Updated %s: %s\n", resp.Entity.Type, resp.Entity.Title)
	fmt.Printf("  ID:      %s\n", resp.Entity.ID)
	fmt.Printf("  Status:  %s\n", resp.Entity.Status)
	fmt.Printf("  Version: %d\n", resp.Entity.Version)
	return nil
}

func init() {
	entityUpdateCmd.Flags().StringVar(&entityUpdateTitle, "title", "", "New title")
	entityUpdateCmd.Flags().StringVar(&entityUpdateDesc, "description", "", "New description")
	entityUpdateCmd.Flags().StringVar(&entityUpdateStatus, "status", "", "New status")
	entityUpdateCmd.Flags().StringVar(&entityUpdateMode, "mode", "", "New execution mode")
	entityUpdateCmd.Flags().StringVar(&entityUpdateCode, "code", "", "New inline code")
	entityUpdateCmd.Flags().StringVar(&entityUpdateCodeFile, "code-file", "", "Read code from file")
	entityUpdateCmd.Flags().StringSliceVar(&entityUpdateTags, "tags", nil, "New tags (comma-separated)")
	entityUpdateCmd.Flags().StringVarP(&entityUpdateFile, "file", "f", "", "JSON file with updates (or - for stdin)")
	entityUpdateCmd.Flags().StringVar(&entityUpdateFormat, "format", "text", "Output format: text|json")
}
