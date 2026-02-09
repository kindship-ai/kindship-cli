package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
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
	hookSummaryFile  string
	hookAutoContinue bool
)

func init() {
	hookStopCmd.Flags().StringVar(&hookSummaryFile, "summary-file", "", "Path to session summary file")
	hookStopCmd.Flags().BoolVar(&hookAutoContinue, "auto-continue", false, "Auto-complete current run and fetch/claim next task")

	hookCmd.AddCommand(hookStartCmd)
	hookCmd.AddCommand(hookStopCmd)
	rootCmd.AddCommand(hookCmd)
}

func runHookStart(cmd *cobra.Command, args []string) error {
	stdinData, _ := io.ReadAll(os.Stdin)
	sessionID := extractJSONString(stdinData, "session_id")

	// Get auth context (may fail if not authenticated)
	ctx := auth.GetAuthContextOrNil()
	if ctx == nil || !ctx.IsLocalMode() {
		fmt.Println("Not authenticated (local mode). Run `kindship login` to authenticate.")
		return nil
	}

	// Get repo config if available
	repoConfig, err := config.LoadRepoConfig()
	if err != nil {
		fmt.Println("No agent configured for this repository. Run `kindship setup` to link an agent.")
		return nil
	}

	repoRoot, err := config.FindRepoRoot()
	if err != nil {
		fmt.Println("Not in a git repository.")
		return nil
	}

	// Resume existing local run if present, unless stale.
	active, err := loadActiveRun(repoRoot)
	if err == nil && active != nil {
		_ = rotateActiveRunIfStale(repoRoot, active)
		active, _ = loadActiveRun(repoRoot)
	}
	if active != nil {
		if active.Task != nil {
			fmt.Print(formatKindshipTaskMarkdown(repoConfig.AgentSlug, active.Task, active.RunID, active.ExecutionMode))
			return nil
		}
		fmt.Printf("Active Kindship run detected.\nEntity: %s | Run: %s | Mode: %s\n\nUse `/kindship complete` or `/kindship fail --reason \"...\"`.\n", active.EntityID, active.RunID, active.ExecutionMode)
		return nil
	}

	// Claim next task.
	task, err := fetchNextTask(ctx, repoConfig.AgentID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not fetch next task: %v\n", err)
		return nil
	}
	if task == nil {
		fmt.Println("No pending Kindship tasks.")
		return nil
	}

	run, err := startLocalExecutionForTask(ctx, repoConfig, task)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not start execution: %v\n", err)
		return nil
	}
	run.SessionID = sessionID
	if err := saveActiveRun(repoRoot, run); err != nil {
		fmt.Fprintf(os.Stderr, "Could not persist active run: %v\n", err)
		// Still output the assignment; user can proceed manually.
	}

	fmt.Print(formatKindshipTaskMarkdown(repoConfig.AgentSlug, task, run.RunID, run.ExecutionMode))
	return nil
}

func runHookStop(cmd *cobra.Command, args []string) error {
	stdinData, _ := io.ReadAll(os.Stdin)
	stopInput := parseHookStopInput(stdinData)

	// Legacy: if a summary file is explicitly provided, prefer it.
	if hookSummaryFile != "" {
		if legacy, err := readLegacySummaryFile(hookSummaryFile); err == nil {
			if stopInput.SessionID == "" {
				stopInput.SessionID = legacy.SessionID
			}
			if stopInput.Summary == "" {
				stopInput.Summary = legacy.Summary
			}
			if len(stopInput.FilesModified) == 0 {
				stopInput.FilesModified = legacy.FilesModified
			}
		} else {
			fmt.Fprintf(os.Stderr, "Warning: Could not read summary file: %v\n", err)
		}
	}

	ctx := auth.GetAuthContextOrNil()
	if ctx == nil || !ctx.IsLocalMode() {
		// In local dev loop, we only support bearer token auth.
		return nil
	}

	repoRoot, err := config.FindRepoRoot()
	if err != nil {
		return nil
	}
	repoCfg, err := config.LoadRepoConfig()
	if err != nil {
		return nil
	}

	stopDetected := transcriptRequestsStop(stopInput.TranscriptPath)

	// Auto-complete current run if we have one.
	active, err := loadActiveRun(repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Could not read active run: %v\n", err)
	} else if active != nil && active.RunID != "" {
		outputs := &api.ExecutionOutputs{
			Stdout: stopInput.Summary,
			Structured: map[string]interface{}{
				"session_id":      stopInput.SessionID,
				"files_modified":  stopInput.FilesModified,
				"transcript_path": stopInput.TranscriptPath,
				"summary":         stopInput.Summary,
			},
		}
		completeReq := api.ExecutionCompleteRequest{
			Status:  api.ExecutionAttemptStatusSuccess,
			Outputs: outputs,
		}

		client := api.NewClient(ctx.APIBaseURL, verbose)
		if _, err := client.CompleteExecutionWithBearer(active.RunID, completeReq, ctx.Token); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Could not complete execution %s: %v\n", active.RunID, err)
		} else if err := clearActiveRun(repoRoot); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Could not clear active run: %v\n", err)
		}
	}

	// Respect "stop" instructions from the user, and don't auto-continue.
	if stopDetected || !hookAutoContinue {
		return nil
	}

	// Fetch and claim next task, then block Claude to continue the loop.
	next, err := fetchNextTask(ctx, repoCfg.AgentID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Could not fetch next task: %v\n", err)
		return nil
	}
	if next == nil {
		return nil
	}

	// Guard against infinite loops where completion didn't take effect.
	if shouldBail, _ := autoContinueGuard(repoRoot, next.ID); shouldBail {
		fmt.Fprintln(os.Stderr, "Warning: next task repeated 3 times; stopping auto-continue loop.")
		return nil
	}

	run, err := startLocalExecutionForTask(ctx, repoCfg, next)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Could not start next execution: %v\n", err)
		return nil
	}
	run.SessionID = stopInput.SessionID
	if err := saveActiveRun(repoRoot, run); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Could not persist active run: %v\n", err)
	}

	decision := struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}{
		Decision: "block",
		Reason:   formatKindshipTaskMarkdown(repoCfg.AgentSlug, next, run.RunID, run.ExecutionMode),
	}
	return printJSON(decision)
}

