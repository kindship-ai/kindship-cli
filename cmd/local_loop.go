package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/kindship-ai/kindship-cli/internal/config"
)

type ActiveRun struct {
	AgentID       string        `json:"agent_id"`
	AgentSlug     string        `json:"agent_slug,omitempty"`
	EntityID      string        `json:"entity_id"`
	RunID         string        `json:"run_id"`
	TaskTitle     string        `json:"task_title,omitempty"`
	ExecutionMode string        `json:"execution_mode,omitempty"`
	StartedAt     time.Time     `json:"started_at"`
	SessionID     string        `json:"session_id,omitempty"`
	Task          *api.TaskInfo `json:"task,omitempty"`

	// Process loop fields (empty = non-process mode)
	ProcessRunID string         `json:"process_run_id,omitempty"`
	TaskIndex    int            `json:"task_index,omitempty"`
	TaskCount    int            `json:"task_count,omitempty"`
	ProcessTasks []api.TaskInfo `json:"process_tasks,omitempty"`

	// Context from API (inputs from previous step outputs)
	Inputs map[string]interface{} `json:"inputs,omitempty"`

	// Cycle tracking
	CycleCount int `json:"cycle_count,omitempty"`
}

// IsProcessLoop returns true if this active run is part of a dev loop process cycle.
func (a *ActiveRun) IsProcessLoop() bool {
	return a.ProcessRunID != ""
}

// fetchActiveRun queries the API for the currently active (RUNNING) task run.
// Returns nil, nil when there is no active run. Replaces the old file-based loadActiveRun.
func fetchActiveRun(ctx *auth.Context, cfg *config.RepoConfig) (*ActiveRun, error) {
	if ctx == nil {
		return nil, fmt.Errorf("auth context is nil")
	}
	if cfg == nil || cfg.AgentID == "" {
		return nil, fmt.Errorf("repo not configured with an agent")
	}

	client := api.NewClient(ctx.APIBaseURL, verbose)
	resp, err := client.FetchActiveRunWithBearer(cfg.AgentID, cfg.ProcessID, ctx.Token)
	if err != nil {
		return nil, fmt.Errorf("fetch active run: %w", err)
	}
	if resp.ActiveRun == nil {
		return nil, nil
	}

	d := resp.ActiveRun

	// Parse started_at from API
	startedAt, _ := time.Parse(time.RFC3339, d.StartedAt)

	// Map API TaskInfo
	var task *api.TaskInfo
	if d.Task != nil {
		task = d.Task
	}

	// Map process tasks
	var processTasks []api.TaskInfo
	if len(d.ProcessTasks) > 0 {
		processTasks = d.ProcessTasks
	}

	return &ActiveRun{
		AgentID:       d.AgentID,
		AgentSlug:     d.AgentSlug,
		EntityID:      d.EntityID,
		RunID:         d.RunID,
		TaskTitle:     d.TaskTitle,
		ExecutionMode: d.ExecutionMode,
		StartedAt:     startedAt,
		SessionID:     d.SessionID,
		Task:          task,
		ProcessRunID:  d.ProcessRunID,
		TaskIndex:     d.TaskIndex,
		TaskCount:     d.TaskCount,
		ProcessTasks:  processTasks,
		Inputs:        d.Inputs,
		CycleCount:    d.CycleCount,
	}, nil
}

func startLocalExecutionForTask(ctx *auth.Context, repoCfg *config.RepoConfig, task *api.TaskInfo, sessionID string) (*ActiveRun, error) {
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
		SessionID:     sessionID,
	}

	startResp, err := client.StartExecutionWithBearer(startReq, ctx.Token)
	if err != nil {
		return nil, err
	}

	return &ActiveRun{
		AgentID:       repoCfg.AgentID,
		AgentSlug:     repoCfg.AgentSlug,
		EntityID:      task.ID,
		RunID:         startResp.ExecutionID,
		TaskTitle:     task.Title,
		ExecutionMode: task.ExecutionMode,
		StartedAt:     time.Now(),
		SessionID:     sessionID,
		Task:          task,
	}, nil
}

