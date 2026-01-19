package cmd

import (
	"fmt"
	"os"

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
	statusJSON bool
)

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "Output in JSON format")
	rootCmd.AddCommand(statusCmd)
}

type StatusOutput struct {
	Authenticated  bool   `json:"authenticated"`
	AuthMethod     string `json:"auth_method,omitempty"`
	UserEmail      string `json:"user_email,omitempty"`
	TokenExpiry    string `json:"token_expiry,omitempty"`
	InRepo         bool   `json:"in_repo"`
	RepoRoot       string `json:"repo_root,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	AgentSlug      string `json:"agent_slug,omitempty"`
	AccountID      string `json:"account_id,omitempty"`
	BoundAt        string `json:"bound_at,omitempty"`
	APIBaseURL     string `json:"api_base_url,omitempty"`
	HooksInstalled bool   `json:"hooks_installed"`
	Error          string `json:"error,omitempty"`
}

func runStatus(cmd *cobra.Command, args []string) error {
	output := StatusOutput{}

	// Check authentication
	ctx := auth.GetAuthContextOrNil()
	if ctx != nil {
		output.Authenticated = true
		output.AuthMethod = string(ctx.Method)
		output.UserEmail = ctx.UserEmail
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

	// API section
	if output.APIBaseURL != "" {
		fmt.Printf("API: %s\n", output.APIBaseURL)
	}

	return nil
}

func checkHooksInstalled(repoRoot string) bool {
	startHookPath := repoRoot + "/.claude/hooks/start.yaml"
	stopHookPath := repoRoot + "/.claude/hooks/stop.yaml"

	if !fileExists(startHookPath) {
		return false
	}
	if !fileExists(stopHookPath) {
		return false
	}
	return true
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