func fetchNextTask(ctx *auth.Context, agentID string) (*api.TaskInfo, error) {
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

	var nextResp api.PlanNextResponse
	if err := json.NewDecoder(resp.Body).Decode(&nextResp); err != nil {
		return nil, err
	}

	return nextResp.Task, nil
}

type hookStopInput struct {
	SessionID      string
	Summary        string
	FilesModified  []string
	TranscriptPath string
}

func parseHookStopInput(stdin []byte) hookStopInput {
	in := hookStopInput{}
	if len(stdin) == 0 {
		return in
	}
	var m map[string]interface{}
	if err := json.Unmarshal(stdin, &m); err != nil {
		return in
	}

	// Best-effort extraction; Claude hook payload shape can vary.
	in.SessionID = toString(m["session_id"])
	in.Summary = toString(m["summary"])
	in.TranscriptPath = toString(m["transcript_path"])

	if fm, ok := m["files_modified"]; ok {
		switch v := fm.(type) {
		case []interface{}:
			for _, item := range v {
				if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
					in.FilesModified = append(in.FilesModified, s)
				}
			}
		case []string:
			in.FilesModified = append(in.FilesModified, v...)
		}
	}

	// Some hook payloads nest under "output" or "summary".
	if in.Summary == "" {
		if out, ok := m["output"].(map[string]interface{}); ok {
			in.Summary = toString(out["summary"])
		}
	}

	return in
}

type legacySummary struct {
	SessionID     string   `json:"session_id"`
	Summary       string   `json:"summary"`
	FilesModified []string `json:"files_modified"`
}

func readLegacySummaryFile(path string) (*legacySummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s legacySummary
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func extractJSONString(stdin []byte, key string) string {
	var m map[string]interface{}
	if err := json.Unmarshal(stdin, &m); err != nil {
		return ""
	}
	return toString(m[key])
}

func toString(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func transcriptRequestsStop(transcriptPath string) bool {
	if strings.TrimSpace(transcriptPath) == "" {
		return false
	}
	data, err := os.ReadFile(transcriptPath)
	if err != nil || len(data) == 0 {
		return false
	}
	// Only scan the tail to reduce false positives from earlier context.
	const tail = 8192
	if len(data) > tail {
		data = data[len(data)-tail:]
	}
	s := strings.ToLower(string(data))
	return strings.Contains(s, "\nstop\n") ||
		strings.Contains(s, "\nstop.") ||
		strings.Contains(s, "\nstop!") ||
		strings.Contains(s, "/stop") ||
		strings.Contains(s, "stop the loop") ||
		strings.Contains(s, "please stop")
}

type autoContinueState struct {
	LastTaskID string `json:"last_task_id"`
	Count      int    `json:"count"`
}

func autoContinueGuard(repoRoot string, nextTaskID string) (bool, error) {
	if strings.TrimSpace(nextTaskID) == "" {
		return false, nil
	}
	path := filepath.Join(repoRoot, config.ConfigDir, "auto_continue_guard.json")

	var st autoContinueState
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &st)
	}

	if st.LastTaskID == nextTaskID {
		st.Count++
	} else {
		st.LastTaskID = nextTaskID
		st.Count = 1
	}

	if err := os.MkdirAll(filepath.Join(repoRoot, config.ConfigDir), config.ConfigDirMode); err == nil {
		if data, err := json.MarshalIndent(st, "", "  "); err == nil {
			_ = os.WriteFile(path, data, 0600)
		}
	}

	return st.Count >= 3, nil
}
