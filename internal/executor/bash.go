package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
)

const maxOutputBytes = 1 << 20 // 1MB

// DefaultExecTimeout is the maximum time a bash/python command can run.
const DefaultExecTimeout = 10 * time.Minute

// ExecuteBash runs a shell command from entity.Code
func ExecuteBash(entity *api.PlanningEntity, inputs map[string]interface{}) *ExecutionResult {
	return ExecuteBashWithContext(context.Background(), entity, inputs)
}

// ExecuteBashWithContext runs a shell command with context for cancellation/timeout.
func ExecuteBashWithContext(ctx context.Context, entity *api.PlanningEntity, inputs map[string]interface{}) *ExecutionResult {
	if entity.Code == nil || *entity.Code == "" {
		return &ExecutionResult{
			Success:  false,
			ExitCode: 1,
			Error:    fmt.Errorf("no code provided for BASH execution"),
		}
	}

	execCtx, cancel := context.WithTimeout(ctx, DefaultExecTimeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "sh", "-c", *entity.Code)
	cmd.Dir = "/workspace"
	cmd.Env = buildEnvWithInputs(inputs)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{buf: &stdout, limit: maxOutputBytes}
	cmd.Stderr = &limitedWriter{buf: &stderr, limit: maxOutputBytes}

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return &ExecutionResult{
				Success:  false,
				Stdout:   stdout.String(),
				Stderr:   stderr.String(),
				ExitCode: 124, // standard timeout exit code
				Error:    fmt.Errorf("execution timed out after %v", DefaultExecTimeout),
			}
		}
		if exitError, ok := err.(*exec.ExitError); ok {
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
		Error:    err,
	}
}

// inputDir is where input JSON files are written for safe BASH consumption.
// BASH's echo interprets \n escape sequences, corrupting JSON. File-based
// access via INPUT_<LABEL>_FILE avoids this.
const inputDir = "/tmp/.kindship-inputs"

// buildEnvWithInputs creates an environment variable slice with the current
// env plus INPUT_<LABEL>=<json_value> and INPUT_<LABEL>_FILE=<path> for each
// labeled input. The _FILE variant provides safe access for BASH scripts that
// would otherwise corrupt JSON via echo's escape sequence interpretation.
func buildEnvWithInputs(inputs map[string]interface{}) []string {
	env := os.Environ()

	if len(inputs) > 0 {
		_ = os.MkdirAll(inputDir, 0755)
	}

	for label, value := range inputs {
		envKey := "INPUT_" + strings.ToUpper(strings.ReplaceAll(label, "-", "_"))
		jsonBytes, err := json.Marshal(value)
		if err != nil {
			continue
		}
		env = append(env, fmt.Sprintf("%s=%s", envKey, string(jsonBytes)))

		// Write to file for safe BASH access (avoids echo \n interpretation)
		filePath := fmt.Sprintf("%s/%s.json", inputDir, label)
		if writeErr := os.WriteFile(filePath, jsonBytes, 0644); writeErr == nil {
			env = append(env, fmt.Sprintf("%s_FILE=%s", envKey, filePath))
		}
	}

	return env
}

// limitedWriter wraps a bytes.Buffer and stops writing after limit bytes.
type limitedWriter struct {
	buf   *bytes.Buffer
	limit int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		return len(p), nil // discard silently
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	return w.buf.Write(p)
}
