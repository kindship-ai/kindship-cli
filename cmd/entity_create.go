package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/spf13/cobra"
)

var entityCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new planning entity",
	Long: `Create a new planning entity.

Examples:
  # Create a task under a project
  kindship entity create --type TASK --parent 550e8400-... --title "Implement auth"

  # Create with description and mode
  kindship entity create --type TASK --parent 550e8400-... \
    --title "Build API" --description "REST endpoints" --mode LLM_REASONING

  # Create from JSON file
  kindship entity create -f entity.json

  # Create from stdin
  echo '{"type":"TASK","title":"Test"}' | kindship entity create -f -

  # File + flag overrides (flags win)
  kindship entity create -f base.json --title "Override title"`,
	RunE: runEntityCreate,
}

var (
	entityCreateType   string
	entityCreateParent string
	entityCreateTitle  string
	entityCreateDesc   string
	entityCreateStatus string
	entityCreateMode   string
	entityCreateCode   string
	entityCreateFile   string
	entityCreateSeq    int
	entityCreateTags   []string
	entityCreateFormat string
	entityCreateCodeFile string
)

func runEntityCreate(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	client := api.NewClient(ctx.APIBaseURL, verbose)

	var createReq api.EntityCreateRequest

	// Load from file if provided
	if entityCreateFile != "" {
		var data []byte
		if entityCreateFile == "-" {
			data, err = readStdin()
			if err != nil {
				return fmt.Errorf("failed to read stdin: %w", err)
			}
		} else {
			data, err = os.ReadFile(entityCreateFile)
			if err != nil {
				return fmt.Errorf("failed to read file %s: %w", entityCreateFile, err)
			}
		}
		if err := json.Unmarshal(data, &createReq); err != nil {
			return fmt.Errorf("failed to parse JSON: %w", err)
		}
	}

	// Flags override file values
	if cmd.Flags().Changed("type") {
		createReq.Type = entityCreateType
	}
	if cmd.Flags().Changed("parent") {
		createReq.ParentID = &entityCreateParent
	}
	if cmd.Flags().Changed("title") {
		createReq.Title = entityCreateTitle
	}
	if cmd.Flags().Changed("description") {
		createReq.Description = entityCreateDesc
	}
	if cmd.Flags().Changed("status") {
		createReq.Status = entityCreateStatus
	}
	if cmd.Flags().Changed("mode") {
		createReq.ExecutionMode = entityCreateMode
	}
	if cmd.Flags().Changed("code") {
		createReq.Code = &entityCreateCode
	}
	if cmd.Flags().Changed("code-file") {
		codeData, err := os.ReadFile(entityCreateCodeFile)
		if err != nil {
			return fmt.Errorf("failed to read code file %s: %w", entityCreateCodeFile, err)
		}
		codeStr := string(codeData)
		createReq.Code = &codeStr
	}
	if cmd.Flags().Changed("seq") {
		createReq.SequenceOrder = &entityCreateSeq
	}
	if cmd.Flags().Changed("tags") {
		createReq.Tags = entityCreateTags
	}

	// Validate required fields
	if createReq.Type == "" {
		return fmt.Errorf("--type is required")
	}
	if createReq.Title == "" {
		return fmt.Errorf("--title is required")
	}
	if createReq.ParentID == nil && createReq.Type != "PRIME_DIRECTIVE" {
		return fmt.Errorf("--parent is required for %s entities", createReq.Type)
	}

	resp, err := client.CreateEntity(ctx, createReq)
	if err != nil {
		return fmt.Errorf("failed to create entity: %w", err)
	}

	if entityCreateFormat == "json" {
		return printJSON(resp)
	}

	fmt.Printf("Created %s: %s\n", resp.Entity.Type, resp.Entity.Title)
	fmt.Printf("  ID: %s\n", resp.Entity.ID)
	fmt.Printf("  Status: %s\n", resp.Entity.Status)
	return nil
}

func readStdin() ([]byte, error) {
	return os.ReadFile("/dev/stdin")
}

func init() {
	entityCreateCmd.Flags().StringVar(&entityCreateType, "type", "", "Entity type (PRIME_DIRECTIVE, OBJECTIVE, PROJECT, PROCESS, TASK, SUBTASK)")
	entityCreateCmd.Flags().StringVar(&entityCreateParent, "parent", "", "Parent entity ID (required except for PRIME_DIRECTIVE)")
	entityCreateCmd.Flags().StringVar(&entityCreateTitle, "title", "", "Entity title")
	entityCreateCmd.Flags().StringVar(&entityCreateDesc, "description", "", "Entity description")
	entityCreateCmd.Flags().StringVar(&entityCreateStatus, "status", "", "Initial status (default: DRAFT)")
	entityCreateCmd.Flags().StringVar(&entityCreateMode, "mode", "", "Execution mode (LLM_REASONING, BASH, PYTHON, etc.)")
	entityCreateCmd.Flags().StringVar(&entityCreateCode, "code", "", "Inline code for BASH/PYTHON entities")
	entityCreateCmd.Flags().StringVar(&entityCreateCodeFile, "code-file", "", "Read code from file")
	entityCreateCmd.Flags().IntVar(&entityCreateSeq, "seq", 0, "Sequence order")
	entityCreateCmd.Flags().StringSliceVar(&entityCreateTags, "tags", nil, "Tags (comma-separated)")
	entityCreateCmd.Flags().StringVarP(&entityCreateFile, "file", "f", "", "JSON file (or - for stdin)")
	entityCreateCmd.Flags().StringVar(&entityCreateFormat, "format", "text", "Output format: text|json")
}
