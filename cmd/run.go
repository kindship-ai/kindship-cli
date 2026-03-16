package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
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
	Use:   "run [entity-id]",
	Short: "Execute a planning entity",
	Long: `Execute a planning entity by UUID.

The command fetches the entity details from the API and auto-detects the entity
type. For PROCESS entities, it executes all child tasks in sequence. For all
other entity types, it executes the single entity based on its execution_mode
(LLM_REASONING, BASH, PYTHON, etc.) and reports the results back to the API.

Local development subcommands:
  kindship run local-next      Fetch + claim next task and print assignment markdown
  kindship run local-complete  Mark the current local task run as SUCCESS
  kindship run local-fail      Mark the current local task run as FAILED

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

	// If this entity uses ORCHESTRATE mode, run the orchestration loop
	if entityResp.Entity.ExecutionMode == api.ExecutionModeOrchestrate {
		log.Info("Entity uses ORCHESTRATE mode, executing all child tasks", map[string]interface{}{
			"entity_id":    entityID,
			"entity_title": entityResp.Entity.Title,
			"entity_type":  entityResp.Entity.Type,
		})
		return runOrchestration(entityID, client, log)
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
	EntityID    string
	AgentID     string
	ServiceKey  string
	Client      *api.Client
	Log         *logging.Logger
	ParentRunID string // ORCHESTRATE run ID (empty for top-level runs)
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

	// ORCHESTRATE: handled separately — creates its own run and orchestration loop
	if entityResp.Entity.ExecutionMode == api.ExecutionModeOrchestrate {
		startReq := api.ExecutionStartRequest{
			EntityID:      params.EntityID,
			ExecutionMode: api.ExecutionModeOrchestrate,
			AgentID:       params.AgentID,
		}
		orchStartResp, orchErr := params.Client.StartExecution(startReq, params.ServiceKey)
		if orchErr != nil {
			log.Error("Failed to start ORCHESTRATE run", orchErr)
			return false, fmt.Errorf("failed to start ORCHESTRATE run: %w", orchErr)
		}
		log.Info("Created ORCHESTRATE run", map[string]interface{}{
			"run_id":    orchStartResp.ExecutionID,
			"entity_id": params.EntityID,
		})
		orchLoopErr := orchestrateChildren(params.EntityID, orchStartResp.ExecutionID, params.Client, params.Log)
		if orchLoopErr != nil {
			return false, orchLoopErr
		}
		return true, nil
	}

	// Step 3: Create run
	log.Info("Creating run")
	startExecReq := api.ExecutionStartRequest{
		EntityID:      params.EntityID,
		ExecutionMode: entityResp.Entity.ExecutionMode,
		AgentID:       params.AgentID,
		ParentRun:     params.ParentRunID,
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

	// ASK_USER / CHOICE / CALL_TO_ACTION: create the run (RUNNING) but don't block — user responds via UI
	if entityResp.Entity.ExecutionMode == api.ExecutionModeAskUser ||
		entityResp.Entity.ExecutionMode == api.ExecutionModeChoice ||
		entityResp.Entity.ExecutionMode == api.ExecutionModeCallToAction {
		log.Info("Interactive task started, not blocking", map[string]interface{}{
			"execution_id": executionID,
			"entity_id":    params.EntityID,
			"mode":         entityResp.Entity.ExecutionMode,
		})
		return false, ErrAskUserSkipped
	}

	// Step 4: Execute based on execution mode
	log.Info("Executing entity", map[string]interface{}{
		"mode": entityResp.Entity.ExecutionMode,
	})
	execStart := time.Now()

	// Create log line sender for real-time streaming
	logSender := func(lines []api.LogLine) error {
		return params.Client.SendLogLines(startResp.ExecutionID, lines, params.ServiceKey)
	}

	// Shared seq counter for all stream writers in this run.
	// Both stdout and stderr use this to avoid UNIQUE(run_id, seq) collisions.
	var sharedSeq atomic.Int64

	// systemSend emits a "system" log line for lifecycle events.
	systemSend := func(content string) {
		_ = logSender([]api.LogLine{{
			Seq:     sharedSeq.Add(1),
			Stream:  "system",
			Content: content,
			Ts:      time.Now().UTC().Format(time.RFC3339Nano),
		}})
	}

	systemSend(fmt.Sprintf("Starting: %s (%s)", entityResp.Entity.Title, entityResp.Entity.ExecutionMode))

	var result *executor.ExecutionResult
	switch entityResp.Entity.ExecutionMode {
	case api.ExecutionModeLLMReasoning:
		result = executor.ExecuteLLMStreaming(&entityResp.Entity, startResp.Inputs, logSender, &sharedSeq)
	case api.ExecutionModeBash:
		result = executor.ExecuteBashStreaming(&entityResp.Entity, startResp.Inputs, logSender, &sharedSeq)
	case api.ExecutionModePython:
		result = executor.ExecutePythonStreaming(&entityResp.Entity, startResp.Inputs, logSender, &sharedSeq)
	case api.ExecutionModePythonSandbox:
		// Legacy mode — treat as PYTHON
		result = executor.ExecutePythonStreaming(&entityResp.Entity, startResp.Inputs, logSender, &sharedSeq)
	case api.ExecutionModeHybrid:
		// HYBRID uses LLM with entity context + code as reference
		result = executor.ExecuteLLMStreaming(&entityResp.Entity, startResp.Inputs, logSender, &sharedSeq)
	case api.ExecutionModeAgent:
		result = executor.ExecuteAgentStreaming(&entityResp.Entity, startResp.Inputs, params.Client, params.ServiceKey, logSender, &sharedSeq)
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

	// Emit system event for completion/failure
	if result.Success {
		systemSend(fmt.Sprintf("Completed successfully (exit code 0, %s)", execDuration.Truncate(time.Millisecond)))
	} else {
		reason := ""
		if result.Error != nil {
			reason = result.Error.Error()
		}
		if reason != "" {
			systemSend(fmt.Sprintf("Failed (exit code %d, %s): %s", result.ExitCode, execDuration.Truncate(time.Millisecond), reason))
		} else {
			systemSend(fmt.Sprintf("Failed (exit code %d, %s)", result.ExitCode, execDuration.Truncate(time.Millisecond)))
		}
	}

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

		// Emit system event for output validation result
		if outputValidationRecord != nil {
			systemSend(fmt.Sprintf("Output validation: %s", outputValidationRecord.Outcome))
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

// runOrchestration creates a new ORCHESTRATE run for any entity with
// execution_mode=ORCHESTRATE and orchestrates its child tasks.
func runOrchestration(entityID string, client *api.Client, log *logging.Logger) error {
	// Create Run for the entity
	startReq := api.ExecutionStartRequest{
		EntityID:      entityID,
		ExecutionMode: api.ExecutionModeOrchestrate,
		AgentID:       agentID,
	}

	startResp, err := client.StartExecution(startReq, serviceKey)
	if err != nil {
		return fmt.Errorf("failed to start ORCHESTRATE run: %w", err)
	}

	runID := startResp.ExecutionID
	log.Info("Created ORCHESTRATE run", map[string]interface{}{
		"run_id":    runID,
		"entity_id": entityID,
	})

	return orchestrateChildren(entityID, runID, client, log)
}

// resumeOrchestration resumes an existing ORCHESTRATE run after container
// restart, re-entering the child orchestration polling loop.
// Works for any entity type (PROCESS, PROJECT, TASK-with-children).
func resumeOrchestration(entityID, runID string, client *api.Client, log *logging.Logger) error {
	log.Info("Resuming ORCHESTRATE run", map[string]interface{}{
		"entity_id": entityID,
		"run_id":    runID,
	})

	return orchestrateChildren(entityID, runID, client, log)
}

// orchestrateChildren is the shared polling loop for ORCHESTRATE runs.
// It polls for runnable child tasks, executes them, and completes the
// parent run when all children are done.
func orchestrateChildren(entityID, runID string, client *api.Client, log *logging.Logger) error {
	// Set up graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Info("Received signal, shutting down orchestration", map[string]interface{}{
			"signal": sig.String(),
		})
		cancel()
	}()

	tasksExecuted := 0
	var lastError error
	interrupted := false
	childOutcomes := make(map[string]api.ExecutionAttemptStatus)
	const initialBackoff = 1 * time.Second
	const maxBackoff = 30 * time.Second
	backoff := initialBackoff

	// Max cumulative time spent waiting with pending-but-no-runnable tasks
	// before giving up. Prevents infinite polling when children are stuck
	// (e.g., blocked deps that will never resolve, stale ASK_USER).
	const maxIdleWait = 30 * time.Minute
	idleStart := time.Time{}

	for {
		// Check context cancellation
		select {
		case <-ctx.Done():
			log.Info("Orchestration interrupted by signal", map[string]interface{}{
				"tasks_executed": tasksExecuted,
			})
			interrupted = true
			lastError = ctx.Err()
			goto complete
		default:
		}

		// Fetch next task scoped to this entity
		nextResp, err := client.FetchNextTaskScoped(agentID, entityID, serviceKey)
		if err != nil {
			log.Error("Failed to fetch next task", err, nil)
			lastError = err
			break
		}

		// No task available right now
		if nextResp.Task == nil {
			if nextResp.PendingCount > 0 {
				// Track cumulative idle time to detect permanently stuck children
				if idleStart.IsZero() {
					idleStart = time.Now()
				}
				idleElapsed := time.Since(idleStart)
				if idleElapsed >= maxIdleWait {
					log.Error("Orchestration idle timeout — children pending but none runnable", nil, map[string]interface{}{
						"pending_count":   nextResp.PendingCount,
						"tasks_executed":  tasksExecuted,
						"idle_elapsed_m":  int(idleElapsed.Minutes()),
						"max_idle_wait_m": int(maxIdleWait.Minutes()),
					})
					lastError = fmt.Errorf("idle timeout: %d children pending but none runnable for %s", nextResp.PendingCount, idleElapsed.Truncate(time.Second))
					break
				}

				// Tasks are pending (blocked deps, ASK_USER waiting, etc.) — backoff and retry
				log.Info("No runnable tasks but children pending, waiting", map[string]interface{}{
					"pending_count":  nextResp.PendingCount,
					"tasks_executed": tasksExecuted,
					"backoff_s":      int(backoff.Seconds()),
					"idle_elapsed_m": int(idleElapsed.Minutes()),
				})
				select {
				case <-ctx.Done():
					interrupted = true
					lastError = ctx.Err()
					goto complete
				case <-time.After(backoff):
				}
				// Exponential backoff: 1s -> 2s -> 4s -> 8s -> 16s -> 30s (cap)
				backoff = backoff * 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}
			// pending_count == 0 — queue drained (success/failure outcomes captured separately)
			log.Info("Scoped child queue drained", map[string]interface{}{
				"tasks_executed": tasksExecuted,
			})
			break
		}
		// Reset backoff and idle timer on successful task fetch
		backoff = initialBackoff
		idleStart = time.Time{}

		// Execute task
		log.Info("Executing task", map[string]interface{}{
			"task_id":    nextResp.Task.ID,
			"task_title": nextResp.Task.Title,
		})

		success, err := executeEntity(EntityExecutionParams{
			EntityID:    nextResp.Task.ID,
			AgentID:     agentID,
			ServiceKey:  serviceKey,
			Client:      client,
			Log:         log,
			ParentRunID: runID,
		})

		if err != nil {
			if errors.Is(err, ErrAskUserSkipped) {
				// ASK_USER started — pending_count will keep loop alive
				log.Info("ASK_USER task started within orchestration", map[string]interface{}{
					"task_id": nextResp.Task.ID,
				})
				continue
			}
			// Retry-aware behavior: keep polling until queue drains.
			log.Error("Child task execution returned error; continuing orchestration loop", err, map[string]interface{}{
				"task_id": nextResp.Task.ID,
			})
			childOutcomes[nextResp.Task.ID] = api.ExecutionAttemptStatusFailed
			continue
		}

		if !success {
			// Child execution returned failure (non-zero exit)
			log.Error("Child task execution failed; continuing orchestration loop", nil, map[string]interface{}{
				"task_id": nextResp.Task.ID,
			})
			childOutcomes[nextResp.Task.ID] = api.ExecutionAttemptStatusFailed
			continue
		}

		childOutcomes[nextResp.Task.ID] = api.ExecutionAttemptStatusSuccess
		tasksExecuted++
	}

complete:

	terminalChildFailures := make([]string, 0)
	for entityID, outcome := range childOutcomes {
		if outcome == api.ExecutionAttemptStatusFailed {
			terminalChildFailures = append(terminalChildFailures, entityID)
		}
	}

	// Complete the orchestration run
	completeReq := api.ExecutionCompleteRequest{
		Status: api.ExecutionAttemptStatusSuccess,
		Outputs: &api.ExecutionOutputs{
			Metrics: map[string]interface{}{
				"tasks_executed":          tasksExecuted,
				"interrupted":             interrupted,
				"terminal_child_failures": len(terminalChildFailures),
			},
		},
	}

	if interrupted {
		completeReq.Status = api.ExecutionAttemptStatusAbandoned
		errorMsg := "Orchestration interrupted by signal"
		completeReq.FailureReason = &errorMsg
	} else if lastError != nil {
		completeReq.Status = api.ExecutionAttemptStatusFailed
		errorMsg := lastError.Error()
		completeReq.FailureReason = &errorMsg
	} else if len(terminalChildFailures) > 0 {
		completeReq.Status = api.ExecutionAttemptStatusFailed
		errorMsg := fmt.Sprintf("%d child task(s) ended in FAILED state", len(terminalChildFailures))
		completeReq.FailureReason = &errorMsg
	}

	_, err := client.CompleteExecution(runID, completeReq, serviceKey)
	if err != nil {
		log.Error("Failed to complete orchestration run", err, nil)
		return err
	}

	log.Info("Orchestration completed", map[string]interface{}{
		"run_id":                  runID,
		"status":                  completeReq.Status,
		"tasks_executed":          tasksExecuted,
		"interrupted":             interrupted,
		"terminal_child_failures": len(terminalChildFailures),
	})

	if interrupted {
		return fmt.Errorf("orchestration interrupted")
	}

	if lastError != nil {
		return fmt.Errorf("orchestration completed with errors: %w", lastError)
	}

	if len(terminalChildFailures) > 0 {
		return fmt.Errorf("orchestration completed with %d terminal child failure(s)", len(terminalChildFailures))
	}

	return nil
}

func init() {
	runCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose logging for debugging")
	runCmd.Flags().StringVar(&agentID, "agent-id", "", "Agent container ID (defaults to AGENT_ID env var)")
	runCmd.Flags().StringVar(&serviceKey, "service-key", "", "Service key for authentication (defaults to KINDSHIP_SERVICE_KEY env var)")
	runCmd.Flags().StringVar(&apiURL, "api-url", "", "API base URL (defaults to KINDSHIP_API_URL env var or https://kindship.ai)")
}
