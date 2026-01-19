package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/config"

	"github.com/spf13/cobra"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out from Kindship",
	Long: `Log out from the Kindship CLI and revoke the current token.

By default, only the current token is revoked. Use --all to revoke
all tokens for your account (useful after a security incident).

Examples:
  kindship logout           # Revoke current token
  kindship logout --all     # Revoke all tokens`,
	RunE: runLogout,
}

var (
	logoutAll bool
)

func init() {
	logoutCmd.Flags().BoolVar(&logoutAll, "all", false, "Revoke all tokens for your account")
	rootCmd.AddCommand(logoutCmd)
}

func runLogout(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadGlobalConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if cfg.Token == "" {
		fmt.Println("Not currently logged in.")
		return nil
	}

	// Try to revoke token on server (best effort)
	if err := revokeToken(cfg, logoutAll); err != nil {
		// Don't fail logout if server revocation fails
		fmt.Fprintf(os.Stderr, "Warning: Failed to revoke token on server: %v\n", err)
	}

	// Clear local config
	if err := config.ClearGlobalConfig(); err != nil {
		return fmt.Errorf("failed to clear config: %w", err)
	}

	if logoutAll {
		fmt.Println("✓ Logged out and revoked all tokens")
	} else {
		fmt.Println("✓ Logged out successfully")
	}

	return nil
}

func revokeToken(cfg *config.GlobalConfig, all bool) error {
	endpoint := fmt.Sprintf("%s/api/cli/auth/revoke", cfg.GetAPIBaseURL())
	if all {
		endpoint += "?all=true"
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", cfg.Token))
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error (%d): %s", resp.StatusCode, string(body))
	}

	return nil
}
