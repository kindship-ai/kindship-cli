package cmd

import (
	"fmt"
	"os"

	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/kindship-ai/kindship-cli/internal/config"

	"github.com/spf13/cobra"
)

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Display current authentication status",
	Long: `Display information about the current authentication status.

Shows the authenticated user email, token prefix, and configured agent
if in a repository with Kindship setup.

Examples:
  kindship whoami`,
	RunE: runWhoami,
}

var (
	whoamiJSON bool
)

func init() {
	whoamiCmd.Flags().BoolVar(&whoamiJSON, "json", false, "Output in JSON format")
	rootCmd.AddCommand(whoamiCmd)
}

type WhoamiOutput struct {
	Authenticated bool   `json:"authenticated"`
	Method        string `json:"method,omitempty"`
	UserEmail     string `json:"user_email,omitempty"`
	UserID        string `json:"user_id,omitempty"`
	TokenPrefix   string `json:"token_prefix,omitempty"`
	TokenExpiry   string `json:"token_expiry,omitempty"`
	AgentID       string `json:"agent_id,omitempty"`
	APIBaseURL    string `json:"api_base_url,omitempty"`
	Error         string `json:"error,omitempty"`
}

func runWhoami(cmd *cobra.Command, args []string) error {
	output := WhoamiOutput{}

	ctx, err := auth.GetAuthContext()
	if err != nil {
		output.Authenticated = false
		output.Error = err.Error()

		if whoamiJSON {
			return printJSON(output)
		}

		// Try to give helpful guidance
		if os.Getenv("KINDSHIP_SERVICE_KEY") == "" {
			fmt.Println("Not authenticated.")
			fmt.Println("Run 'kindship login' to authenticate.")
		} else {
			fmt.Printf("Authentication error: %v\n", err)
		}
		return nil
	}

	output.Authenticated = true
	output.Method = string(ctx.Method)
	output.UserEmail = ctx.UserEmail
	output.UserID = ctx.UserID
	output.TokenPrefix = ctx.TokenPrefix
	output.AgentID = ctx.AgentID
	output.APIBaseURL = ctx.APIBaseURL

	if !ctx.TokenExpiry.IsZero() {
		output.TokenExpiry = ctx.TokenExpiry.Format("2006-01-02 15:04:05")
	}

	if whoamiJSON {
		return printJSON(output)
	}

	// Human-readable output
	switch ctx.Method {
	case auth.AuthMethodOAuth:
		fmt.Printf("Logged in as: %s\n", ctx.UserEmail)
		fmt.Printf("Token prefix: %s...\n", ctx.TokenPrefix)
		if !ctx.TokenExpiry.IsZero() {
			fmt.Printf("Token expires: %s\n", ctx.TokenExpiry.Format("2006-01-02 15:04:05"))
		}
	case auth.AuthMethodServiceKey:
		fmt.Println("Running in container mode (service key)")
		if ctx.AgentID != "" {
			fmt.Printf("Agent ID: %s\n", ctx.AgentID)
		}
	}

	if ctx.AgentID != "" && ctx.Method == auth.AuthMethodOAuth {
		fmt.Printf("\nCurrent agent: %s\n", ctx.AgentID)
	} else if ctx.AgentID == "" && ctx.Method == auth.AuthMethodOAuth {
		// Try to load repo config for more context
		repoConfig, err := config.LoadRepoConfig()
		if err != nil {
			fmt.Println("\nNo agent configured for this repository.")
			fmt.Println("Run 'kindship setup' to link an agent.")
		} else {
			fmt.Printf("\nCurrent agent: %s\n", repoConfig.AgentID)
			if repoConfig.AgentSlug != "" {
				fmt.Printf("Agent slug: %s\n", repoConfig.AgentSlug)
			}
		}
	}

	fmt.Printf("\nAPI: %s\n", ctx.APIBaseURL)

	return nil
}
