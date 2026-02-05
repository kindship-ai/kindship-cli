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
	"github.com/kindship-ai/kindship-cli/internal/logging"
	"github.com/spf13/cobra"
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Agent container commands",
	Long: `Commands for agent containers running on infrastructure.

Subcommands:
  loop     Run autonomous execution loop
  run      Execute all tasks in a Process`,
}

var loopCmd = &cobra.Command{
	Use:   "loop",
	Short: "Run autonomous execution loop",
	Long: `Continuously polls for runnable tasks and executes them.

Runs inside agent containers. Automatically:
- Abandons stale RUNNING runs on startup
- Polls for next task at configurable interval
- Dispatches execution by mode (LLM, Bash, Python, etc.)
- Sleeps when no tasks are available

Configuration:
  --poll-interval  Seconds between idle polls (default: 30)
  --api-url        API base URL (env: KINDSHIP_API_URL)
  --service-key    Service key (env: KINDSHIP_SERVICE_KEY)
  --agent-id       Agent ID (env: AGENT_ID)`,
	RunE: runLoop,
}

var pollInterval int
var entityUUID string

var agentRunCmd = &cobra.Command{
	Use:   "run --entity <entity-uuid>",
	Short: "Execute all tasks in a Process",
	Long: `Execute all tasks within a Process entity by repeatedly polling
for runnable tasks scoped to that Process, executing them, and exiting
when no more tasks remain.

This command is designed for ephemeral execution of recurring Processes
triggered by the scheduler. It creates a Run for the Process itself and
tracks execution of all child tasks.

Configuration (flags take precedence over environment variables):
  --entity - Process entity UUID (required)
  --agent-id / AGENT_ID - The agent container ID
  --service-key / KINDSHIP_SERVICE_KEY - Service key for authentication
  --api-url / KINDSHIP_API_URL - API base URL

Examples:
  kindship agent run --entity 550e8400-e29b-41d4-a716-446655440000`,
	RunE: runAgentRun,
}

func init() {
	loopCmd.Flags().IntVar(&pollInterval, "poll-interval", 30, "Seconds between idle polls")
	loopCmd.Flags().StringVar(&agentID, "agent-id", "", "Agent ID")
	loopCmd.Flags().StringVar(&serviceKey, "service-key", "", "Service key")
	loopCmd.Flags().StringVar(&apiURL, "api-url", "", "API base URL")
	loopCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Verbose logging")

	agentRunCmd.Flags().StringVar(&entityUUID, "entity", "", "Process entity UUID (required)")
	agentRunCmd.MarkFlagRequired("entity")
	agentRunCmd.Flags().StringVar(&agentID, "agent-id", "", "Agent ID")
	agentRunCmd.Flags().StringVar(&serviceKey, "service-key", "", "Service key")
	agentRunCmd.Flags().StringVar(&apiURL, "api-url", "", "API URL")
	agentRunCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Verbose logging")

	agentCmd.AddCommand(loopCmd)
	agentCmd.AddCommand(agentRunCmd)
	rootCmd.AddCommand(agentCmd)
}

