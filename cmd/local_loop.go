package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/kindship-ai/kindship-cli/internal/config"
)

const (
	activeRunFileName = "active_run.json"
	activeRunVersion  = 1
)

// Treat an active run file as stale after this duration and rotate it aside.
// This is a safety valve for crashes or abandoned sessions, not a "task timeout".
var activeRunStaleAfter = 72 * time.Hour

type ActiveRun struct {
	Version       int           `json:"version"`
	AgentID       string        `json:"agent_id"`
	AgentSlug     string        `json:"agent_slug,omitempty"`
	EntityID      string        `json:"entity_id"`
	RunID         string        `json:"run_id"`
	TaskTitle     string        `json:"task_title,omitempty"`
	ExecutionMode string        `json:"execution_mode,omitempty"`
	StartedAt     time.Time     `json:"started_at"`
	SessionID     string        `json:"session_id,omitempty"`
	Task          *api.TaskInfo `json:"task,omitempty"`

	// Process loop fields (empty = legacy non-process mode)
	ProcessRunID string         `json:"process_run_id,omitempty"`
	TaskIndex    int            `json:"task_index,omitempty"`
	TaskCount    int            `json:"task_count,omitempty"`
	ProcessTasks []api.TaskInfo `json:"process_tasks,omitempty"`
}

// IsProcessLoop returns true if this active run is part of a dev loop process cycle.
func (a *ActiveRun) IsProcessLoop() bool {
	return a.ProcessRunID != ""
}

func activeRunPath(repoRoot string) string {
	return filepath.Join(repoRoot, config.ConfigDir, activeRunFileName)
}

func loadActiveRun(repoRoot string) (*ActiveRun, error) {
	path := activeRunPath(repoRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read active run: %w", err)
	}

	var run ActiveRun
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, fmt.Errorf("parse active run: %w", err)
	}
	if run.Version == 0 {
		run.Version = activeRunVersion
	}
	return &run, nil
}

func saveActiveRun(repoRoot string, run *ActiveRun) error {
	if run == nil {
		return fmt.Errorf("active run is nil")
	}
	if run.Version == 0 {
		run.Version = activeRunVersion
	}

	kindshipDir := filepath.Join(repoRoot, config.ConfigDir)
	if err := os.MkdirAll(kindshipDir, config.ConfigDirMode); err != nil {
		return fmt.Errorf("create %s: %w", kindshipDir, err)
	}

	path := activeRunPath(repoRoot)
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal active run: %w", err)
	}
	// The file may contain task context; keep it owner-readable only.
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write active run: %w", err)
	}
	return nil
}

func clearActiveRun(repoRoot string) error {
	path := activeRunPath(repoRoot)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove active run: %w", err)
	}
	return nil
}

func rotateActiveRunIfStale(repoRoot string, run *ActiveRun) error {
	if run == nil || run.StartedAt.IsZero() {
		return nil
	}
	if time.Since(run.StartedAt) <= activeRunStaleAfter {
		return nil
	}

	src := activeRunPath(repoRoot)
	dst := filepath.Join(repoRoot, config.ConfigDir, fmt.Sprintf("active_run.stale.%s.json", time.Now().UTC().Format("20060102T150405Z")))
	if err := os.Rename(src, dst); err != nil {
		// If we can't rotate, don't block new work; just remove.
		_ = os.Remove(src)
		return nil
	}
	return nil
}

func startLocalExecutionForTask(ctx *auth.Context, repoCfg *config.RepoConfig, task *api.TaskInfo) (*ActiveRun, error) {
	if ctx == nil {
		return nil, fmt.Errorf("auth context is nil")
	}
	if repoCfg == nil || repoCfg.AgentID == "" {
		return nil, fmt.Errorf("repo not configured with an agent (run 'kindship setup')")
	}
	if task == nil {
		return nil, fmt.Errorf("task is nil")
	}

	client := api.NewClient(ctx.APIBaseURL, verbose)
	startReq := api.ExecutionStartRequest{
		EntityID:      task.ID,
		ExecutionMode: api.ExecutionMode(task.ExecutionMode),
		AgentID:       repoCfg.AgentID,
	}

	startResp, err := client.StartExecutionWithBearer(startReq, ctx.Token)
	if err != nil {
		return nil, err
	}

	return &ActiveRun{
		Version:       activeRunVersion,
		AgentID:       repoCfg.AgentID,
		AgentSlug:     repoCfg.AgentSlug,
		EntityID:      task.ID,
		RunID:         startResp.ExecutionID,
		TaskTitle:     task.Title,
		ExecutionMode: task.ExecutionMode,
		StartedAt:     time.Now(),
		Task:          task,
	}, nil
}

