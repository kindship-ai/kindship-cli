package executor

import (
	"bytes"
	"fmt"
	"os/exec"
	"sync/atomic"

	"github.com/kindship-ai/kindship-cli/internal/api"
)

// ExecuteAgent executes a planning entity using Claude Code with the full
// agent system prompt injected via --append-system-prompt.
// Fails if the system prompt cannot be fetched.
func ExecuteAgent(entity *api.PlanningEntity, inputs map[string]interface{}, client *api.Client, serviceKey string) *ExecutionResult {
	// 1. Fetch system prompt from API
	systemPrompt, err := client.FetchSystemPrompt(entity.AgentID, serviceKey)
	if err != nil {
		return &ExecutionResult{
			Success:  false,
			ExitCode: 1,
			Error:    fmt.Errorf("failed to fetch system prompt: %w", err),
		}
	}

	// 2. Build task prompt (reuse existing buildPrompt)
	taskPrompt := buildPrompt(entity, inputs)

	// 3. Execute with --dangerously-skip-permissions + --append-system-prompt
	cmd := exec.Command("kindship", "auth", "claude",
		"--dangerously-skip-permissions",
		"--append-system-prompt", systemPrompt,
		"-p", taskPrompt,
	)
	cmd.Dir = "/workspace"

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	execErr := cmd.Run()
	exitCode := 0
	if execErr != nil {
		if exitError, ok := execErr.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			exitCode = 1
		}
	}

	return &ExecutionResult{
		Success:  exitCode == 0,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Error:    execErr,
	}
}

// ExecuteAgentStreaming executes a planning entity using Claude Code with the full
// agent system prompt, streaming logs in real-time via LogSender.
// Supports session continuity via sessionID/isResume for system-chat.
// Retrieves memU memory context and appends to system prompt.
// Fails if the system prompt cannot be fetched.
func ExecuteAgentStreaming(entity *api.PlanningEntity, inputs map[string]interface{}, client *api.Client, serviceKey string, sender LogSender, seq *atomic.Int64, sessionID string, isResume bool) *ExecutionResult {
	// 1. Fetch system prompt from API
	systemPrompt, err := client.FetchSystemPrompt(entity.AgentID, serviceKey)
	if err != nil {
		return &ExecutionResult{
			Success:  false,
			ExitCode: 1,
			Error:    fmt.Errorf("failed to fetch system prompt: %w", err),
		}
	}

	// 2. Retrieve memU memory context for this entity (non-blocking)
	memoryContext, memErr := client.RetrieveMemoryForEntity(entity.ID, serviceKey)
	if memErr != nil {
		// Log but don't fail — graceful degradation
		fmt.Printf("[memU] Failed to retrieve memory context: %v\n", memErr)
	}
	if memoryContext != "" {
		systemPrompt = systemPrompt + "\n\n" + memoryContext
	}

	// 3. Build task prompt (reuse existing buildPrompt)
	taskPrompt := buildPrompt(entity, inputs)

	// 4. Build command args with streaming flags + system prompt
	args := []string{
		"auth", "claude",
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--append-system-prompt", systemPrompt,
	}

	// Add session continuity flags for system-chat
	if sessionID != "" {
		if isResume {
			args = append(args, "--resume", sessionID)
		} else {
			args = append(args, "--session-id", sessionID)
		}
	}

	args = append(args, "-p", taskPrompt)

	cmd := exec.Command("kindship", args...)
	cmd.Dir = "/workspace"

	var stdoutBuf, stderrBuf bytes.Buffer
	stdoutPassthru := &limitedWriter{buf: &stdoutBuf, limit: 10 << 20} // 10MB for agent
	stderrPassthru := &limitedWriter{buf: &stderrBuf, limit: 10 << 20}

	stdoutStream := NewStreamWriter("stdout", sender, stdoutPassthru, seq)
	stderrStream := NewStreamWriter("stderr", sender, stderrPassthru, seq)

	cmd.Stdout = stdoutStream
	cmd.Stderr = stderrStream

	execErr := cmd.Run()

	stdoutStream.Close()
	stderrStream.Close()

	exitCode := 0
	if execErr != nil {
		if exitError, ok := execErr.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			exitCode = 1
		}
	}

	return &ExecutionResult{
		Success:  exitCode == 0,
		Stdout:   reduceStreamJSONForCompletion(stdoutBuf.String()),
		Stderr:   stderrBuf.String(),
		ExitCode: exitCode,
		Error:    execErr,
	}
}
