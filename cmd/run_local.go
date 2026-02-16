package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/kindship-ai/kindship-cli/internal/config"
	"github.com/spf13/cobra"
)

var runLocalNextCmd = &cobra.Command{
	Use:   "local-next",
	Short: "Fetch and start the next Kindship task (local mode)",
	Long: `Fetches the next runnable task for the agent bound to this repository,
starts an execution run (RUNNING), and prints the task prompt as markdown.
Run state is stored in the database, not locally.`,
	RunE: runLocalNext,
}

var (
	localCompleteOutputs string
)

var runLocalCompleteCmd = &cobra.Command{
	Use:   "local-complete",
	Short: "Mark the current local Kindship run as SUCCESS",
	RunE:  runLocalComplete,
}

var (
	localFailReason  string
	localFailOutputs string
)

var runLocalFailCmd = &cobra.Command{
	Use:   "local-fail",
	Short: "Mark the current local Kindship run as FAILED",
	RunE:  runLocalFail,
}

func init() {
	runLocalCompleteCmd.Flags().StringVar(&localCompleteOutputs, "outputs", "", "Execution outputs JSON (api.ExecutionOutputs)")
	runLocalFailCmd.Flags().StringVar(&localFailReason, "reason", "", "Failure reason (required)")
	runLocalFailCmd.Flags().StringVar(&localFailOutputs, "outputs", "", "Execution outputs JSON (api.ExecutionOutputs)")

	runCmd.AddCommand(runLocalNextCmd)
	runCmd.AddCommand(runLocalCompleteCmd)
	runCmd.AddCommand(runLocalFailCmd)
}

func requireLocalRepoContext() (*auth.Context, *config.RepoConfig, string, error) {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return nil, nil, "", err
	}
	if !ctx.IsLocalMode() {
		return nil, nil, "", fmt.Errorf("local commands require OAuth auth (run 'kindship login' locally; unset KINDSHIP_SERVICE_KEY if set)")
	}

	repoRoot, err := config.FindRepoRoot()
	if err != nil {
		return nil, nil, "", fmt.Errorf("not in a git repository: %w", err)
	}

	repoCfg, err := config.LoadRepoConfig()
	if err != nil {
		return nil, nil, "", fmt.Errorf("no agent configured for this repository (run 'kindship setup'): %w", err)
	}

	return ctx, repoCfg, repoRoot, nil
}

func runLocalNext(cmd *cobra.Command, args []string) error {
	ctx, repoCfg, _, err := requireLocalRepoContext()
	if err != nil {
		return err
	}

	// Check for existing active run from DB (staleness handled server-side).
	active, err := fetchActiveRun(ctx, repoCfg)
	if err != nil {
		return err
	}

	// Process loop mode: delegate to dev loop helpers.
	if repoCfg.ProcessID != "" {
		if active != nil && active.IsProcessLoop() {
			// Resume existing process task.
			if active.Task == nil {
				return fmt.Errorf("active process run has no task payload")
			}
			_, markdown, err := startDevLoopTask(ctx, repoCfg, active.ProcessRunID, active.ProcessTasks, *active.Task, "")
			if err != nil {
				return fmt.Errorf("resume dev loop task: %w", err)
			}
			fmt.Print(markdown)
			return nil
		}
		// Start fresh cycle.
		_, markdown, err := startDevLoopCycle(ctx, repoCfg, "")
		if err != nil {
			return fmt.Errorf("start dev loop cycle: %w", err)
		}
		fmt.Print(markdown)
		return nil
	}

	// Non-process mode: resume or claim individual tasks.
	if active != nil {
		// Don't claim another task; require explicit completion/failure.
		if active.Task != nil {
			fmt.Print(formatKindshipTaskMarkdown(repoCfg.AgentSlug, active.Task, active.RunID, active.ExecutionMode, active))
			return nil
		}
		fmt.Printf("Active Kindship run detected.\nEntity: %s | Run: %s | Mode: %s\n\nUse `/kindship complete` or `/kindship fail --reason \"...\"`.\n", active.EntityID, active.RunID, active.ExecutionMode)
		return nil
	}

	task, err := fetchNextTask(ctx, repoCfg.AgentID)
	if err != nil {
		return err
	}
	if task == nil {
		fmt.Println("No pending Kindship tasks.")
		return nil
	}

	run, err := startLocalExecutionForTask(ctx, repoCfg, task, "")
	if err != nil {
		return err
	}

	fmt.Print(formatKindshipTaskMarkdown(repoCfg.AgentSlug, task, run.RunID, run.ExecutionMode, nil))
	return nil
}

func runLocalComplete(cmd *cobra.Command, args []string) error {
	ctx, repoCfg, _, err := requireLocalRepoContext()
	if err != nil {
		return err
	}

	active, err := fetchActiveRun(ctx, repoCfg)
	if err != nil {
		return err
	}
	if active == nil || active.RunID == "" {
		return fmt.Errorf("no active run found (nothing to complete)")
	}

	outputs, err := parseExecutionOutputsJSON(localCompleteOutputs)
	if err != nil {
		return err
	}

	// Process loop mode: advance to next task or new cycle.
	if active.IsProcessLoop() {
		markdown, shouldBlock, err := advanceDevLoop(ctx, repoCfg, active, outputs)
		if err != nil {
			return fmt.Errorf("advance dev loop: %w", err)
		}
		if shouldBlock {
			fmt.Print(markdown)
		} else {
			if strings.TrimSpace(markdown) != "" {
				fmt.Println(markdown)
			} else {
				fmt.Println("Dev loop cycle completed.")
			}
		}
		return nil
	}

	// Non-process mode: complete run.
	req := api.ExecutionCompleteRequest{
		Status: api.ExecutionAttemptStatusSuccess,
	}
	if outputs != nil {
		req.Outputs = outputs
	}

	client := api.NewClient(ctx.APIBaseURL, verbose)
	if _, err := client.CompleteExecutionWithBearer(active.RunID, req, ctx.Token); err != nil {
		return err
	}

	fmt.Printf("Completed Kindship run %s (entity %s).\n", active.RunID, active.EntityID)
	return nil
}

func runLocalFail(cmd *cobra.Command, args []string) error {
	ctx, repoCfg, _, err := requireLocalRepoContext()
	if err != nil {
		return err
	}
	if localFailReason == "" {
		return fmt.Errorf("--reason is required")
	}

	active, err := fetchActiveRun(ctx, repoCfg)
	if err != nil {
		return err
	}
	if active == nil || active.RunID == "" {
		return fmt.Errorf("no active run found (nothing to fail)")
	}

	// Process loop mode: fail current task.
	if active.IsProcessLoop() {
		if err := failDevLoop(ctx, active, localFailReason); err != nil {
			return fmt.Errorf("fail dev loop task: %w", err)
		}
		fmt.Fprintf(os.Stdout, "Failed dev loop task %s (entity %s): %s\n", active.RunID, active.EntityID, localFailReason)
		return nil
	}

	// Non-process mode: fail run.
	outputs, err := parseExecutionOutputsJSON(localFailOutputs)
	if err != nil {
		return err
	}

	reason := localFailReason
	req := api.ExecutionCompleteRequest{
		Status:        api.ExecutionAttemptStatusFailed,
		FailureReason: &reason,
	}
	if outputs != nil {
		req.Outputs = outputs
	}

	client := api.NewClient(ctx.APIBaseURL, verbose)
	if _, err := client.CompleteExecutionWithBearer(active.RunID, req, ctx.Token); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "Failed Kindship run %s (entity %s): %s\n", active.RunID, active.EntityID, reason)
	return nil
}
