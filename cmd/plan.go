package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
  next     Get the next executable task`,
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
	planFormat string
)

func init() {
	planSubmitCmd.Flags().StringVar(&planFormat, "format", "text", "Output format (json, text)")
	planNextCmd.Flags().StringVar(&planFormat, "format", "json", "Output format (json, text)")

	planCmd.AddCommand(planSubmitCmd)
	planCmd.AddCommand(planNextCmd)
	rootCmd.AddCommand(planCmd)
}

// PlanSubmitRequest is the request body for plan submission
type PlanSubmitRequest struct {
	AgentID       string     `json:"agent_id"`
	Title         string     `json:"title"`
	Description   string     `json:"description"`
	Tasks         []TaskSpec `json:"tasks"`
	Type          string     `json:"type,omitempty"`
	SkipBootstrap bool       `json:"skip_bootstrap,omitempty"`
}

// TaskSpec represents a task in the plan
type TaskSpec struct {
	Title               string                 `json:"title"`
	Description         string                 `json:"description,omitempty"`
	SequenceOrder       int                    `json:"sequence_order,omitempty"`
	ExecutionMode       string                 `json:"execution_mode,omitempty"`
	Code                string                 `json:"code,omitempty"`
	DependenciesLabeled map[string]string      `json:"dependencies_labeled,omitempty"`
	InputSchema         map[string]interface{} `json:"input_schema,omitempty"`
	OutputSchema        map[string]interface{} `json:"output_schema,omitempty"`
	SuccessCriteria     *api.SuccessCriteria   `json:"success_criteria,omitempty"`
	Boundaries          map[string]interface{} `json:"boundaries,omitempty"`
}

// PlanSubmitResponse is the response from plan submission
type PlanSubmitResponse struct {
	Success     bool `json:"success"`
	Project     struct {
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
		Title         string     `json:"title"`
		Description   string     `json:"description"`
		Tasks         []TaskSpec `json:"tasks"`
		Type          string     `json:"type,omitempty"`
		SkipBootstrap bool       `json:"skip_bootstrap,omitempty"`
	}

	if err := json.Unmarshal(planData, &plan); err != nil {
		return fmt.Errorf("failed to parse plan: %w", err)
	}

	// Build request
	reqBody := PlanSubmitRequest{
		AgentID:       agentID,
		Title:         plan.Title,
		Description:   plan.Description,
		Tasks:         plan.Tasks,
		Type:          plan.Type,
		SkipBootstrap: plan.SkipBootstrap,
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
