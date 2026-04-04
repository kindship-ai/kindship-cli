package executor

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

	var filePath string
	switch cli {
	case "codex":
		// Codex reads AGENTS.md from workspace and ~/.codex/AGENTS.md globally
		filePath = "/workspace/AGENTS.md"
	case "gemini":
		// Gemini reads GEMINI.md from workspace and ~/.gemini/GEMINI.md globally
		filePath = "/workspace/GEMINI.md"
	case "opencode":
		// OpenCode reads AGENTS.md from workspace and ~/.config/opencode/AGENTS.md globally
		// Also falls back to CLAUDE.md
		filePath = "/workspace/AGENTS.md"
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
// Retrieves memU memory context and appends to system prompt.
func ExecuteAgentStreaming(entity *api.PlanningEntity, inputs map[string]interface{}, client *api.Client, serviceKey string, sender LogSender, seq *atomic.Int64, sessionID string, isResume bool) *ExecutionResult {
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

	// 7. Execute via kindship auth <cli> which injects secrets
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

	// Only reduce stream-json for Claude (other CLIs don't output this format yet)
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