func formatKindshipTaskMarkdown(agentSlug string, task *api.TaskInfo, runID string, executionMode string, active *ActiveRun) string {
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

	// Cycle and step info (WS2, WS5)
	if active != nil && active.IsProcessLoop() {
		b.WriteString(fmt.Sprintf("### Cycle %d — %s (Step %d of %d)\n", active.CycleCount, task.Title, active.TaskIndex+1, active.TaskCount))
		// Progress indicator
		steps := make([]string, len(active.ProcessTasks))
		for i, t := range active.ProcessTasks {
			steps[i] = t.Title
		}
		var progress strings.Builder
		progress.WriteString("Progress: ")
		for i, step := range steps {
			if i < active.TaskIndex {
				progress.WriteString(fmt.Sprintf("[x] %s  ", step))
			} else if i == active.TaskIndex {
				progress.WriteString(fmt.Sprintf("[>] %s  ", step))
			} else {
				progress.WriteString(fmt.Sprintf("[ ] %s  ", step))
			}
		}
		b.WriteString(progress.String())
		b.WriteString("\n")
	} else {
		b.WriteString(fmt.Sprintf("### Current Task: %s\n", task.Title))
	}

	b.WriteString(fmt.Sprintf("Entity: `%s` | Run: `%s` | Mode: %s\n\n", task.ID, runID, mode))

	if strings.TrimSpace(task.Description) != "" {
		b.WriteString(task.Description)
		b.WriteString("\n\n")
	}

	// Context from previous step (WS1)
	if active != nil && active.Inputs != nil {
		if prevData, ok := active.Inputs["prev"]; ok {
			b.WriteString("### Context from Previous Step\n")
			if prevMap, ok := prevData.(map[string]interface{}); ok {
				// Try to render each key nicely
				for key, val := range prevMap {
					label := key
					// Try to find matching task title from ProcessTasks
					if active.TaskIndex > 0 && active.TaskIndex-1 < len(active.ProcessTasks) {
						label = active.ProcessTasks[active.TaskIndex-1].Title
					}
					b.WriteString(fmt.Sprintf("**%s**:\n", label))
					switch v := val.(type) {
					case map[string]interface{}:
						if raw, err := json.MarshalIndent(v, "", "  "); err == nil {
							b.WriteString("```json\n")
							b.WriteString(string(raw))
							b.WriteString("\n```\n")
						}
					case string:
						b.WriteString(v)
						b.WriteString("\n")
					default:
						if raw, err := json.MarshalIndent(val, "", "  "); err == nil {
							b.WriteString("```json\n")
							b.WriteString(string(raw))
							b.WriteString("\n```\n")
						}
					}
				}
			} else {
				// Render as JSON block
				if raw, err := json.MarshalIndent(prevData, "", "  "); err == nil {
					b.WriteString("```json\n")
					b.WriteString(string(raw))
					b.WriteString("\n```\n")
				}
			}
			b.WriteString("\n")
		}
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

	// Boundaries (WS5)
	if len(task.Boundaries) > 0 {
		b.WriteString("### Boundaries\n")
		if scope, ok := task.Boundaries["scope_boundaries"]; ok {
			if items, ok := scope.([]interface{}); ok {
				for _, item := range items {
					if s, ok := item.(string); ok {
						b.WriteString(fmt.Sprintf("- %s\n", s))
					}
				}
			}
		}
		if limits, ok := task.Boundaries["resource_limits"]; ok {
			if limMap, ok := limits.(map[string]interface{}); ok {
				for k, v := range limMap {
					b.WriteString(fmt.Sprintf("- %s: %v\n", k, v))
				}
			}
		}
		b.WriteString("\n")
	}

	// Output schema (WS5)
	if len(task.OutputSchema) > 0 {
		b.WriteString("### Expected Output Schema\n")
		if raw, err := json.MarshalIndent(task.OutputSchema, "", "  "); err == nil {
			b.WriteString("```json\n")
			b.WriteString(string(raw))
			b.WriteString("\n```\n\n")
		}
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
