package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/kindship-ai/kindship-cli/internal/config"

	"github.com/spf13/cobra"
)

var hookCmd = &cobra.Command{
	Use:    "hook",
	Short:  "Claude Code hook handlers (internal)",
	Long:   `Internal commands for Claude Code hook integration. These are called by Claude Code hooks, not directly by users.`,
	Hidden: true,
}

var hookStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Session start hook handler",
	Long:  `Called by Claude Code at session start. Returns context about the current agent and any pending tasks.`,
	RunE:  runHookStart,
}

var hookStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Session stop hook handler",
	Long:  `Called by Claude Code at session end. Records session summary and outputs.`,
	RunE:  runHookStop,
}

var (
	hookSummaryFile string
)

func init() {
	hookStopCmd.Flags().StringVar(&hookSummaryFile, "summary-file", "", "Path to session summary file")

	hookCmd.AddCommand(hookStartCmd)
	hookCmd.AddCommand(hookStopCmd)
	rootCmd.AddCommand(hookCmd)
}

// HookStartOutput is the JSON output for hook start
type HookStartOutput struct {
	Version     int            `json:"version"`
	Agent       *HookAgentInfo `json:"agent,omitempty"`
	CurrentTask *HookTaskInfo  `json:"current_task,omitempty"`
	Context     string         `json:"context,omitempty"`
	Error       string         `json:"error,omitempty"`
}

// HookAgentInfo represents agent info in hook output
type HookAgentInfo struct {
	ID    string `json:"id"`
	Slug  string `json:"slug,omitempty"`
	Title string `json:"title,omitempty"`
}

// HookTaskInfo represents task info in hook output
type HookTaskInfo struct {
	ID              string                 `json:"id"`
	Title           string                 `json:"title"`
	Description     string                 `json:"description"`
	SuccessCriteria map[string]interface{} `json:"success_criteria,omitempty"`
}

func runHookStart(cmd *cobra.Command, args []string) error {
	output := HookStartOutput{Version: 1}

	// Check hook version
	hookVersion := os.Getenv("KINDSHIP_HOOK_VERSION")
	if hookVersion != "" && hookVersion != "1" {
		output.Error = fmt.Sprintf("unsupported hook version: %s", hookVersion)
		return printJSON(output)
	}

	// Get auth context (may fail if not authenticated)
	ctx := auth.GetAuthContextOrNil()
	if ctx == nil {
		output.Context = "Not authenticated. Run 'kindship login' to authenticate."
		return printJSON(output)
	}

	// Get repo config if available
	repoConfig, err := config.LoadRepoConfig()
	if err != nil {
		output.Context = "No agent configured for this repository. Run 'kindship setup' to link an agent."
		return printJSON(output)
	}

	// Set agent info
	output.Agent = &HookAgentInfo{
		ID:   repoConfig.AgentID,
		Slug: repoConfig.AgentSlug,
	}

	// Try to get next task
	task, err := fetchNextTask(ctx, repoConfig.AgentID)
	if err != nil {
		output.Context = fmt.Sprintf("Could not fetch current task: %v", err)
	} else if task != nil {
		output.CurrentTask = &HookTaskInfo{
			ID:              task.ID,
			Title:           task.Title,
			Description:     task.Description,
			SuccessCriteria: task.SuccessCriteria,
		}
		output.Context = fmt.Sprintf("Current task: %s\n\nUse '/kindship next' to get task details or '/kindship complete' when done.", task.Title)
	} else {
		output.Context = "No pending tasks. Use '/kindship plan submit' to create tasks."
	}

	return printJSON(output)
}

func runHookStop(cmd *cobra.Command, args []string) error {
	// Hook stop is called with summary file
	if hookSummaryFile == "" {
		// No summary file provided, just acknowledge
		fmt.Println("Session ended.")
		return nil
	}

	// Read summary file
	summaryData, err := os.ReadFile(hookSummaryFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Could not read summary file: %v\n", err)
		return nil
	}

	// Parse summary
	var summary struct {
		SessionID     string   `json:"session_id"`
		Summary       string   `json:"summary"`
		FilesModified []string `json:"files_modified"`
	}

	if err := json.Unmarshal(summaryData, &summary); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Could not parse summary file: %v\n", err)
		return nil
	}

	// Log the summary (future: send to API for tracking)
	fmt.Printf("Session %s ended.\n", summary.SessionID)
	if len(summary.FilesModified) > 0 {
		fmt.Printf("Modified %d file(s)\n", len(summary.FilesModified))
	}

	return nil
}

func fetchNextTask(ctx *auth.Context, agentID string) (*TaskInfo, error) {
	endpoint := fmt.Sprintf("%s/api/cli/plan/next?agent_id=%s", ctx.APIBaseURL, agentID)

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", ctx.GetAuthHeader())
	req.Header.Set("X-Kindship-CLI-Version", Version)
	req.Header.Set("X-Kindship-Hook-Version", "1")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var nextResp PlanNextResponse
	if err := json.NewDecoder(resp.Body).Decode(&nextResp); err != nil {
		return nil, err
	}

	return nextResp.Task, nil
}
