package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/executor"
	"github.com/kindship-ai/kindship-cli/internal/logging"
	"github.com/kindship-ai/kindship-cli/internal/validator"
	"github.com/spf13/cobra"
)

var (
	agentID    string
	serviceKey string
	apiURL     string
)

// ErrAskUserSkipped is returned when an ASK_USER task is started but not
// blocked on — the loop should move to the next task.
var ErrAskUserSkipped = errors.New("ASK_USER task started, awaiting user response")

var runCmd = &cobra.Command{
	Use:   "run <entity-id>",
	Short: "Execute a planning entity",
	Long: `Execute a planning entity by UUID.

The command fetches the entity details from the API and auto-detects the entity
type. For PROCESS entities, it executes all child tasks in sequence. For all
other entity types, it executes the single entity based on its execution_mode
(LLM_REASONING, BASH, PYTHON, etc.) and reports the results back to the API.

Configuration (flags take precedence over environment variables):
  --agent-id / AGENT_ID - The agent container ID
  --service-key / KINDSHIP_SERVICE_KEY - Service key for authentication
  --api-url / KINDSHIP_API_URL - API base URL (defaults to https://kindship.ai)

Examples:
  # Execute a single task
  kindship run 550e8400-e29b-41d4-a716-446655440000

  # Execute all tasks in a Process
  kindship run 660e8400-e29b-41d4-a716-446655440000`,
	Args: cobra.ExactArgs(1),
	RunE: runExecute,
}

func runExecute(cmd *cobra.Command, args []string) error {
	entityID := args[0]

	// Read from flags first, fall back to environment variables
	if agentID == "" {
		agentID = os.Getenv("AGENT_ID")
	}
	if serviceKey == "" {
		serviceKey = os.Getenv("KINDSHIP_SERVICE_KEY")
	}
	if apiURL == "" {
		apiURL = os.Getenv("KINDSHIP_API_URL")
	}
	if apiURL == "" {
		apiURL = "https://kindship.ai"
	}

	// Initialize logging
	log := logging.Init(agentID, "run", verbose)
	defer log.FlushSync()

	// Validate required parameters
	if agentID == "" {
		log.Error("AGENT_ID not provided", nil)
		return fmt.Errorf("AGENT_ID is required (use --agent-id flag or AGENT_ID environment variable)")
	}
	if serviceKey == "" {
		log.Error("KINDSHIP_SERVICE_KEY not provided", nil)
		return fmt.Errorf("KINDSHIP_SERVICE_KEY is required (use --service-key flag or KINDSHIP_SERVICE_KEY environment variable)")
	}

	// Create API client
	client := api.NewClient(apiURL, verbose)

	// Fetch entity to detect type before execution
	log.Info("Fetching entity to detect type", map[string]interface{}{
		"entity_id": entityID,
	})
	entityResp, err := client.FetchEntityForExecution(entityID, serviceKey)
	if err != nil {
		log.Error("Failed to fetch entity", err)
		return fmt.Errorf("failed to fetch entity: %w", err)
	}

	// If this is a PROCESS entity, run the process execution loop
	if entityResp.Entity.Type == "PROCESS" {
		log.Info("Entity is a PROCESS, executing all child tasks", map[string]interface{}{
			"entity_id":    entityID,
			"entity_title": entityResp.Entity.Title,
		})
		return runProcessExecution(entityID, client, log)
	}

	// Otherwise, execute a single entity
	success, err := executeEntity(EntityExecutionParams{
		EntityID:   entityID,
		AgentID:    agentID,
		ServiceKey: serviceKey,
		Client:     client,
		Log:        log,
	})

	if err != nil {
		if errors.Is(err, ErrAskUserSkipped) {
			log.Info("ASK_USER task started, awaiting user response via UI")
			return nil
		}
		return err
	}

	if !success {
		os.Exit(1)
	}

	return nil
}

// EntityExecutionParams holds parameters for executing an entity.
// Used by both `kindship run <id>` and the agent loop.
type EntityExecutionParams struct {
	EntityID   string
	AgentID    string
	ServiceKey string
	Client     *api.Client
	Log        *logging.Logger
}

