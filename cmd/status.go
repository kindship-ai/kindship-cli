package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/kindship-ai/kindship-cli/internal/config"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current repository and agent status",
	Long: `Display the current status of the Kindship CLI configuration.

Shows:
- Authentication status
- Current repository binding (if any)
- Agent information

Examples:
  kindship status
  kindship status --json`,
	RunE: runStatus,
}

var (
	statusJSON  bool
	statusLocal bool
)

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "Output in JSON format")
	statusCmd.Flags().BoolVar(&statusLocal, "local", false, "Include local dev loop status (active run, hook config)")
	rootCmd.AddCommand(statusCmd)
}

type StatusOutput struct {
	Authenticated  bool             `json:"authenticated"`
	AuthMethod     string           `json:"auth_method,omitempty"`
	UserEmail      string           `json:"user_email,omitempty"`
	UserID         string           `json:"user_id,omitempty"`
	TokenPrefix    string           `json:"token_prefix,omitempty"`
	TokenExpiry    string           `json:"token_expiry,omitempty"`
	InRepo         bool             `json:"in_repo"`
	RepoRoot       string           `json:"repo_root,omitempty"`
	AgentID        string           `json:"agent_id,omitempty"`
	AgentSlug      string           `json:"agent_slug,omitempty"`
	AccountID      string           `json:"account_id,omitempty"`
	BoundAt        string           `json:"bound_at,omitempty"`
	APIBaseURL     string           `json:"api_base_url,omitempty"`
	HooksInstalled bool             `json:"hooks_installed"`
	ActiveRun      *ActiveRunStatus `json:"active_run,omitempty"`
	Error          string           `json:"error,omitempty"`
}

