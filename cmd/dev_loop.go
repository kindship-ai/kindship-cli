package cmd

import (
	"fmt"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/kindship-ai/kindship-cli/internal/config"
)

func fetchScopedNextTaskForDevLoop(ctx *auth.Context, cfg *config.RepoConfig) (*api.PlanNextResponse, error) {
	client := api.NewClient(ctx.APIBaseURL, verbose)
	return client.FetchNextTaskScopedWithBearer(cfg.AgentID, cfg.ProcessID, ctx.Token)
}

func findProcessTaskIndex(tasks []api.TaskInfo, entityID string) int {
	for i, task := range tasks {
		if task.ID == entityID {
			return i
		}
	}
	return -1
}

func formatDevLoopWaitMarkdown(pendingCount, cycleCount int) string {
	if pendingCount <= 0 {
		return "No runnable tasks available."
	}
	return fmt.Sprintf(
		"## Kindship Dev Loop\n\nCycle %d is waiting for %d pending task(s). Retry `kindship run local-next` later.\n",
		cycleCount,
		pendingCount,
	)
}

// startDevLoopCycle starts a new ORCHESTRATE run on the process and
// claims the next runnable child task via scoped /plan/next.
// Handles both fresh starts and resumes.
func startDevLoopCycle(ctx *auth.Context, cfg *config.RepoConfig, sessionID string) (*ActiveRun, string, error) {
	client := api.NewClient(ctx.APIBaseURL, verbose)

	// Start process run (resets children + creates ORCHESTRATE run, or resumes existing)
	resp, err := client.StartProcessRunWithBearer(cfg.ProcessID, ctx.Token)
	if err != nil {
		return nil, "", fmt.Errorf("start process run: %w", err)
	}

	if len(resp.Tasks) == 0 {
		return nil, "", fmt.Errorf("process has no tasks")
	}

	nextResp, err := fetchScopedNextTaskForDevLoop(ctx, cfg)
	if err != nil {
		return nil, "", fmt.Errorf("fetch scoped next task: %w", err)
	}

	// Queue drained while ORCHESTRATE run is still RUNNING (resume edge case).
	if nextResp.Task == nil && nextResp.PendingCount == 0 && resp.Resumed {
		return completeAndRestartCycle(ctx, cfg, resp.ProcessRunID, resp.Tasks, resp.RunNumber, sessionID)
	}

	if nextResp.Task == nil {
		return nil, formatDevLoopWaitMarkdown(nextResp.PendingCount, resp.RunNumber), nil
	}

	state, markdown, err := startDevLoopTask(
		ctx,
		cfg,
		resp.ProcessRunID,
		resp.Tasks,
		*nextResp.Task,
		sessionID,
	)
	if err != nil {
		return nil, "", err
	}
	state.CycleCount = resp.RunNumber
	state.Attachments = resp.Attachments
	// Regenerate markdown with cycle count and attachments
	markdown = formatKindshipTaskMarkdown(cfg.AgentSlug, state.Task, state.RunID, state.ExecutionMode, state)
	return state, markdown, nil
}

// completeAndRestartCycle handles the edge case where start-run resumed an
// existing ORCHESTRATE run but all child tasks are already COMPLETED (interrupt
// happened between last task complete and ORCHESTRATE complete).
func completeAndRestartCycle(ctx *auth.Context, cfg *config.RepoConfig, processRunID string, tasks []api.TaskInfo, runNumber int, sessionID string) (*ActiveRun, string, error) {
	client := api.NewClient(ctx.APIBaseURL, verbose)

	orchestrateComplete := api.ExecutionCompleteRequest{
		Status: api.ExecutionAttemptStatusSuccess,
		Outputs: &api.ExecutionOutputs{
			Structured: map[string]interface{}{
				"tasks_executed": len(tasks),
				"cycle_number":   runNumber,
			},
		},
	}
	if _, err := client.CompleteExecutionWithBearer(processRunID, orchestrateComplete, ctx.Token); err != nil {
		return nil, "", fmt.Errorf("complete orphaned process run: %w", err)
	}

	// Now start fresh
	return startDevLoopCycle(ctx, cfg, sessionID)
}

