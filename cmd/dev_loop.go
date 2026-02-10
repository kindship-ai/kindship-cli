package cmd

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/kindship-ai/kindship-cli/internal/config"
)

// startDevLoopCycle starts a new ORCHESTRATE run on the process and
// begins the first non-COMPLETED task. Handles both fresh starts and resumes.
func startDevLoopCycle(ctx *auth.Context, cfg *config.RepoConfig, repoRoot string) (*ActiveRun, string, error) {
	client := api.NewClient(ctx.APIBaseURL, verbose)

	// Start process run (resets children + creates ORCHESTRATE run, or resumes existing)
	resp, err := client.StartProcessRunWithBearer(cfg.ProcessID, ctx.Token)
	if err != nil {
		return nil, "", fmt.Errorf("start process run: %w", err)
	}

	if len(resp.Tasks) == 0 {
		return nil, "", fmt.Errorf("process has no tasks")
	}

	// Find first non-COMPLETED task (handles resume after interrupt)
	startIndex := 0
	if resp.Resumed {
		allCompleted := true
		for i, t := range resp.Tasks {
			if t.Status != "COMPLETED" {
				startIndex = i
				allCompleted = false
				break
			}
		}
		// All tasks COMPLETED but ORCHESTRATE run still RUNNING — complete it and restart
		if allCompleted {
			return completeAndRestartCycle(ctx, cfg, repoRoot, resp.ProcessRunID, resp.Tasks)
		}
	}

	return startDevLoopTask(ctx, cfg, repoRoot, resp.ProcessRunID, resp.Tasks, startIndex, "")
}

// completeAndRestartCycle handles the edge case where start-run resumed an
// existing ORCHESTRATE run but all child tasks are already COMPLETED (interrupt
// happened between last task complete and ORCHESTRATE complete).
func completeAndRestartCycle(ctx *auth.Context, cfg *config.RepoConfig, repoRoot string, processRunID string, tasks []api.TaskInfo) (*ActiveRun, string, error) {
	client := api.NewClient(ctx.APIBaseURL, verbose)

	orchestrateComplete := api.ExecutionCompleteRequest{
		Status: api.ExecutionAttemptStatusSuccess,
		Outputs: &api.ExecutionOutputs{
			Structured: map[string]interface{}{
				"tasks_executed": len(tasks),
			},
		},
	}
	if _, err := client.CompleteExecutionWithBearer(processRunID, orchestrateComplete, ctx.Token); err != nil {
		return nil, "", fmt.Errorf("complete orphaned process run: %w", err)
	}

	// Now start fresh
	return startDevLoopCycle(ctx, cfg, repoRoot)
}

// startDevLoopTask starts execution on a specific task within the process cycle.
// Saves state to active_run.json and returns formatted markdown.
func startDevLoopTask(ctx *auth.Context, cfg *config.RepoConfig, repoRoot string, processRunID string, tasks []api.TaskInfo, index int, sessionID string) (*ActiveRun, string, error) {
	if index < 0 || index >= len(tasks) {
		return nil, "", fmt.Errorf("task index %d out of range (0..%d)", index, len(tasks)-1)
	}

	task := tasks[index]
	client := api.NewClient(ctx.APIBaseURL, verbose)

	// Start execution on this task (idempotent — returns existing RUNNING run)
	startReq := api.ExecutionStartRequest{
		EntityID:      task.ID,
		ExecutionMode: api.ExecutionMode(task.ExecutionMode),
		AgentID:       cfg.AgentID,
	}
	startResp, err := client.StartExecutionWithBearer(startReq, ctx.Token)
	if err != nil {
		return nil, "", fmt.Errorf("start task execution: %w", err)
	}

	state := &ActiveRun{
		Version:       activeRunVersion,
		AgentID:       cfg.AgentID,
		AgentSlug:     cfg.AgentSlug,
		EntityID:      task.ID,
		RunID:         startResp.ExecutionID,
		TaskTitle:     task.Title,
		ExecutionMode: task.ExecutionMode,
		StartedAt:     time.Now(),
		SessionID:     sessionID,
		Task:          &task,
		ProcessRunID:  processRunID,
		TaskIndex:     index,
		TaskCount:     len(tasks),
		ProcessTasks:  tasks,
		Inputs:        startResp.Inputs,
	}

	if err := saveActiveRun(repoRoot, state); err != nil {
		return nil, "", fmt.Errorf("save state: %w", err)
	}

	markdown := formatKindshipTaskMarkdown(cfg.AgentSlug, &task, startResp.ExecutionID, task.ExecutionMode, state)
	return state, markdown, nil
}

