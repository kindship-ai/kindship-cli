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
  export   Export a planning entity as JSON
  import   Import entities from an export file`,
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
	Long: `Export a planning entity and its descendants as JSON.

The output is a flat array of entities with UUID references,
suitable for round-trip import via 'plan import'.

Examples:
  kindship plan export <entity-id>
  kindship plan export <entity-id> --format text
  kindship plan export <entity-id> --output plan.json
  kindship plan export <entity-id> --include-deleted`,
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

var planImportCmd = &cobra.Command{
	Use:   "import [file]",
	Short: "Import entities from an export file",
	Long: `Import planning entities from a JSON file or stdin.

The JSON format matches the output of 'plan export'.
Entities with existing UUIDs are updated; new UUIDs are created.

If no file is provided, reads from stdin.

Examples:
  kindship plan import plan.json
  kindship plan export <id> | kindship plan import
  cat plan.json | kindship plan import`,
	RunE: runPlanImport,
}

var (
	planFormat           string
	exportOutput         string
	exportIncludeDeleted bool
	importFormat         string
)

func init() {
	planSubmitCmd.Flags().StringVar(&planFormat, "format", "text", "Output format (json, text)")
	planNextCmd.Flags().StringVar(&planFormat, "format", "json", "Output format (json, text)")

	planExportCmd.Flags().StringVar(&planFormat, "format", "json", "Output format (json, text)")
	planExportCmd.Flags().StringVar(&exportOutput, "output", "", "Write output to file instead of stdout")
	planExportCmd.Flags().BoolVar(&exportIncludeDeleted, "include-deleted", false, "Include DELETED entities")

	planImportCmd.Flags().StringVar(&importFormat, "format", "text", "Output format (json, text)")

	planCmd.AddCommand(planSubmitCmd)
	planCmd.AddCommand(planNextCmd)
	planCmd.AddCommand(planExportCmd)
	planCmd.AddCommand(planImportCmd)
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
	if exportIncludeDeleted {
		endpoint += "&include_deleted=true"
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
		var errObj struct {
			Error string `json:"error,omitempty"`
		}
		if json.Unmarshal(body, &errObj) == nil && errObj.Error != "" {
			return fmt.Errorf("export failed: %s", errObj.Error)
		}
		return fmt.Errorf("export failed (%d): %s", resp.StatusCode, string(body))
	}

	var exportResp api.ExportResponse
	if err := json.Unmarshal(body, &exportResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if exportResp.Error != "" {
		return fmt.Errorf("export failed: %s", exportResp.Error)
	}

	if planFormat == "text" {
		renderFlatAsTree(exportResp.Entities)
		return nil
	}

	// JSON output — pretty-print
	prettyJSON, err := json.MarshalIndent(exportResp, "", "  ")
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

func runPlanImport(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	// Read JSON from file or stdin
	var importData []byte

	if len(args) > 0 {
		importData, err = os.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("failed to read import file: %w", err)
		}
	} else {
		importData, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("failed to read from stdin: %w", err)
		}
	}

	if len(importData) == 0 {
		return fmt.Errorf("no import data provided")
	}

	// Parse as ExportResponse to extract entities
	var exportResp api.ExportResponse
	if err := json.Unmarshal(importData, &exportResp); err != nil {
		return fmt.Errorf("failed to parse import data: %w", err)
	}

	if len(exportResp.Entities) == 0 {
		return fmt.Errorf("no entities found in import data")
	}

	// Build import request: { agent_id, entities }
	importReq := struct {
		AgentID  string             `json:"agent_id"`
		Entities []api.ExportEntity `json:"entities"`
	}{
		AgentID:  agentID,
		Entities: exportResp.Entities,
	}

	jsonData, err := json.Marshal(importReq)
	if err != nil {
		return fmt.Errorf("failed to marshal import request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/cli/plan/import", ctx.APIBaseURL)

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to import plan: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp api.ImportResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("import failed: %s", errResp.Error)
		}
		return fmt.Errorf("import failed (%d): %s", resp.StatusCode, string(body))
	}

	var importResp api.ImportResponse
	if err := json.Unmarshal(body, &importResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if importFormat == "json" {
		return printJSON(importResp)
	}

	// Human-readable output
	fmt.Printf("✓ Imported %d entities (%d created, %d updated)\n",
		len(importResp.Entities), importResp.CreatedCount, importResp.UpdatedCount)
	for _, e := range importResp.Entities {
		if e.Action == "skipped" && e.Error != "" {
			fmt.Printf("  [%s] %s (%s) — %s\n", e.Action, e.Title, e.ID, e.Error)
		} else {
			fmt.Printf("  [%s] %s (%s)\n", e.Action, e.Title, e.ID)
		}
	}

	return nil
}

// renderFlatAsTree reconstructs a tree from the flat entity array and renders it as indented text.
func renderFlatAsTree(entities []api.ExportEntity) {
	if len(entities) == 0 {
		fmt.Println("(empty)")
		return
	}

	// Build parent→children index map
	childrenMap := map[string][]int{} // parentID -> entity indices
	var rootIdx int

	for i := range entities {
		if entities[i].ParentID == nil {
			rootIdx = i
		} else {
			pid := *entities[i].ParentID
			childrenMap[pid] = append(childrenMap[pid], i)
		}
	}

	renderEntityText(&entities[rootIdx], entities, childrenMap, 0)
}

func renderEntityText(entity *api.ExportEntity, all []api.ExportEntity, childrenMap map[string][]int, indent int) {
	prefix := strings.Repeat("  ", indent)

	childIndices := childrenMap[entity.ID]

	// Build the line: TYPE: Title [STATUS]
	line := fmt.Sprintf("%s%s: %s", prefix, entity.Type, entity.Title)

	// Add execution mode for leaf nodes (no children)
	if len(childIndices) == 0 && entity.ExecutionMode != "" {
		line += fmt.Sprintf(" (%s)", entity.ExecutionMode)
	}

	// Add child count for nodes with children
	if len(childIndices) > 0 {
		line += fmt.Sprintf(" (%d children)", len(childIndices))
	}

	// Add recurrence for PROCESS nodes
	if entity.RecurrencePattern != "" {
		line += fmt.Sprintf(" (%s)", entity.RecurrencePattern)
	}

	line += fmt.Sprintf(" [%s]", entity.Status)
	line += fmt.Sprintf(" {%s}", entity.ID)

	fmt.Println(line)

	// Recurse into children
	for _, childIdx := range childIndices {
		renderEntityText(&all[childIdx], all, childrenMap, indent+1)
	}
}
