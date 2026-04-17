package executor

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
)

// Session-conflict retry tuning. Bounded retry loop around the Claude
// subprocess invocation only — other CLIs (codex, gemini, opencode) do
// not plumb --session-id today, so no retry branch applies to them.
const (
	sessionConflictMaxAttempts   = 3
	sessionConflictBaseDelayMs   = 2000
	sessionConflictJitterMaxMs   = 1000
	sessionConflictLogPrefix     = "[kindship:session-conflict-retry"
)

// commandContext is the seam for constructing the Claude subprocess.
// Production wires it to exec.Command + cmd.Dir = /workspace; unit tests
// override it to return a fake *exec.Cmd (typically `sh -c '...'`) that
// writes deterministic stdout/stderr and exits with a known code without
// requiring /workspace to exist. Without this seam the retry loop would
// need a live `kindship auth claude` binary to test.
var commandContext = func(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Dir = "/workspace"
	return cmd
}

// sessionConflictPattern matches the Claude Code CLI error emitted when two
// processes try to hold the same --session-id concurrently. The canonical
// phrase referenced in kindship-vercel/apps/web/lib/planning/complete-execution.ts
// is "Session ID already in use". Pattern is intentionally forgiving (case-
// insensitive, flexible whitespace/punctuation) because the exact wording
// drifts across Claude Code minor versions.
//
// Last verified (regex hit against production stderr): pending — not yet
// confirmed against live collision output. Fallback to published phrasing.
// If retries silently stop firing after a Claude update, re-capture the
// current stderr string on a live container and update this pattern.
var sessionConflictPattern = regexp.MustCompile(`(?i)session[\s_\-:,.]*id\b[^\n]{0,80}?already[^\n]{0,10}?in[^\n]{0,10}?use`)

// isSessionConflictError reports whether the given stderr buffer contains
// the Claude Code "session already in use" error. Used by the retry loop
// inside ExecuteAgentStreaming to know when a transient session-lock race
// is worth retrying.
func isSessionConflictError(stderr string) bool {
	if stderr == "" {
		return false
	}
	return sessionConflictPattern.MatchString(stderr)
}

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

// resolveInnerLoopModel returns the model to use for AGENT execution.
// Reads INNER_LOOP_MODEL env var. Empty string means CLI default.
func resolveInnerLoopModel() string {
	return os.Getenv("INNER_LOOP_MODEL")
}

// writeInstructionFile writes the system prompt to the appropriate instruction
// file for non-Claude CLIs. Each CLI discovers instructions from specific
// file paths (validated in cli-research docs).
func writeInstructionFile(cli string, content string) error {
	if content == "" {
		return nil
	}

	// Use global/user-level paths (not /workspace/) to avoid clobbering
	// any AGENTS.md the agent creates as part of its work.
	var filePath string
	switch cli {
	case "codex":
		filePath = "/home/autonomous/.codex/AGENTS.md"
	case "gemini":
		filePath = "/home/autonomous/.gemini/GEMINI.md"
	case "opencode":
		filePath = "/home/autonomous/.config/opencode/AGENTS.md"
	default:
		return nil // Claude uses --append-system-prompt flag, no file needed
	}

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory for instruction file: %w", err)
	}

	return os.WriteFile(filePath, []byte(content), 0644)
}

// writeOpenCodePermissionConfig writes the OpenCode permission config file
// to allow full tool access in headless mode (equivalent to --dangerously-skip-permissions).
func writeOpenCodePermissionConfig() error {
	config := `{"$schema":"https://opencode.ai/config.json","permission":"allow"}`
	return os.WriteFile("/workspace/opencode.json", []byte(config), 0644)
}

