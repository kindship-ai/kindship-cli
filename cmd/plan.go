package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"

	"github.com/spf13/cobra"
)

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Plan management commands",
	Long: `Commands for managing planning entities.

Subcommands:
  submit   Submit a plan from file or stdin
  next     Get the next executable task
  export   Export a planning entity as JSON`,
}

var planSubmitCmd = &cobra.Command{
	Use:   "submit [file]",
	Short: "Submit a plan",
	Long: `Submit a plan to create planning entities.

The plan should be in JSON format with the following structure:
{
  "title": "Project title",
  "description": "Project description",
  "tasks": [
    {"title": "Task 1", "description": "..."},
    {"title": "Task 2", "description": "..."}
  ]
}

If no file is provided, reads from stdin.

Examples:
  kindship plan submit plan.json
  cat plan.json | kindship plan submit`,
	RunE: runPlanSubmit,
}

var planExportCmd = &cobra.Command{
	Use:   "export <entity-id>",
	Short: "Export a planning entity as JSON",
	Long: `Export a planning entity in JSON format.

By default, exports a PROJECT or PROCESS with flat child TASKs
in the same JSON format accepted by 'plan submit'.

With --recursive, exports any entity type and its full descendant
tree as nested JSON with 'children' arrays.

Examples:
  kindship plan export <entity-id>
  kindship plan export <entity-id> --include-ids
  kindship plan export <entity-id> --output plan.json
  kindship plan export <entity-id> --format text
  kindship plan export <entity-id> --recursive --format text
  kindship plan export <entity-id> --recursive --include-deleted`,
	Args: cobra.ExactArgs(1),
	RunE: runPlanExport,
}

var planNextCmd = &cobra.Command{
	Use:   "next",
	Short: "Get next executable task",
	Long: `Returns the next task that can be executed.

A task is executable when:
- It is in ACTIVE or READY status
- All its dependencies are completed

Output format:
  --format json    JSON output (default)
  --format text    Human-readable text

Examples:
  kindship plan next
  kindship plan next --format text`,
	RunE: runPlanNext,
}

var (
	planFormat           string
	exportIncludeIDs     bool
	exportOutput         string
	exportRecursive      bool
	exportIncludeDeleted bool
)

func init() {
	planSubmitCmd.Flags().StringVar(&planFormat, "format", "text", "Output format (json, text)")
	planNextCmd.Flags().StringVar(&planFormat, "format", "json", "Output format (json, text)")

	planExportCmd.Flags().StringVar(&planFormat, "format", "json", "Output format (json, text)")
	planExportCmd.Flags().BoolVar(&exportIncludeIDs, "include-ids", false, "Include _metadata block with entity UUIDs")
	planExportCmd.Flags().StringVar(&exportOutput, "output", "", "Write output to file instead of stdout")
	planExportCmd.Flags().BoolVar(&exportRecursive, "recursive", false, "Recursively export full entity tree")
	planExportCmd.Flags().BoolVar(&exportIncludeDeleted, "include-deleted", false, "Include DELETED entities (recursive only)")

	planCmd.AddCommand(planSubmitCmd)
	planCmd.AddCommand(planNextCmd)
	planCmd.AddCommand(planExportCmd)
	rootCmd.AddCommand(planCmd)
}

// PlanSubmitRequest is the request body for plan submission
type PlanSubmitRequest struct {
	AgentID           string         `json:"agent_id"`
	Title             string         `json:"title"`
	Description       string         `json:"description"`
	Tasks             []api.TaskSpec `json:"tasks"`
	Type              string         `json:"type,omitempty"`
	SkipBootstrap     bool           `json:"skip_bootstrap,omitempty"`
	Status            string         `json:"status,omitempty"`
	RecurrencePattern string         `json:"recurrence_pattern,omitempty"`
	Tags              []string       `json:"tags,omitempty"`
}

// PlanSubmitResponse is the response from plan submission
type PlanSubmitResponse struct {
	Success bool `json:"success"`
	Project struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"project"`
	Tasks []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"tasks"`
	ObjectiveID string `json:"objective_id"`
	Error       string `json:"error,omitempty"`
}