// executeEntity runs the full execution lifecycle for a single entity.
// Returns (true, nil) on success, (false, nil) on execution failure (non-zero exit),
// and (false, err) on infrastructure errors.
// Returns (false, ErrAskUserSkipped) for ASK_USER mode tasks.
func executeEntity(params EntityExecutionParams) (bool, error) {
	startTime := time.Now()
	log := params.Log

	log.Info("Starting entity execution", map[string]interface{}{
		"entity_id": params.EntityID,
	})

	// Step 1: Fetch entity details
	log.Info("Fetching entity details")
	fetchStart := time.Now()
	entityResp, err := params.Client.FetchEntityForExecution(params.EntityID, params.ServiceKey)
	if err != nil {
		log.Error("Failed to fetch entity", err, map[string]interface{}{
			"duration_ms": time.Since(fetchStart).Milliseconds(),
		})
		return false, fmt.Errorf("failed to fetch entity: %w", err)
	}
	log.WithDuration("Fetched entity", time.Since(fetchStart), map[string]interface{}{
		"title":          entityResp.Entity.Title,
		"execution_mode": entityResp.Entity.ExecutionMode,
		"status":         entityResp.Entity.Status,
	})

	// Log inputs information
	inputLabels := validator.GetInputLabels(entityResp.Inputs)
	log.Info("Inputs gathered from dependencies", map[string]interface{}{
		"input_count": len(entityResp.Inputs),
		"labels":      inputLabels,
	})

	// Step 2: Validate dependencies
	if !entityResp.DependenciesStatus.AllMet {
		log.Error("Dependencies not met", nil, map[string]interface{}{
			"pending": entityResp.DependenciesStatus.Pending,
		})
		return false, fmt.Errorf("dependencies not met: %v", entityResp.DependenciesStatus.Pending)
	}

	// Step 2b: Validate inputs against input_schema if provided
	if len(entityResp.Entity.InputSchema) > 0 {
		log.Info("Validating inputs against input_schema")
		if err := validator.ValidateInputs(entityResp.Inputs, entityResp.Entity.InputSchema); err != nil {
			log.Error("Input validation failed", err)
			return false, fmt.Errorf("input validation failed: %w", err)
		}
		log.Info("Input validation passed")
	}

	// Step 3: Create run
	log.Info("Creating run")
	startExecReq := api.ExecutionStartRequest{
		EntityID:      params.EntityID,
		ExecutionMode: entityResp.Entity.ExecutionMode,
		AgentID:       params.AgentID,
	}
	startResp, err := params.Client.StartExecution(startExecReq, params.ServiceKey)
	if err != nil {
		log.Error("Failed to start execution", err)
		return false, fmt.Errorf("failed to start execution: %w", err)
	}
	log.Info("Run created", map[string]interface{}{
		"execution_id":   startResp.ExecutionID,
		"attempt_number": startResp.AttemptNumber,
	})

	executionID := startResp.ExecutionID

	// ASK_USER: create the run (RUNNING) but don't block — user responds via UI
	if entityResp.Entity.ExecutionMode == api.ExecutionModeAskUser {
		log.Info("ASK_USER task started, not blocking", map[string]interface{}{
			"execution_id": executionID,
			"entity_id":    params.EntityID,
		})
		return false, ErrAskUserSkipped
	}

	// Step 4: Execute based on execution mode
	log.Info("Executing entity", map[string]interface{}{
		"mode": entityResp.Entity.ExecutionMode,
	})
	execStart := time.Now()

	var result *executor.ExecutionResult
	switch entityResp.Entity.ExecutionMode {
	case api.ExecutionModeLLMReasoning:
		result = executor.ExecuteLLM(&entityResp.Entity, startResp.Inputs)
	case api.ExecutionModeBash:
		result = executor.ExecuteBash(&entityResp.Entity, startResp.Inputs)
	case api.ExecutionModePython:
		result = executor.ExecutePython(&entityResp.Entity, startResp.Inputs)
	case api.ExecutionModePythonSandbox:
		// Legacy mode — treat as PYTHON
		result = executor.ExecutePython(&entityResp.Entity, startResp.Inputs)
	case api.ExecutionModeHybrid:
		// HYBRID uses LLM with entity context + code as reference
		result = executor.ExecuteLLM(&entityResp.Entity, startResp.Inputs)
	default:
		log.Error("Unknown execution mode", nil, map[string]interface{}{
			"mode": entityResp.Entity.ExecutionMode,
		})
		return false, fmt.Errorf("unknown execution mode: %s", entityResp.Entity.ExecutionMode)
	}

	execDuration := time.Since(execStart)
	log.WithDuration("Execution completed", execDuration, map[string]interface{}{
		"success":   result.Success,
		"exit_code": result.ExitCode,
	})

	// Step 4b: Validate outputs against output_schema if provided (only for successful executions)
	var structuredOutput map[string]interface{}
	var outputValidationRecord *api.ValidationRecord
	if result.Success && len(entityResp.Entity.OutputSchema) > 0 {
		log.Info("Validating outputs against output_schema")

		// Try to extract structured JSON from stdout
		extracted, extractErr := validator.ExtractJSONFromOutput(result.Stdout)
		if extractErr != nil {
			log.Warn("Could not extract structured output from stdout", map[string]interface{}{
				"error": extractErr.Error(),
			})
			failReason := fmt.Sprintf("Failed to extract structured output: %v", extractErr)
			outputValidationRecord = &api.ValidationRecord{
				ValidationType: "OUTPUT_SCHEMA",
				Outcome:        api.ValidationOutcomeWarn,
				Severity:       api.ValidationSeverityWarning,
				Target:         "output_schema",
				FailureReason:  &failReason,
			}
		} else {
			structuredOutput = extracted
			log.Info("Extracted structured output", map[string]interface{}{
				"keys": validator.GetInputLabels(extracted),
			})

			// Validate against output_schema
			if err := validator.ValidateOutputs(extracted, entityResp.Entity.OutputSchema); err != nil {
				log.Warn("Output validation failed", map[string]interface{}{
					"error": err.Error(),
				})
				failReason := err.Error()
				outputValidationRecord = &api.ValidationRecord{
					ValidationType: "OUTPUT_SCHEMA",
					Outcome:        api.ValidationOutcomeFail,
					Severity:       api.ValidationSeverityWarning,
					Target:         "output_schema",
					Actual:         extracted,
					FailureReason:  &failReason,
				}
			} else {
				log.Info("Output validation passed")
				outputValidationRecord = &api.ValidationRecord{
					ValidationType: "OUTPUT_SCHEMA",
					Outcome:        api.ValidationOutcomePass,
					Severity:       api.ValidationSeverityInfo,
					Target:         "output_schema",
					Actual:         extracted,
				}
			}
		}
	}

	// Step 5: Prepare completion request
	var completeReq api.ExecutionCompleteRequest
	if result.Success {
		completeReq.Status = api.ExecutionAttemptStatusSuccess
		outputs := &api.ExecutionOutputs{
			Stdout: result.Stdout,
			Stderr: result.Stderr,
			Metrics: map[string]interface{}{
				"duration_ms": execDuration.Milliseconds(),
				"exit_code":   result.ExitCode,
			},
		}
		// Add structured output if extracted
		if structuredOutput != nil {
			outputs.Structured = structuredOutput
		}
		completeReq.Outputs = outputs

		// Create validation record for successful execution
		validationRecord := api.ValidationRecord{
			ValidationType: "OUTPUT",
			Outcome:        api.ValidationOutcomePass,
			Severity:       api.ValidationSeverityInfo,
			Target:         "execution_completion",
			Actual: map[string]interface{}{
				"exit_code":   result.ExitCode,
				"duration_ms": execDuration.Milliseconds(),
			},
		}
		completeReq.ValidationRecords = []api.ValidationRecord{validationRecord}

		// Add output schema validation record if present
		if outputValidationRecord != nil {
			completeReq.ValidationRecords = append(completeReq.ValidationRecords, *outputValidationRecord)
		}
	} else {
		completeReq.Status = api.ExecutionAttemptStatusFailed
		failureMsg := fmt.Sprintf("Execution failed with exit code %d", result.ExitCode)
		if result.Error != nil {
			failureMsg = fmt.Sprintf("%s: %v", failureMsg, result.Error)
		}
		completeReq.FailureReason = &failureMsg
		outputs := &api.ExecutionOutputs{
			Stdout: result.Stdout,
			Stderr: result.Stderr,
			Metrics: map[string]interface{}{
				"duration_ms": execDuration.Milliseconds(),
				"exit_code":   result.ExitCode,
			},
		}
		completeReq.Outputs = outputs

		// Create validation record for failed execution
		validationRecord := api.ValidationRecord{
			ValidationType: "OUTPUT",
			Outcome:        api.ValidationOutcomeFail,
			Severity:       api.ValidationSeverityCritical,
			Target:         "execution_completion",
			Actual: map[string]interface{}{
				"exit_code":   result.ExitCode,
				"duration_ms": execDuration.Milliseconds(),
			},
			FailureReason: &failureMsg,
		}
		completeReq.ValidationRecords = []api.ValidationRecord{validationRecord}
	}

	// Step 6: Complete execution
	log.Info("Completing execution", map[string]interface{}{
		"status": completeReq.Status,
	})
	_, err = params.Client.CompleteExecution(executionID, completeReq, params.ServiceKey)
	if err != nil {
		log.Error("Failed to complete execution", err)
		return false, fmt.Errorf("failed to complete execution: %w", err)
	}

	totalDuration := time.Since(startTime)
	log.WithDuration("Run command completed", totalDuration, map[string]interface{}{
		"success":      result.Success,
		"execution_id": executionID,
	})

	return result.Success, nil
}