// translateOpenCodeModel translates UI model IDs to OpenCode-native model IDs.
// Matches the OPENCODE_MODEL_MAP in cli-runtime.ts.
func translateOpenCodeModel(model string) string {
	translations := map[string]string{
		"minimax-m2.5":   "opencode/minimax-m2.5-free",
		"mimo-v2-pro":    "opencode/mimo-v2-pro-free",
		"deepseek-v3.2":  "opencode/gpt-5-nano", // fallback — deepseek model ID needs verification
	}
	if translated, ok := translations[model]; ok {
		return translated
	}
	// Unknown models get opencode/ prefix
	if model != "" {
		return "opencode/" + model
	}
	return model
}

// buildCliArgs constructs the command-line arguments for the selected coding CLI.
// Each CLI has different flags for headless execution.
// model is the INNER_LOOP_MODEL env var value (e.g., "gpt-5.4", "claude-sonnet-4-6").
func buildCliArgs(cli string, model string, systemPrompt string, taskPrompt string, sessionID string, isResume bool) (command string, args []string) {
	switch cli {
	case "codex":
		args = []string{
			"exec",
			taskPrompt,
			"--dangerously-bypass-approvals-and-sandbox",
			"--skip-git-repo-check",
			"--json",
		}
		if model != "" {
			args = append(args, "-m", model)
		}
		return "codex", args

	case "gemini":
		args = []string{
			"--yolo",
			"-o", "stream-json",
			"-p", taskPrompt,
		}
		if model != "" {
			args = append(args, "-m", model)
		}
		return "gemini", args

	case "opencode":
		translatedModel := translateOpenCodeModel(model)
		args = []string{
			"run", taskPrompt,
			"--format", "json",
		}
		if translatedModel != "" {
			args = append(args, "-m", translatedModel)
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
		if model != "" {
			args = append(args, "--model", model)
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
	model := resolveInnerLoopModel()

	// 1. Fetch system prompt from API
	systemPrompt, err := client.FetchSystemPrompt(entity.AgentID, serviceKey)
	if err != nil {
		return &ExecutionResult{
			Success:  false,
			ExitCode: 1,
			Error:    fmt.Errorf("failed to fetch system prompt: %w", err),
		}
	}

	// 2. Write instruction file for non-Claude CLIs
	if cli != "claude" {
		if writeErr := writeInstructionFile(cli, systemPrompt); writeErr != nil {
			fmt.Printf("[inner-loop] Warning: failed to write instruction file: %v\n", writeErr)
		}
	}

	// 3. Write OpenCode permission config
	if cli == "opencode" {
		if writeErr := writeOpenCodePermissionConfig(); writeErr != nil {
			fmt.Printf("[inner-loop] Warning: failed to write OpenCode permission config: %v\n", writeErr)
		}
	}

	// 4. Build task prompt (reuse existing buildPrompt)
	taskPrompt := buildPrompt(entity, inputs)

	// 5. Build CLI-specific args
	cliCommand, cliArgs := buildCliArgs(cli, model, systemPrompt, taskPrompt, "", false)

	// 6. Execute via kindship auth <cli> which injects secrets
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
// When sessionRetryOnConflict is true AND cli resolves to "claude", transient
// "Session ID already in use" errors on subprocess stderr trigger a bounded
// retry loop (up to sessionConflictMaxAttempts with jittered backoff). The
// retry is invisible to the caller — only the final attempt's result surfaces.
// Retrieves memU memory context and appends to system prompt.
func ExecuteAgentStreaming(entity *api.PlanningEntity, inputs map[string]interface{}, client *api.Client, serviceKey string, sender LogSender, seq *atomic.Int64, sessionID string, isResume bool, sessionRetryOnConflict bool) *ExecutionResult {
	cli := resolveInnerLoopCli()
	model := resolveInnerLoopModel()

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

	// 3. Write instruction file for non-Claude CLIs (includes memU context)
	if cli != "claude" {
		if writeErr := writeInstructionFile(cli, systemPrompt); writeErr != nil {
			fmt.Printf("[inner-loop] Warning: failed to write instruction file: %v\n", writeErr)
		}
	}

	// 4. Write OpenCode permission config
	if cli == "opencode" {
		if writeErr := writeOpenCodePermissionConfig(); writeErr != nil {
			fmt.Printf("[inner-loop] Warning: failed to write OpenCode permission config: %v\n", writeErr)
		}
	}

	// 5. Build task prompt (reuse existing buildPrompt)
	taskPrompt := buildPrompt(entity, inputs)

	// 6. Build CLI-specific args
	cliCommand, cliArgs := buildCliArgs(cli, model, systemPrompt, taskPrompt, sessionID, isResume)

	fmt.Printf("[inner-loop] Using CLI: %s (command: kindship auth %s)\n", cli, cliCommand)

	finalStdoutBuf, finalStderrBuf, finalExitCode, finalExecErr :=
		runClaudeWithRetry(cli, cliCommand, cliArgs, sender, seq, sessionRetryOnConflict)

	// Only reduce stream-json for Claude (other CLIs don't output this format yet)
	stdout := finalStdoutBuf.String()
	if cli == "claude" {
		stdout = reduceStreamJSONForCompletion(stdout)
	}

	return &ExecutionResult{
		Success:  finalExitCode == 0,
		Stdout:   stdout,
		Stderr:   finalStderrBuf.String(),
		ExitCode: finalExitCode,
		Error:    finalExecErr,
	}
}

// runClaudeWithRetry executes `kindship auth <cliCommand> <cliArgs...>` and,
// for Claude specifically with sessionRetryOnConflict enabled, retries on
// "Session ID already in use" up to sessionConflictMaxAttempts times with
// jittered backoff. Only the final attempt's buffers are returned to the
// caller — retry noise is surfaced via the stderr stream (which flows to
// run logs) using the `[kindship:session-conflict-retry ...]` marker.
//
// This function is the retry-loop seam. Unit tests swap `commandContext`
// and (optionally) the sleep path to exercise all branches.
func runClaudeWithRetry(
	cli string,
	cliCommand string,
	cliArgs []string,
	sender LogSender,
	seq *atomic.Int64,
	sessionRetryOnConflict bool,
) (finalStdout bytes.Buffer, finalStderr bytes.Buffer, finalExitCode int, finalExecErr error) {
	retryEligible := sessionRetryOnConflict && cli == "claude"
	maxAttempts := 1
	if retryEligible {
		maxAttempts = sessionConflictMaxAttempts
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		fullArgs := append([]string{"auth", cliCommand}, cliArgs...)
		cmd := commandContext("kindship", fullArgs...)

		// Fresh buffers per attempt so we can inspect this-attempt stderr
		// for the conflict marker without carrying over prior attempts'
		// noise. The caller only sees the FINAL attempt's stdout/stderr.
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

		shouldRetry := retryEligible &&
			attempt < maxAttempts &&
			exitCode != 0 &&
			isSessionConflictError(stderrBuf.String())

		if shouldRetry {
			waitMs := sessionConflictBaseDelayMs + rand.Intn(sessionConflictJitterMaxMs)
			// Emit retry notice via the stderr stream so it flows through
			// the CLI's existing logSender path to the run-logs column —
			// NOT via Hatchet workflow's spawnResult.stderr (nohup +
			// redirection decouples child stderr from spawn return), and
			// NOT via a new Axiom logger (would require threading a logger
			// into the executor, widening the change).
			retryStream := NewStreamWriter("stderr", sender, stderrPassthru, seq)
			fmt.Fprintf(retryStream, "%s attempt=%d waited_ms=%d]\n", sessionConflictLogPrefix, attempt, waitMs)
			retryStream.Close()
			time.Sleep(time.Duration(waitMs) * time.Millisecond)
			continue
		}

		finalStdout = stdoutBuf
		finalStderr = stderrBuf
		finalExitCode = exitCode
		finalExecErr = execErr
		return
	}
	return
}
