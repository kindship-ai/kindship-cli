package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/logging"
	"github.com/spf13/cobra"
)

// activeResumes tracks active resume goroutines by run ID to prevent duplicates.
var activeResumes sync.Map

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Agent container commands",
	Long: `Commands for agent containers running on infrastructure.

Subcommands:
  loop     Run autonomous execution loop`,
}

var loopCmd = &cobra.Command{
	Use:   "loop",
	Short: "Run autonomous execution loop",
	Long: `Continuously polls for runnable tasks and executes them.

Runs inside agent containers. Automatically:
- Recovers RUNNING runs on startup (resumes ORCHESTRATE, fails leaf runs)
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

func init() {
	loopCmd.Flags().IntVar(&pollInterval, "poll-interval", 30, "Seconds between idle polls")
	loopCmd.Flags().StringVar(&agentID, "agent-id", "", "Agent ID")
	loopCmd.Flags().StringVar(&serviceKey, "service-key", "", "Service key")
	loopCmd.Flags().StringVar(&apiURL, "api-url", "", "API base URL")
	loopCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Verbose logging")

	agentCmd.AddCommand(loopCmd)
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

	// Step 1: Recover runs from previous loop instance
	log.Info("Recovering runs from previous loop instance")
	recoverResp, err := client.RecoverRuns(agentID, serviceKey)
	if err != nil {
		log.Error("Failed to recover runs", err)
		// Non-fatal — continue loop startup
	} else {
		log.Info("Run recovery complete", map[string]interface{}{
			"resumed_count":    len(recoverResp.ResumedRuns),
			"failed_count":     recoverResp.FailedCount,
			"skipped_ask_user": recoverResp.SkippedAskUser,
		})

		// Resume ORCHESTRATE runs in background goroutines
		for _, resumed := range recoverResp.ResumedRuns {
			if resumed.ExecutionMode == string(api.ExecutionModeOrchestrate) {
				runID := resumed.RunID
				if _, loaded := activeResumes.LoadOrStore(runID, true); loaded {
					log.Info("Resume already active, skipping", map[string]interface{}{
						"run_id": runID,
					})
					continue
				}
				go func(entityID, runID string) {
					defer activeResumes.Delete(runID)
					if resumeErr := resumeOrchestration(entityID, runID, client, log); resumeErr != nil {
						log.Error("Failed to resume ORCHESTRATE run", resumeErr, map[string]interface{}{
							"entity_id": entityID,
							"run_id":    runID,
						})
					}
				}(resumed.EntityID, runID)
			}
		}
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