// runProcessExecution executes all tasks within a Process entity by polling
// for runnable tasks scoped to that Process. Extracted from the former
// "agent run" command.
func runProcessExecution(processEntityID string, client *api.Client, log *logging.Logger) error {
	// Set up graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Info("Received signal, shutting down", map[string]interface{}{
			"signal": sig.String(),
		})
		cancel()
	}()

	// Create Run for the Process entity
	startReq := api.ExecutionStartRequest{
		EntityID:      processEntityID,
		ExecutionMode: "PROCESS",
		AgentID:       agentID,
	}

	startResp, err := client.StartExecution(startReq, serviceKey)
	if err != nil {
		return fmt.Errorf("failed to start Process run: %w", err)
	}

	processRunID := startResp.ExecutionID
	log.Info("Created Process run", map[string]interface{}{
		"run_id": processRunID,
	})

	// Process execution loop
	tasksExecuted := 0
	var lastError error
	interrupted := false

	for {
		// Check context cancellation
		select {
		case <-ctx.Done():
			log.Info("Process execution interrupted by signal", map[string]interface{}{
				"tasks_executed": tasksExecuted,
			})
			interrupted = true
			lastError = ctx.Err()
			goto complete
		default:
		}

		// Fetch next task scoped to this Process
		nextResp, err := client.FetchNextTaskForProcess(agentID, processEntityID, serviceKey)
		if err != nil {
			log.Error("Failed to fetch next task", err, nil)
			lastError = err
			break
		}

		// No more tasks — Process complete
		if nextResp.Task == nil {
			log.Info("No more tasks in Process", map[string]interface{}{
				"tasks_executed": tasksExecuted,
			})
			break
		}

		// Execute task
		log.Info("Executing task", map[string]interface{}{
			"task_id":    nextResp.Task.ID,
			"task_title": nextResp.Task.Title,
		})

		success, err := executeEntity(EntityExecutionParams{
			EntityID:   nextResp.Task.ID,
			AgentID:    agentID,
			ServiceKey: serviceKey,
			Client:     client,
			Log:        log,
		})

		if err != nil && !errors.Is(err, ErrAskUserSkipped) {
			log.Error("Task execution failed", err, map[string]interface{}{
				"task_id": nextResp.Task.ID,
			})
			lastError = err
			// Continue to next task (non-fatal)
		} else if success {
			tasksExecuted++
		}
	}