func runPlanSubmit(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	// Read plan from file or stdin
	var planData []byte

	if len(args) > 0 {
		// Read from file
		planData, err = os.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("failed to read plan file: %w", err)
		}
	} else {
		// Read from stdin
		planData, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("failed to read from stdin: %w", err)
		}
	}

	if len(planData) == 0 {
		return fmt.Errorf("no plan data provided")
	}

	// Parse the plan
	var plan struct {
		Title             string         `json:"title"`
		Description       string         `json:"description"`
		Tasks             []api.TaskSpec `json:"tasks"`
		Type              string         `json:"type,omitempty"`
		SkipBootstrap     bool           `json:"skip_bootstrap,omitempty"`
		Status            string         `json:"status,omitempty"`
		RecurrencePattern string         `json:"recurrence_pattern,omitempty"`
		Tags              []string       `json:"tags,omitempty"`
	}

	if err := json.Unmarshal(planData, &plan); err != nil {
		return fmt.Errorf("failed to parse plan: %w", err)
	}

	// Build request
	reqBody := PlanSubmitRequest{
		AgentID:           agentID,
		Title:             plan.Title,
		Description:       plan.Description,
		Tasks:             plan.Tasks,
		Type:              plan.Type,
		SkipBootstrap:     plan.SkipBootstrap,
		Status:            plan.Status,
		RecurrencePattern: plan.RecurrencePattern,
		Tags:              plan.Tags,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	// Submit to API
	endpoint := fmt.Sprintf("%s/api/cli/plan/submit", ctx.APIBaseURL)

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to submit plan: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp PlanSubmitResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("submission failed: %s", errResp.Error)
		}
		return fmt.Errorf("submission failed (%d): %s", resp.StatusCode, string(body))
	}

	var submitResp PlanSubmitResponse
	if err := json.Unmarshal(body, &submitResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if planFormat == "json" {
		return printJSON(submitResp)
	}

	// Human-readable output
	fmt.Printf("✓ Created project '%s' with %d tasks\n", submitResp.Project.Title, len(submitResp.Tasks))
	fmt.Printf("  Project ID: %s\n", submitResp.Project.ID)
	for i, task := range submitResp.Tasks {
		fmt.Printf("  [%d] %s (%s)\n", i+1, task.Title, task.ID)
	}

	return nil
}

func runPlanNext(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	// Call plan/next API
	endpoint := fmt.Sprintf("%s/api/cli/plan/next?agent_id=%s", ctx.APIBaseURL, agentID)

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch next task: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp api.PlanNextResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("failed: %s", errResp.Error)
		}
		return fmt.Errorf("failed (%d): %s", resp.StatusCode, string(body))
	}

	var nextResp api.PlanNextResponse
	if err := json.Unmarshal(body, &nextResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if planFormat == "json" {
		return printJSON(nextResp)
	}

	// Human-readable output
	if nextResp.Task == nil {
		fmt.Println("No executable tasks found.")
		if nextResp.Message != "" {
			fmt.Printf("Message: %s\n", nextResp.Message)
		}
		return nil
	}

	fmt.Printf("Next task: %s\n", nextResp.Task.Title)
	fmt.Printf("  ID: %s\n", nextResp.Task.ID)
	if nextResp.Task.Description != "" {
		fmt.Printf("  Description: %s\n", nextResp.Task.Description)
	}
	if nextResp.Task.Rationale != "" {
		fmt.Printf("  Rationale: %s\n", nextResp.Task.Rationale)
	}
	fmt.Printf("  Execution mode: %s\n", nextResp.Task.ExecutionMode)

	return nil
}

