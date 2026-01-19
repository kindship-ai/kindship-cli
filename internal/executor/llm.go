package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/kindship-ai/kindship-cli/internal/api"
)

// ExecutionResult represents the result of an execution attempt
type ExecutionResult struct {
	Success  bool
	Stdout   string
	Stderr   string
	ExitCode int
	Error    error
}

// ExecuteLLM executes a planning entity using LLM reasoning (Claude Code)
func ExecuteLLM(entity *api.PlanningEntity, inputs map[string]interface{}) *ExecutionResult {
	prompt := buildPrompt(entity, inputs)

	// Execute Claude Code with the prompt
	cmd := exec.Command("claude", "--prompt", prompt)
	cmd.Dir = "/workspace"

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
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

// buildPrompt creates a comprehensive prompt for Claude Code
func buildPrompt(entity *api.PlanningEntity, inputs map[string]interface{}) string {
	var prompt strings.Builder

	prompt.WriteString("You are executing a planning entity in Kindship.\n\n")

	// Core task info
	prompt.WriteString(fmt.Sprintf("# Task: %s\n\n", entity.Title))
	prompt.WriteString(fmt.Sprintf("## Description\n%s\n\n", entity.Description))

	// Add rationale if available
	if entity.Rationale != nil && *entity.Rationale != "" {
		prompt.WriteString(fmt.Sprintf("## Rationale\n%s\n\n", *entity.Rationale))
	}

	// Add inputs from dependencies
	if len(inputs) > 0 {
		prompt.WriteString("## Available Inputs\n\n")
		prompt.WriteString("The following inputs are available from completed dependencies:\n\n")
		for label, value := range inputs {
			jsonBytes, err := json.MarshalIndent(value, "", "  ")
			if err != nil {
				prompt.WriteString(fmt.Sprintf("### Input: %s\n[Error marshaling input]\n\n", label))
				continue
			}
			// Add a note for the "prev" label
			if label == "prev" {
				prompt.WriteString(fmt.Sprintf("### Input: %s (Previous Sibling Output)\n", label))
			} else {
				prompt.WriteString(fmt.Sprintf("### Input: %s\n", label))
			}
			prompt.WriteString("```json\n")
			prompt.WriteString(string(jsonBytes))
			prompt.WriteString("\n```\n\n")
		}
	}

	// Add success criteria
	prompt.WriteString("## Success Criteria\n")
	if entity.SuccessCriteria.Description != "" {
		prompt.WriteString(fmt.Sprintf("%s\n\n", entity.SuccessCriteria.Description))
	}
	if len(entity.SuccessCriteria.MeasurableOutcomes) > 0 {
		prompt.WriteString("### Measurable Outcomes\n")
		for _, outcome := range entity.SuccessCriteria.MeasurableOutcomes {
			prompt.WriteString(fmt.Sprintf("- %s\n", outcome))
		}
		prompt.WriteString("\n")
	}

	// Add output schema if provided
	if len(entity.OutputSchema) > 0 {
		prompt.WriteString("## Expected Output Format\n")
		schemaJSON, err := json.MarshalIndent(entity.OutputSchema, "", "  ")
		if err == nil {
			prompt.WriteString("Your outputs should conform to this JSON schema:\n")
			prompt.WriteString("```json\n")
			prompt.WriteString(string(schemaJSON))
			prompt.WriteString("\n```\n\n")
		}
	}

	// Add constraints and guidelines
	prompt.WriteString("## Guidelines\n")
	prompt.WriteString("- Work in the /workspace directory\n")
	prompt.WriteString("- All artifacts should be saved to /workspace\n")
	prompt.WriteString("- Ensure all success criteria are met before completing\n")
	prompt.WriteString("- If you encounter blockers, document them clearly\n")
	if len(inputs) > 0 {
		prompt.WriteString("- Use the available inputs from dependencies as context for this task\n")
	}
	prompt.WriteString("\n")

	// Execution instructions
	prompt.WriteString("## Instructions\n")
	prompt.WriteString("Execute this task completely. When done, provide a summary of:\n")
	prompt.WriteString("1. What was accomplished\n")
	prompt.WriteString("2. Any artifacts created (with file paths)\n")
	prompt.WriteString("3. How each success criterion was met\n")
	prompt.WriteString("4. Any issues encountered or next steps needed\n")

	return prompt.String()
}
