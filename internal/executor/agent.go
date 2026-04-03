package executor

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"

	"github.com/kindship-ai/kindship-cli/internal/api"
)

// resolveInnerLoopCli returns the coding CLI to use for AGENT execution.
// Reads INNER_LOOP_CLI env var, defaults to "claude".
func resolveInnerLoopCli() string {
	cli := os.Getenv("INNER_LOOP_CLI")
	switch cli {
	case "claude", "codex", "gemini", "opencode":
		return cli
	default:
		return "claude"
	}
}

// buildCliArgs constructs the command-line arguments for the selected coding CLI.
// Each CLI has different flags for headless execution.
func buildCliArgs(cli string, systemPrompt string, taskPrompt string, sessionID string, isResume bool) (command string, args []string) {
	switch cli {
	case "codex":
		// codex exec <prompt> --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check
		args = []string{
			"exec",
			taskPrompt,
			"--dangerously-bypass-approvals-and-sandbox",
			"--skip-git-repo-check",
		}
		return "codex", args

	case "gemini":
		// gemini -p <prompt> --sandbox false
		args = []string{
			"-p", taskPrompt,
		}
		return "gemini", args

	case "opencode":
		// opencode -p <prompt>
		args = []string{
			"-p", taskPrompt,
		}
		return "opencode", args

	default: // claude
		args = []string{
			"--dangerously-skip-permissions",
			"--output-format", "stream-json",
			"--verbose",
			"--include-partial-messages",
			"--append-system-prompt", systemPrompt,
		}

		// Session continuity (Claude Code only)
		if sessionID != "" {
			if isResume {
				args = append(args, "--resume", sessionID)
			} else {
				args = append(args, "--session-id", sessionID)
			}
		}

		args = append(args, "-p", taskPrompt)
		return "claude", args
	}
}

// ExecuteAgent executes a planning entity using the selected coding CLI.
// Fails if the system prompt cannot be fetched.
func ExecuteAgent(entity *api.PlanningEntity, inputs map[string]interface{}, client *api.Client, serviceKey string) *ExecutionResult {
	cli := resolveInnerLoopCli()

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

	// 3. Build CLI-specific args
	cliCommand, cliArgs := buildCliArgs(cli, systemPrompt, taskPrompt, "", false)

	// 4. Execute via kindship auth <cli> which injects secrets
	fullArgs := append([]string{"auth", cliCommand}, cliArgs...)
	cmd := exec.Command("kindship", fullArgs...)
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

// ExecuteAgentStreaming executes a planning entity using the selected coding CLI,
// streaming logs in real-time via LogSender.
// Supports session continuity via sessionID/isResume for system-chat (Claude only).
// Retrieves memU memory context and appends to system prompt.
func ExecuteAgentStreaming(entity *api.PlanningEntity, inputs map[string]interface{}, client *api.Client, serviceKey string, sender LogSender, seq *atomic.Int64, sessionID string, isResume bool) *ExecutionResult {
	cli := resolveInnerLoopCli()

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

	// 4. Build CLI-specific args
	cliCommand, cliArgs := buildCliArgs(cli, systemPrompt, taskPrompt, sessionID, isResume)

	// 5. Execute via kindship auth <cli> which injects secrets
	fullArgs := append([]string{"auth", cliCommand}, cliArgs...)
	cmd := exec.Command("kindship", fullArgs...)
	cmd.Dir = "/workspace"

	fmt.Printf("[inner-loop] Using CLI: %s (command: kindship auth %s)\n", cli, cliCommand)

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

	// Only reduce stream-json for Claude (other CLIs don't output this format)
	stdout := stdoutBuf.String()
	if cli == "claude" {
		stdout = reduceStreamJSONForCompletion(stdout)
	}

	return &ExecutionResult{
		Success:  exitCode == 0,
		Stdout:   stdout,
		Stderr:   stderrBuf.String(),
		ExitCode: exitCode,
		Error:    execErr,
	}
}