type ActiveRunStatus struct {
	EntityID      string `json:"entity_id"`
	RunID         string `json:"run_id"`
	TaskTitle     string `json:"task_title,omitempty"`
	ExecutionMode string `json:"execution_mode,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
}

func runStatus(cmd *cobra.Command, args []string) error {
	output := StatusOutput{}

	// Check authentication
	ctx := auth.GetAuthContextOrNil()
	if ctx != nil {
		output.Authenticated = true
		output.AuthMethod = string(ctx.Method)
		output.UserEmail = ctx.UserEmail
		output.UserID = ctx.UserID
		output.TokenPrefix = ctx.TokenPrefix
		output.APIBaseURL = ctx.APIBaseURL
		if !ctx.TokenExpiry.IsZero() {
			output.TokenExpiry = ctx.TokenExpiry.Format("2006-01-02 15:04:05")
		}
	}

	// Check repository
	repoRoot, err := config.FindRepoRoot()
	if err == nil {
		output.InRepo = true
		output.RepoRoot = repoRoot

		// Check for kindship config
		repoConfig, err := config.LoadRepoConfig()
		if err == nil {
			output.AgentID = repoConfig.AgentID
			output.AgentSlug = repoConfig.AgentSlug
			output.AccountID = repoConfig.AccountID
			if !repoConfig.BoundAt.IsZero() {
				output.BoundAt = repoConfig.BoundAt.Format("2006-01-02 15:04:05")
			}
		}

		// Check for Claude Code hooks
		output.HooksInstalled = checkHooksInstalled(repoRoot)

		// Local dev loop status: active run file.
		if statusLocal {
			if active, err := loadActiveRun(repoRoot); err == nil && active != nil && active.RunID != "" {
				ar := &ActiveRunStatus{
					EntityID:      active.EntityID,
					RunID:         active.RunID,
					TaskTitle:     active.TaskTitle,
					ExecutionMode: active.ExecutionMode,
				}
				if !active.StartedAt.IsZero() {
					ar.StartedAt = active.StartedAt.Format("2006-01-02 15:04:05")
				}
				output.ActiveRun = ar
			}
		}
	}

	if statusJSON {
		return printJSON(output)
	}

	// Human-readable output
	fmt.Println("Kindship CLI Status")
	fmt.Println("===================")
	fmt.Println()

	// Authentication section
	fmt.Println("Authentication:")
	if output.Authenticated {
		if output.AuthMethod == "oauth" {
			fmt.Printf("  ✓ Logged in as %s\n", output.UserEmail)
			if output.TokenPrefix != "" {
				fmt.Printf("  Token: %s...\n", output.TokenPrefix)
			}
			if output.TokenExpiry != "" {
				fmt.Printf("  Token expires: %s\n", output.TokenExpiry)
			}
		} else {
			fmt.Println("  ✓ Running in container mode (service key)")
		}
	} else {
		fmt.Println("  ✗ Not authenticated")
		fmt.Println("    Run 'kindship login' to authenticate")
	}
	fmt.Println()

	// Repository section
	fmt.Println("Repository:")
	if output.InRepo {
		fmt.Printf("  ✓ Git repository: %s\n", output.RepoRoot)

		if output.AgentID != "" {
			fmt.Printf("  ✓ Agent bound: %s\n", output.AgentID)
			if output.AgentSlug != "" {
				fmt.Printf("    Slug: %s\n", output.AgentSlug)
			}
			if output.BoundAt != "" {
				fmt.Printf("    Bound at: %s\n", output.BoundAt)
			}
		} else {
			fmt.Println("  ✗ No agent configured")
			fmt.Println("    Run 'kindship setup' to link an agent")
		}
	} else {
		fmt.Println("  ✗ Not in a git repository")
	}
	fmt.Println()

	// Hooks section
	if output.InRepo {
		fmt.Println("Claude Code Integration:")
		if output.HooksInstalled {
			fmt.Println("  ✓ Hooks installed")
		} else {
			fmt.Println("  ✗ Hooks not installed")
			fmt.Println("    Run 'kindship setup' to install hooks")
		}
		fmt.Println()
	}

	if statusLocal && output.InRepo {
		fmt.Println("Local Dev Loop:")
		if output.ActiveRun != nil {
			fmt.Printf("  ✓ Active run: %s\n", output.ActiveRun.RunID)
			fmt.Printf("    Entity: %s\n", output.ActiveRun.EntityID)
			if output.ActiveRun.TaskTitle != "" {
				fmt.Printf("    Task: %s\n", output.ActiveRun.TaskTitle)
			}
			if output.ActiveRun.ExecutionMode != "" {
				fmt.Printf("    Mode: %s\n", output.ActiveRun.ExecutionMode)
			}
			if output.ActiveRun.StartedAt != "" {
				fmt.Printf("    Started: %s\n", output.ActiveRun.StartedAt)
			}
		} else {
			fmt.Println("  ✗ No active run")
		}
		fmt.Println()
	}

	// API section
	if output.APIBaseURL != "" {
		fmt.Printf("API: %s\n", output.APIBaseURL)
	}

	return nil
}

func checkHooksInstalled(repoRoot string) bool {
	if hooksInstalledSettingsLocalJSON(repoRoot) {
		return true
	}

	// Backward compatible: old YAML hook format.
	startHookPath := filepath.Join(repoRoot, ".claude", "hooks", "start.yaml")
	stopHookPath := filepath.Join(repoRoot, ".claude", "hooks", "stop.yaml")
	return fileExists(startHookPath) && fileExists(stopHookPath)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func hooksInstalledSettingsLocalJSON(repoRoot string) bool {
	path := filepath.Join(repoRoot, ".claude", "settings.local.json")
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return false
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok || hooks == nil {
		return false
	}

	return hookEventHasCommand(hooks, "SessionStart", "kindship hook start") &&
		hookEventHasCommand(hooks, "Stop", "kindship hook stop")
}

func hookEventHasCommand(hooks map[string]interface{}, event string, prefix string) bool {
	rawEntries, ok := hooks[event].([]interface{})
	if !ok {
		return false
	}
	for _, rawEntry := range rawEntries {
		entry, ok := rawEntry.(map[string]interface{})
		if !ok {
			continue
		}
		rawHooks, ok := entry["hooks"].([]interface{})
		if !ok {
			continue
		}
		for _, rawHook := range rawHooks {
			h, ok := rawHook.(map[string]interface{})
			if !ok {
				continue
			}
			if h["type"] != "command" {
				continue
			}
			cmd, _ := h["command"].(string)
			if strings.HasPrefix(cmd, prefix) {
				return true
			}
		}
	}
	return false
}