complete:

	// Complete Process run
	completeReq := api.ExecutionCompleteRequest{
		Status: api.ExecutionAttemptStatusSuccess,
		Outputs: &api.ExecutionOutputs{
			Metrics: map[string]interface{}{
				"tasks_executed": tasksExecuted,
				"interrupted":    interrupted,
			},
		},
	}

	if interrupted {
		completeReq.Status = api.ExecutionAttemptStatusAbandoned
		errorMsg := "Process execution interrupted by signal"
		completeReq.FailureReason = &errorMsg
	} else if lastError != nil {
		completeReq.Status = api.ExecutionAttemptStatusFailed
		errorMsg := lastError.Error()
		completeReq.FailureReason = &errorMsg
	}

	_, err = client.CompleteExecution(processRunID, completeReq, serviceKey)
	if err != nil {
		log.Error("Failed to complete Process run", err, nil)
		return err
	}

	log.Info("Process execution completed", map[string]interface{}{
		"run_id":         processRunID,
		"status":         completeReq.Status,
		"tasks_executed": tasksExecuted,
		"interrupted":    interrupted,
	})

	if interrupted {
		return fmt.Errorf("Process execution interrupted")
	}

	if lastError != nil {
		return fmt.Errorf("Process completed with errors: %w", lastError)
	}

	return nil
}

func init() {
	runCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose logging for debugging")
	runCmd.Flags().StringVar(&agentID, "agent-id", "", "Agent container ID (defaults to AGENT_ID env var)")
	runCmd.Flags().StringVar(&serviceKey, "service-key", "", "Service key for authentication (defaults to KINDSHIP_SERVICE_KEY env var)")
	runCmd.Flags().StringVar(&apiURL, "api-url", "", "API base URL (defaults to KINDSHIP_API_URL env var or https://kindship.ai)")
}