// startDevLoopTask starts execution on a specific scoped task within the process cycle.
// Returns the ActiveRun state and formatted markdown.
func startDevLoopTask(ctx *auth.Context, cfg *config.RepoConfig, processRunID string, tasks []api.TaskInfo, task api.TaskInfo, sessionID string) (*ActiveRun, string, error) {
	taskIndex := findProcessTaskIndex(tasks, task.ID)
	if taskIndex < 0 {
		tasks = append(tasks, task)
		taskIndex = len(tasks) - 1
	}

	client := api.NewClient(ctx.APIBaseURL, verbose)

	if processRunID == "" {
		fmt.Println("⚠ processRunID is empty — parent_run will not be set on this task run")
	}

	// Start execution on this task (idempotent — returns existing RUNNING run)
	startReq := api.ExecutionStartRequest{
		EntityID:      task.ID,
		ExecutionMode: api.ExecutionMode(task.ExecutionMode),
		AgentID:       cfg.AgentID,
		SessionID:     sessionID,
		ParentRun:     processRunID,
	}
	startResp, err := client.StartExecutionWithBearer(startReq, ctx.Token)
	if err != nil {
		return nil, "", fmt.Errorf("start task execution: %w", err)
	}

	state := &ActiveRun{
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
		TaskIndex:     taskIndex,
		TaskCount:     len(tasks),
		ProcessTasks:  tasks,
		Inputs:        startResp.Inputs,
	}

	markdown := formatKindshipTaskMarkdown(cfg.AgentSlug, &task, startResp.ExecutionID, task.ExecutionMode, state)
	return state, markdown, nil
}

// advanceDevLoop completes the current task run and either advances to the
// next task in the cycle or starts a new cycle. Returns (markdown, shouldBlock, error).
// shouldBlock=true means there's a next task to present.
func advanceDevLoop(ctx *auth.Context, cfg *config.RepoConfig, active *ActiveRun, outputs *api.ExecutionOutputs) (string, bool, error) {
	client := api.NewClient(ctx.APIBaseURL, verbose)

	// 1. Complete current task run
	completeReq := api.ExecutionCompleteRequest{
		Status:  api.ExecutionAttemptStatusSuccess,
		Outputs: outputs,
	}
	if _, err := client.CompleteExecutionWithBearer(active.RunID, completeReq, ctx.Token); err != nil {
		return "", false, fmt.Errorf("complete task run: %w", err)
	}

	// 2. Ask the server for the next runnable child task in this process scope.
	nextResp, err := fetchScopedNextTaskForDevLoop(ctx, cfg)
	if err != nil {
		return "", false, fmt.Errorf("fetch scoped next task: %w", err)
	}

	if nextResp.Task != nil {
		state, _, err := startDevLoopTask(
			ctx,
			cfg,
			active.ProcessRunID,
			active.ProcessTasks,
			*nextResp.Task,
			active.SessionID,
		)
		if err != nil {
			return "", false, err
		}
		// Carry cycle count and attachments forward within same cycle
		state.CycleCount = active.CycleCount
		state.Attachments = active.Attachments
		markdown := formatKindshipTaskMarkdown(cfg.AgentSlug, state.Task, state.RunID, state.ExecutionMode, state)
		return markdown, true, nil
	}

	if nextResp.PendingCount > 0 {
		return formatDevLoopWaitMarkdown(nextResp.PendingCount, active.CycleCount), false, nil
	}

	// 3. Cycle queue drained — close ORCHESTRATE run, then start a new cycle.
	// CRITICAL: ORCHESTRATE completion must succeed before starting a new cycle,
	// because DB enforces one RUNNING run per entity (ix_runs_one_running_per_entity).
	// The process has recurrence_pattern set, so complete-execution.ts will NOT mark
	// the entity as COMPLETED — it stays ACTIVE.
	orchestrateComplete := api.ExecutionCompleteRequest{
		Status: api.ExecutionAttemptStatusSuccess,
		Outputs: &api.ExecutionOutputs{
			Structured: map[string]interface{}{
				"tasks_executed": active.TaskCount,
				"cycle_number":   active.CycleCount,
			},
		},
	}
	if _, err := client.CompleteExecutionWithBearer(active.ProcessRunID, orchestrateComplete, ctx.Token); err != nil {
		return "", false, fmt.Errorf("complete process run (required before new cycle): %w", err)
	}

	// Start new cycle
	state, markdown, err := startDevLoopCycle(ctx, cfg, active.SessionID)
	if err != nil {
		return "", false, fmt.Errorf("start new cycle: %w", err)
	}
	if state == nil {
		return markdown, false, nil
	}
	return markdown, true, nil
}

// failDevLoop marks the current task as failed.
// Does NOT advance to the next task — the user must resolve the failure.
func failDevLoop(ctx *auth.Context, active *ActiveRun, reason string) error {
	client := api.NewClient(ctx.APIBaseURL, verbose)

	failReason := reason
	completeReq := api.ExecutionCompleteRequest{
		Status:        api.ExecutionAttemptStatusFailed,
		FailureReason: &failReason,
	}
	if _, err := client.CompleteExecutionWithBearer(active.RunID, completeReq, ctx.Token); err != nil {
		return fmt.Errorf("fail task run: %w", err)
	}

	return nil
}
