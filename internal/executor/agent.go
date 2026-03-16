package executor

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"

	"github.com/kindship-ai/kindship-cli/internal/api"
)

// ExecuteAgent executes a planning entity using Claude Code with the full
// agent system prompt injected via --append-system-prompt.
// Falls back to ExecuteLLM on transient API errors (5xx, timeout).
func ExecuteAgent(entity *api.PlanningEntity, inputs map[string]interface{}, client *api.Client, serviceKey string) *ExecutionResult {
	// 1. Fetch system prompt from API
	systemPrompt, err := client.FetchSystemPrompt(entity.AgentID, serviceKey)
	if err != nil {
		// Differentiate transient vs permanent errors
		if isTransientError(err) {
			fmt.Fprintf(os.Stderr, "[kindship:agent] Warning: failed to fetch system prompt (%v), falling back to LLM_REASONING\n", err)
			return ExecuteLLM(entity, inputs)
		}
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
// Falls back to ExecuteLLMStreaming on transient API errors (5xx, timeout).
func ExecuteAgentStreaming(entity *api.PlanningEntity, inputs map[string]interface{}, client *api.Client, serviceKey string, sender LogSender, seq *atomic.Int64) *ExecutionResult {
	// 1. Fetch system prompt from API
	systemPrompt, err := client.FetchSystemPrompt(entity.AgentID, serviceKey)
	if err != nil {
		// Differentiate transient vs permanent errors
		if isTransientError(err) {
			fmt.Fprintf(os.Stderr, "[kindship:agent] Warning: failed to fetch system prompt (%v), falling back to LLM_REASONING streaming\n", err)
			return ExecuteLLMStreaming(entity, inputs, sender, seq)
		}
		return &ExecutionResult{
			Success:  false,
			ExitCode: 1,
			Error:    fmt.Errorf("failed to fetch system prompt: %w", err),
		}
	}

	// 2. Build task prompt (reuse existing buildPrompt)
	taskPrompt := buildPrompt(entity, inputs)

	// 3. Execute with streaming flags + system prompt
	cmd := exec.Command("kindship", "auth", "claude",
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--append-system-prompt", systemPrompt,
		"-p", taskPrompt,
	)
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
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		ExitCode: exitCode,
		Error:    execErr,
	}
}

// isTransientError returns true for errors likely caused by temporary server issues
// (5xx status codes, network timeouts) where a fallback to LLM_REASONING is appropriate.
func isTransientError(err error) bool {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode >= 500
	}
	// Network errors (timeouts, connection refused) are also transient
	return false
}
