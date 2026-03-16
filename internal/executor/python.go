package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sync/atomic"

	"github.com/kindship-ai/kindship-cli/internal/api"
)

// ExecutePython runs Python code from entity.Code
func ExecutePython(entity *api.PlanningEntity, inputs map[string]interface{}) *ExecutionResult {
	return ExecutePythonWithContext(context.Background(), entity, inputs)
}

// ExecutePythonStreaming runs Python code with real-time log streaming.
func ExecutePythonStreaming(entity *api.PlanningEntity, inputs map[string]interface{}, sender LogSender, seq *atomic.Int64) *ExecutionResult {
	return ExecutePythonStreamingWithContext(context.Background(), entity, inputs, sender, seq)
}

// ExecutePythonStreamingWithContext runs Python code with streaming and cancellation.
func ExecutePythonStreamingWithContext(ctx context.Context, entity *api.PlanningEntity, inputs map[string]interface{}, sender LogSender, seq *atomic.Int64) *ExecutionResult {
	if entity.Code == nil || *entity.Code == "" {
		return &ExecutionResult{
			Success:  false,
			ExitCode: 1,
			Error:    fmt.Errorf("no code provided for PYTHON execution"),
		}
	}

	execCtx, cancel := context.WithTimeout(ctx, DefaultExecTimeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "python3", "-c", *entity.Code)
	cmd.Dir = "/workspace"
	cmd.Env = buildEnvWithInputs(inputs)

	var stdoutBuf, stderrBuf bytes.Buffer
	stdoutLimited := &limitedWriter{buf: &stdoutBuf, limit: maxOutputBytes}
	stderrLimited := &limitedWriter{buf: &stderrBuf, limit: maxOutputBytes}

	stdoutStream := NewStreamWriter("stdout", sender, stdoutLimited, seq)
	stderrStream := NewStreamWriter("stderr", sender, stderrLimited, seq)

	cmd.Stdout = stdoutStream
	cmd.Stderr = stderrStream

	err := cmd.Run()

	stdoutStream.Close()
	stderrStream.Close()

	exitCode := 0
	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return &ExecutionResult{
				Success:  false,
				Stdout:   stdoutBuf.String(),
				Stderr:   stderrBuf.String(),
				ExitCode: 124,
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
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		ExitCode: exitCode,
		Error:    err,
	}
}

// ExecutePythonWithContext runs Python code with context for cancellation/timeout.
func ExecutePythonWithContext(ctx context.Context, entity *api.PlanningEntity, inputs map[string]interface{}) *ExecutionResult {
	if entity.Code == nil || *entity.Code == "" {
		return &ExecutionResult{
			Success:  false,
			ExitCode: 1,
			Error:    fmt.Errorf("no code provided for PYTHON execution"),
		}
	}

	execCtx, cancel := context.WithTimeout(ctx, DefaultExecTimeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "python3", "-c", *entity.Code)
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
				ExitCode: 124,
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