func runPlanExport(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	entityID := args[0]

	// Build request URL
	endpoint := fmt.Sprintf("%s/api/cli/plan/export?entity_id=%s", ctx.APIBaseURL, entityID)
	if exportIncludeIDs {
		endpoint += "&include_ids=true"
	}
	if exportRecursive {
		endpoint += "&recursive=true"
		if exportIncludeDeleted {
			endpoint += "&include_deleted=true"
		}
	}

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to export plan: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Try to extract error message from response
		var errObj struct {
			Error string `json:"error,omitempty"`
		}
		if json.Unmarshal(body, &errObj) == nil && errObj.Error != "" {
			return fmt.Errorf("export failed: %s", errObj.Error)
		}
		return fmt.Errorf("export failed (%d): %s", resp.StatusCode, string(body))
	}

	// Recursive mode: parse as ExportNode
	if exportRecursive {
		var treeResp api.ExportNode
		if err := json.Unmarshal(body, &treeResp); err != nil {
			return fmt.Errorf("failed to parse recursive response: %w", err)
		}

		if treeResp.Error != "" {
			return fmt.Errorf("export failed: %s", treeResp.Error)
		}

		if planFormat == "text" {
			renderTreeText(&treeResp, 0)
			return nil
		}

		// JSON output — pretty-print
		prettyJSON, err := json.MarshalIndent(treeResp, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to format JSON: %w", err)
		}

		if exportOutput != "" {
			if err := os.WriteFile(exportOutput, prettyJSON, 0644); err != nil {
				return fmt.Errorf("failed to write to %s: %w", exportOutput, err)
			}
			fmt.Printf("Exported to %s\n", exportOutput)
			return nil
		}

		fmt.Println(string(prettyJSON))
		return nil
	}

	// Flat mode: parse as PlanExportResponse (unchanged)
	var exportResp api.PlanExportResponse
	if err := json.Unmarshal(body, &exportResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if planFormat == "text" {
		fmt.Printf("Title: %s\n", exportResp.Title)
		fmt.Printf("Type: %s\n", exportResp.Type)
		if exportResp.Status != "" {
			fmt.Printf("Status: %s\n", exportResp.Status)
		}
		if exportResp.RecurrencePattern != "" {
			fmt.Printf("Recurrence: %s\n", exportResp.RecurrencePattern)
		}
		if exportResp.Description != "" {
			fmt.Printf("Description: %s\n", exportResp.Description)
		}
		fmt.Printf("Tasks: %d\n", len(exportResp.Tasks))
		for i, task := range exportResp.Tasks {
			mode := task.ExecutionMode
			if mode == "" {
				mode = "default"
			}
			fmt.Printf("  [%d] %s (%s)\n", i+1, task.Title, mode)
		}
		return nil
	}

	// JSON output — pretty-print
	prettyJSON, err := json.MarshalIndent(exportResp, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to format JSON: %w", err)
	}

	// Write to file or stdout
	if exportOutput != "" {
		if err := os.WriteFile(exportOutput, prettyJSON, 0644); err != nil {
			return fmt.Errorf("failed to write to %s: %w", exportOutput, err)
		}
		fmt.Printf("Exported to %s\n", exportOutput)
		return nil
	}

	fmt.Println(string(prettyJSON))
	return nil
}

// renderTreeText renders a recursive export node as an indented text tree.
func renderTreeText(node *api.ExportNode, indent int) {
	prefix := strings.Repeat("  ", indent)

	// Build the line: TYPE: Title [STATUS]
	line := fmt.Sprintf("%s%s: %s", prefix, node.Type, node.Title)

	// Add execution mode for leaf nodes (no children)
	if len(node.Children) == 0 && node.ExecutionMode != "" {
		line += fmt.Sprintf(" (%s)", node.ExecutionMode)
	}

	// Add child count for nodes with children
	if len(node.Children) > 0 {
		line += fmt.Sprintf(" (%d children)", len(node.Children))
	}

	// Add recurrence for PROCESS nodes
	if node.RecurrencePattern != "" {
		line += fmt.Sprintf(" (%s)", node.RecurrencePattern)
	}

	line += fmt.Sprintf(" [%s]", node.Status)

	// Add ID if present
	if node.ID != "" {
		line += fmt.Sprintf(" {%s}", node.ID)
	}

	fmt.Println(line)

	// Recurse into children
	for i := range node.Children {
		renderTreeText(&node.Children[i], indent+1)
	}
}