func runLoop(cmd *cobra.Command, args []string) error {
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

	// Initialize logging with agent-loop component
	log := logging.Init(agentID, "agent-loop", verbose)
	log.SetComponent("agent-loop")
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

	// Step 1: Abandon stale runs on startup
	log.Info("Abandoning stale runs from previous loop instance")
	abandonResp, err := client.AbandonStaleRuns(agentID, serviceKey)
	if err != nil {
		log.Error("Failed to abandon stale runs", err)
		// Non-fatal — continue loop startup
	} else if abandonResp.AbandonedCount > 0 {
		log.Info("Abandoned stale runs", map[string]interface{}{
			"abandoned_count": abandonResp.AbandonedCount,
		})
	}

	log.Info("Loop started", map[string]interface{}{
		"agent_id":      agentID,
		"poll_interval": pollInterval,
		"api_url":       apiURL,
	})
	log.Flush()

	pollDuration := time.Duration(pollInterval) * time.Second
	iterationCount := 0

	// Main loop
	for {
		select {
		case <-ctx.Done():
			log.Info("Shutting down loop (signal received)", map[string]interface{}{
				"iterations": iterationCount,
			})
			return nil
		default:
		}

		iterationCount++

		// Fetch next task
		nextResp, err := client.FetchNextTask(agentID, serviceKey)
		if err != nil {
			log.Error("Failed to fetch next task", err, map[string]interface{}{
				"iteration": iterationCount,
			})
			if sleepWithContext(ctx, pollDuration) {
				return nil
			}
			continue
		}

		// No task available — sleep
		if nextResp.Task == nil {
			log.Debug("No runnable tasks, sleeping", map[string]interface{}{
				"poll_interval_s": pollInterval,
				"pending_count":   nextResp.PendingCount,
				"iteration":       iterationCount,
			})
			if sleepWithContext(ctx, pollDuration) {
				return nil
			}
			continue
		}

		// Execute task
		task := nextResp.Task
		log.Info("Executing task", map[string]interface{}{
			"task_id":        task.ID,
			"task_title":     task.Title,
			"execution_mode": task.ExecutionMode,
			"iteration":      iterationCount,
		})

		success, err := executeEntity(EntityExecutionParams{
			EntityID:   task.ID,
			AgentID:    agentID,
			ServiceKey: serviceKey,
			Client:     client,
			Log:        log,
		})

		if err != nil {
			if errors.Is(err, ErrAskUserSkipped) {
				log.Info("ASK_USER task started, continuing to next task", map[string]interface{}{
					"task_id": task.ID,
				})
			} else {
				log.Error("Task execution error", err, map[string]interface{}{
					"task_id": task.ID,
				})
			}
			// Don't exit — continue loop
		} else {
			log.Info("Task completed", map[string]interface{}{
				"task_id": task.ID,
				"success": success,
			})
		}

		// Flush logs after each task execution
		log.Flush()

		// Immediately check for next task (no sleep after successful execution)
	}
}

// sleepWithContext sleeps for the given duration but returns early if the
// context is cancelled. Returns true if context was cancelled.
func sleepWithContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-timer.C:
		return false
	}
}

func runAgentRun(cmd *cobra.Command, args []string) error {
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
	log := logging.Init(agentID, "agent-run", verbose)
	defer log.FlushSync()

	// Validate required parameters
	if agentID == "" {
		log.Error("AGENT_ID not provided", nil)
		return fmt.Errorf("agent-id is required (via --agent-id or AGENT_ID)")
	}
	if serviceKey == "" {
		log.Error("KINDSHIP_SERVICE_KEY not provided", nil)
		return fmt.Errorf("service-key is required (via --service-key or KINDSHIP_SERVICE_KEY)")
	}

	client := api.NewClient(apiURL, verbose)

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

	// Fetch and validate Process entity
	entityResp, err := client.FetchEntityForExecution(entityUUID, serviceKey)
	if err != nil {
		log.Error("Failed to fetch entity", err, map[string]interface{}{
			"entity_id": entityUUID,
		})
		return fmt.Errorf("failed to fetch entity: %w", err)
	}

	if entityResp.Entity.Type != "PROCESS" {
		log.Error("Entity is not a PROCESS", nil, map[string]interface{}{
			"entity_id":   entityUUID,
			"entity_type": entityResp.Entity.Type,
		})
		return fmt.Errorf("entity %s is not a PROCESS (type=%s)", entityUUID, entityResp.Entity.Type)
	}

	log.Info("Starting Process execution", map[string]interface{}{
		"entity_id":    entityUUID,
		"entity_title": entityResp.Entity.Title,
	})

	// Create Run for the Process entity
	startReq := api.ExecutionStartRequest{
		EntityID:      entityUUID,
		ExecutionMode: entityResp.Entity.ExecutionMode,
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
		nextResp, err := client.FetchNextTaskForProcess(agentID, entityUUID, serviceKey)
		if err != nil {
			log.Error("Failed to fetch next task", err, nil)
			lastError = err
			break
		}

		// No more tasks - Process complete
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
