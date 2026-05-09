package cmd

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/executor"
	"github.com/kindship-ai/kindship-cli/internal/logging"
	"github.com/spf13/cobra"
)

var heartbeatCmd = &cobra.Command{
	Use:   "heartbeat",
	Short: "Run agent heartbeat operations",
}

var heartbeatRunCmd = &cobra.Command{
	Use:   "run [schedule-id]",
	Short: "Execute a heartbeat schedule",
	Args:  cobra.ExactArgs(1),
	RunE:  runHeartbeatExecute,
}

func runHeartbeatExecute(cmd *cobra.Command, args []string) error {
	scheduleID := args[0]

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

	log := logging.Init(agentID, "heartbeat", verbose)
	defer log.FlushSync()

	if agentID == "" {
		log.Error("AGENT_ID not provided", nil)
		return fmt.Errorf("AGENT_ID is required (use --agent-id flag or AGENT_ID environment variable)")
	}
	if serviceKey == "" {
		log.Error("KINDSHIP_SERVICE_KEY not provided", nil)
		return fmt.Errorf("KINDSHIP_SERVICE_KEY is required (use --service-key flag or KINDSHIP_SERVICE_KEY environment variable)")
	}

	client := api.NewClient(apiURL, verbose)
	success, err := executeHeartbeat(scheduleID, agentID, serviceKey, sessionID, resume, client, log)
	if err != nil {
		return err
	}
	if !success {
		os.Exit(1)
	}
	return nil
}

func executeHeartbeat(scheduleID, agentID, serviceKey, sessionID string, resume bool, client *api.Client, log *logging.Logger) (bool, error) {
	startTime := time.Now()

	entityResp, err := client.FetchHeartbeatForExecution(scheduleID, serviceKey)
	if err != nil {
		log.Error("Failed to fetch heartbeat", err)
		return false, fmt.Errorf("failed to fetch heartbeat: %w", err)
	}

	startResp, err := client.StartHeartbeatExecution(api.HeartbeatExecutionStartRequest{
		ScheduleID: scheduleID,
		AgentID:    agentID,
		CLI:        os.Getenv("INNER_LOOP_CLI"),
		SessionID:  sessionID,
	}, serviceKey)
	if err != nil {
		log.Error("Failed to start heartbeat execution", err)
		return false, fmt.Errorf("failed to start heartbeat execution: %w", err)
	}

	logSender := func(lines []api.LogLine) error {
		return client.SendLogLines(startResp.ExecutionID, lines, serviceKey)
	}

	var sharedSeq atomic.Int64
	systemSend := func(content string) {
		_ = logSender([]api.LogLine{{
			Seq:     sharedSeq.Add(1),
			Stream:  "system",
			Content: content,
			Ts:      time.Now().UTC().Format(time.RFC3339Nano),
		}})
	}

	systemSend(fmt.Sprintf("Starting: %s (%s)", entityResp.Entity.Title, entityResp.Entity.ExecutionMode))

	result := executor.ExecuteAgentStreaming(
		&entityResp.Entity,
		startResp.Inputs,
		client,
		serviceKey,
		logSender,
		&sharedSeq,
		sessionID,
		resume,
		false,
	)

	execDuration := time.Since(startTime)
	if result.Success {
		systemSend(fmt.Sprintf("Completed successfully (exit code 0, %s)", execDuration.Truncate(time.Millisecond)))
	} else if result.Error != nil {
		systemSend(fmt.Sprintf("Failed (exit code %d, %s): %s", result.ExitCode, execDuration.Truncate(time.Millisecond), result.Error.Error()))
	} else {
		systemSend(fmt.Sprintf("Failed (exit code %d, %s)", result.ExitCode, execDuration.Truncate(time.Millisecond)))
	}

	status := api.ExecutionAttemptStatusSuccess
	completeReq := api.ExecutionCompleteRequest{
		Status: status,
		Outputs: &api.ExecutionOutputs{
			Stdout: result.Stdout,
			Stderr: result.Stderr,
			Metrics: map[string]interface{}{
				"duration_ms": execDuration.Milliseconds(),
				"exit_code":   result.ExitCode,
			},
		},
	}

	if !result.Success {
		status = api.ExecutionAttemptStatusFailed
		completeReq.Status = status
		failureMsg := fmt.Sprintf("Execution failed with exit code %d", result.ExitCode)
		if result.Error != nil {
			failureMsg = result.Error.Error()
		}
		completeReq.FailureReason = &failureMsg
	}

	_, err = client.CompleteHeartbeatExecution(startResp.ExecutionID, completeReq, serviceKey)
	if err != nil {
		log.Error("Failed to complete heartbeat execution", err)
		return false, fmt.Errorf("failed to complete heartbeat execution: %w", err)
	}

	if result.Success {
		if hb := os.Getenv("KINDSHIP_HEARTBEAT_FILE"); hb != "" {
			commitHeartbeatPicker(hb, log)
		}
	}

	log.WithDuration("Heartbeat command completed", execDuration, map[string]interface{}{
		"success":      result.Success,
		"execution_id": startResp.ExecutionID,
	})

	return result.Success, nil
}

func init() {
	heartbeatRunCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose logging for debugging")
	heartbeatRunCmd.Flags().StringVar(&agentID, "agent-id", "", "Agent container ID (defaults to AGENT_ID env var)")
	heartbeatRunCmd.Flags().StringVar(&serviceKey, "service-key", "", "Service key for authentication (defaults to KINDSHIP_SERVICE_KEY env var)")
	heartbeatRunCmd.Flags().StringVar(&apiURL, "api-url", "", "API base URL (defaults to KINDSHIP_API_URL env var or https://kindship.ai)")
	heartbeatRunCmd.Flags().StringVar(&sessionID, "session-id", "", "Claude Code session ID for session continuity")
	heartbeatRunCmd.Flags().BoolVar(&resume, "resume", false, "Resume a session. With --session-id: Claude (resume that id). Without: non-Claude CLIs pick their 'continue last' flag.")

	heartbeatCmd.AddCommand(heartbeatRunCmd)
	rootCmd.AddCommand(heartbeatCmd)
}