// advanceDevLoop completes the current task run and either advances to the
// next task in the cycle or starts a new cycle. Returns (markdown, shouldBlock, error).
// shouldBlock=true means there's a next task to present.
func advanceDevLoop(ctx *auth.Context, cfg *config.RepoConfig, repoRoot string, active *ActiveRun, outputs *api.ExecutionOutputs) (string, bool, error) {
	client := api.NewClient(ctx.APIBaseURL, verbose)

	// 1. Complete current task run
	completeReq := api.ExecutionCompleteRequest{
		Status:  api.ExecutionAttemptStatusSuccess,
		Outputs: outputs,
	}
	if _, err := client.CompleteExecutionWithBearer(active.RunID, completeReq, ctx.Token); err != nil {
		return "", false, fmt.Errorf("complete task run: %w", err)
	}

	nextIndex := active.TaskIndex + 1

	// 2a. More tasks in current cycle
	if nextIndex < active.TaskCount {
		state, _, err := startDevLoopTask(ctx, cfg, repoRoot, active.ProcessRunID, active.ProcessTasks, nextIndex, active.SessionID)
		if err != nil {
			return "", false, err
		}
		// Carry cycle count forward within same cycle
		state.CycleCount = active.CycleCount
		if err := saveActiveRun(repoRoot, state); err != nil {
			return "", false, err
		}
		markdown := formatKindshipTaskMarkdown(cfg.AgentSlug, state.Task, state.RunID, state.ExecutionMode, state)
		return markdown, true, nil
	}

	// 2b. Cycle complete — close ORCHESTRATE run, then start new cycle.
	// CRITICAL: ORCHESTRATE completion must succeed before starting a new cycle,
	// because DB enforces one RUNNING run per entity (ix_runs_one_running_per_entity).
	// The process has recurrence_pattern set, so complete-execution.ts will NOT mark
	// the entity as COMPLETED — it stays ACTIVE.
	orchestrateComplete := api.ExecutionCompleteRequest{
		Status: api.ExecutionAttemptStatusSuccess,
		Outputs: &api.ExecutionOutputs{
			Structured: map[string]interface{}{
				"tasks_executed": active.TaskCount,
			},
		},
	}
	if _, err := client.CompleteExecutionWithBearer(active.ProcessRunID, orchestrateComplete, ctx.Token); err != nil {
		return "", false, fmt.Errorf("complete process run (required before new cycle): %w", err)
	}

	// Start new cycle
	state, _, err := startDevLoopCycle(ctx, cfg, repoRoot)
	if err != nil {
		return "", false, fmt.Errorf("start new cycle: %w", err)
	}
	state.SessionID = active.SessionID
	state.CycleCount = active.CycleCount + 1
	if err := saveActiveRun(repoRoot, state); err != nil {
		return "", false, err
	}
	// Regenerate markdown with correct cycle count
	markdown := formatKindshipTaskMarkdown(cfg.AgentSlug, state.Task, state.RunID, state.ExecutionMode, state)
	return markdown, true, nil
}

// failDevLoop marks the current task as failed and clears state.
// Does NOT advance to the next task — the user must resolve the failure.
func failDevLoop(ctx *auth.Context, repoRoot string, active *ActiveRun, reason string) error {
	client := api.NewClient(ctx.APIBaseURL, verbose)

	failReason := reason
	completeReq := api.ExecutionCompleteRequest{
		Status:        api.ExecutionAttemptStatusFailed,
		FailureReason: &failReason,
	}
	if _, err := client.CompleteExecutionWithBearer(active.RunID, completeReq, ctx.Token); err != nil {
		return fmt.Errorf("fail task run: %w", err)
	}

	return clearActiveRun(repoRoot)
}

// enrichOutputsForTask adds task-specific structured data to execution outputs.
func enrichOutputsForTask(active *ActiveRun, outputs *api.ExecutionOutputs, repoRoot string) {
	if outputs == nil || outputs.Structured == nil {
		return
	}

	switch active.TaskIndex {
	case 0: // Decide
		// Parse summary for structured plan data
		if summary, ok := outputs.Structured["summary"].(string); ok && summary != "" {
			outputs.Structured["task_type"] = "decide"
			// Best-effort: leave summary as-is for the next step
		}

	case 1: // Build
		// Capture git commit info
		outputs.Structured["task_type"] = "build"
		if sha, msg, stat, err := getLastCommitInfo(repoRoot); err == nil {
			outputs.Structured["commit_sha"] = sha
			outputs.Structured["commit_message"] = msg
			outputs.Structured["diff_stat"] = stat
		}
		// Check for uncommitted changes
		if hasUncommitted(repoRoot) {
			outputs.Structured["commit_warning"] = "uncommitted changes detected"
		}

	case 2: // Validate
		outputs.Structured["task_type"] = "validate"
	}
}

func getLastCommitInfo(repoRoot string) (sha, message, diffStat string, err error) {
	// Get SHA
	shaOut, err := execGit(repoRoot, "log", "-1", "--format=%H")
	if err != nil {
		return "", "", "", err
	}
	sha = strings.TrimSpace(shaOut)

	// Get message
	msgOut, err := execGit(repoRoot, "log", "-1", "--format=%s")
	if err != nil {
		return sha, "", "", nil
	}
	message = strings.TrimSpace(msgOut)

	// Get diff stat
	statOut, err := execGit(repoRoot, "diff", "--stat", "HEAD~1")
	if err != nil {
		return sha, message, "", nil
	}
	diffStat = strings.TrimSpace(statOut)

	return sha, message, diffStat, nil
}

func hasUncommitted(repoRoot string) bool {
	out, err := execGit(repoRoot, "status", "--porcelain")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != ""
}

func execGit(repoRoot string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repoRoot}, args...)...)
	out, err := cmd.Output()
	return string(out), err
}
