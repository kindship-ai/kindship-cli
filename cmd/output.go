package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/kindship-ai/kindship-cli/internal/config"

	"github.com/spf13/cobra"
)

var outputCmd = &cobra.Command{
	Use:   "output",
	Short: "Store structured output data for the current run",
	Long: `Store structured data in the current run's outputs.

This data persists through run completion and is visible on the platform.
Multiple calls accumulate data (keys are merged, not replaced).

Examples:
  kindship output --key plan --file PLAN.md
  kindship output --key roadmap --file ROADMAP.md
  kindship output --json '{"tests_passing": 86}'
  kindship output --key summary --value "Cycle complete"`,
	RunE: runOutput,
}

var (
	outputKey      string
	outputFile     string
	outputJSON     string
	outputValue    string
	maxFileSize    = int64(64 * 1024) // 64KB
)

func init() {
	outputCmd.Flags().StringVar(&outputKey, "key", "", "Key to store the value under (used with --file or --value)")
	outputCmd.Flags().StringVar(&outputFile, "file", "", "Read file contents and store under --key")
	outputCmd.Flags().StringVar(&outputJSON, "json", "", "Merge a JSON object into structured output")
	outputCmd.Flags().StringVar(&outputValue, "value", "", "Store a string value under --key")
	rootCmd.AddCommand(outputCmd)
}

func runOutput(cmd *cobra.Command, args []string) error {
	// Validate flag combinations
	hasFile := outputFile != ""
	hasJSON := outputJSON != ""
	hasValue := outputValue != ""

	if !hasFile && !hasJSON && !hasValue {
		return fmt.Errorf("provide one of --file, --json, or --value")
	}

	if (hasFile || hasValue) && outputKey == "" {
		return fmt.Errorf("--key is required when using --file or --value")
	}

	// Build the structured data map
	structured := map[string]interface{}{}

	if hasFile {
		info, err := os.Stat(outputFile)
		if err != nil {
			return fmt.Errorf("cannot read file %s: %w", outputFile, err)
		}
		if info.Size() > maxFileSize {
			return fmt.Errorf("file %s is too large (%d bytes, max %d)", outputFile, info.Size(), maxFileSize)
		}
		content, err := os.ReadFile(outputFile)
		if err != nil {
			return fmt.Errorf("failed to read file %s: %w", outputFile, err)
		}
		structured[outputKey] = string(content)
	}

	if hasJSON {
		var jsonData map[string]interface{}
		if err := json.Unmarshal([]byte(outputJSON), &jsonData); err != nil {
			return fmt.Errorf("invalid --json value: %w", err)
		}
		for k, v := range jsonData {
			structured[k] = v
		}
	}

	if hasValue {
		structured[outputKey] = outputValue
	}

	// Get auth context
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return fmt.Errorf("not authenticated: %w", err)
	}

	// Get active run
	repoCfg, err := config.LoadRepoConfig()
	if err != nil {
		return fmt.Errorf("not in a configured repository (run 'kindship setup'): %w", err)
	}

	active, err := fetchActiveRun(ctx, repoCfg)
	if err != nil {
		return fmt.Errorf("failed to fetch active run: %w", err)
	}
	if active == nil {
		return fmt.Errorf("no active run — start a dev loop session first")
	}

	// Send to API
	client := api.NewClient(ctx.APIBaseURL, verbose)
	if err := client.UpdateRunOutput(active.RunID, structured, ctx.Token); err != nil {
		return fmt.Errorf("failed to update run output: %w", err)
	}

	fmt.Printf("✓ Output stored (%d keys)\n", len(structured))
	return nil
}