func formatKindshipTaskMarkdown(agentSlug string, task *api.TaskInfo, runID string, executionMode string) string {
	if task == nil {
		return "No pending Kindship tasks."
	}

	agentLabel := agentSlug
	if strings.TrimSpace(agentLabel) == "" {
		agentLabel = "unknown"
	}
	mode := executionMode
	if strings.TrimSpace(mode) == "" {
		mode = task.ExecutionMode
	}

	var b strings.Builder
	b.WriteString("## Kindship Task Assignment\n\n")
	b.WriteString(fmt.Sprintf("You are working as Kindship agent **%s**.\n\n", agentLabel))
	b.WriteString(fmt.Sprintf("### Current Task: %s\n", task.Title))
	b.WriteString(fmt.Sprintf("Entity: `%s` | Run: `%s` | Mode: %s\n\n", task.ID, runID, mode))

	if strings.TrimSpace(task.Description) != "" {
		b.WriteString(task.Description)
		b.WriteString("\n\n")
	}

	if strings.TrimSpace(task.Rationale) != "" {
		b.WriteString("### Rationale\n")
		b.WriteString(task.Rationale)
		b.WriteString("\n\n")
	}

	if len(task.SuccessCriteria) > 0 {
		b.WriteString("### Success Criteria\n")
		b.WriteString(formatSuccessCriteriaMarkdown(task.SuccessCriteria))
		b.WriteString("\n\n")
	}

	if task.Code != nil && strings.TrimSpace(*task.Code) != "" {
		switch api.ExecutionMode(task.ExecutionMode) {
		case api.ExecutionModeBash:
			b.WriteString("### Code\n```bash\n")
			b.WriteString(strings.TrimSpace(*task.Code))
			b.WriteString("\n```\n\n")
		case api.ExecutionModePython, api.ExecutionModePythonSandbox:
			b.WriteString("### Code\n```python\n")
			b.WriteString(strings.TrimSpace(*task.Code))
			b.WriteString("\n```\n\n")
		default:
			b.WriteString("### Code\n```text\n")
			b.WriteString(strings.TrimSpace(*task.Code))
			b.WriteString("\n```\n\n")
		}
	}

	// For interactive modes, keep the prompt user-facing.
	switch api.ExecutionMode(task.ExecutionMode) {
	case api.ExecutionModeAskUser, api.ExecutionModeChoice, api.ExecutionModeCallToAction:
		b.WriteString("### Next Step\n")
		b.WriteString("This is an interactive task. Provide the requested information and proceed.\n\n")
	}

	b.WriteString("### Instructions\n")
	b.WriteString("- Work on this task in the current repository\n")
	b.WriteString("- When complete, use `/kindship complete` to report success\n")
	b.WriteString("- If blocked, use `/kindship fail --reason \"...\"`\n")
	b.WriteString("- The user may give different instructions; always follow the user\n")

	return b.String()
}

func formatSuccessCriteriaMarkdown(sc map[string]interface{}) string {
	// Best-effort structured formatting for common shapes, else fall back to JSON.
	var b strings.Builder

	if desc, ok := sc["description"].(string); ok && strings.TrimSpace(desc) != "" {
		b.WriteString(desc)
		b.WriteString("\n")
	}

	if outs, ok := sc["measurable_outcomes"]; ok {
		switch v := outs.(type) {
		case []interface{}:
			for _, item := range v {
				if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
					b.WriteString("- ")
					b.WriteString(s)
					b.WriteString("\n")
				}
			}
		case []string:
			for _, s := range v {
				if strings.TrimSpace(s) == "" {
					continue
				}
				b.WriteString("- ")
				b.WriteString(s)
				b.WriteString("\n")
			}
		}
	}

	out := strings.TrimSpace(b.String())
	if out != "" {
		return out
	}

	raw, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return "_Success criteria provided (unparseable)._"
	}
	return "```json\n" + string(raw) + "\n```"
}

func parseExecutionOutputsJSON(raw string) (*api.ExecutionOutputs, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out api.ExecutionOutputs
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("invalid --outputs JSON: %w", err)
	}
	return &out, nil
}
