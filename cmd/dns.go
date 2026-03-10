package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"

	"github.com/spf13/cobra"
)

var dnsCmd = &cobra.Command{
	Use:   "dns",
	Short: "DNS management commands",
	Long: `Commands for managing DNS provider credentials.

Subcommands:
  credentials   Manage DNS provider API credentials`,
}

var dnsCredentialsCmd = &cobra.Command{
	Use:   "credentials",
	Short: "Manage DNS provider credentials",
	Long: `Store and manage API credentials for DNS providers (Gandi, Spaceship).
These credentials allow automatic CNAME creation when setting custom domains.

Subcommands:
  set      Store credentials for a DNS provider
  check    List configured DNS providers
  remove   Remove credentials for a DNS provider`,
}

var dnsCredentialsSetCmd = &cobra.Command{
	Use:   "set <provider>",
	Short: "Store DNS provider credentials",
	Long: `Store API credentials for a DNS provider.

Supported providers:
  gandi      - Requires --pat (Personal Access Token)
  spaceship  - Requires --api-key and --api-secret

Examples:
  kindship dns credentials set gandi --pat glpat-xxxxxxxxxxxx
  kindship dns credentials set spaceship --api-key KEY --api-secret SECRET`,
	Args: cobra.ExactArgs(1),
	RunE: runDnsCredentialsSet,
}

var dnsCredentialsCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "List configured DNS providers",
	Long: `Show which DNS providers have credentials configured.

Examples:
  kindship dns credentials check`,
	RunE: runDnsCredentialsCheck,
}

var dnsCredentialsRemoveCmd = &cobra.Command{
	Use:   "remove <provider>",
	Short: "Remove DNS provider credentials",
	Long: `Remove stored credentials for a DNS provider.

Examples:
  kindship dns credentials remove gandi`,
	Args: cobra.ExactArgs(1),
	RunE: runDnsCredentialsRemove,
}

var (
	dnsFormat       string
	dnsPat          string
	dnsApiKey       string
	dnsApiSecret    string
)

func init() {
	dnsCredentialsSetCmd.Flags().StringVar(&dnsPat, "pat", "", "Personal Access Token (for Gandi)")
	dnsCredentialsSetCmd.Flags().StringVar(&dnsApiKey, "api-key", "", "API Key (for Spaceship)")
	dnsCredentialsSetCmd.Flags().StringVar(&dnsApiSecret, "api-secret", "", "API Secret (for Spaceship)")
	dnsCredentialsSetCmd.Flags().StringVar(&dnsFormat, "format", "text", "Output format (json, text)")
	dnsCredentialsCheckCmd.Flags().StringVar(&dnsFormat, "format", "text", "Output format (json, text)")
	dnsCredentialsRemoveCmd.Flags().StringVar(&dnsFormat, "format", "text", "Output format (json, text)")

	dnsCredentialsCmd.AddCommand(dnsCredentialsSetCmd)
	dnsCredentialsCmd.AddCommand(dnsCredentialsCheckCmd)
	dnsCredentialsCmd.AddCommand(dnsCredentialsRemoveCmd)

	dnsCmd.AddCommand(dnsCredentialsCmd)
	rootCmd.AddCommand(dnsCmd)
}

func runDnsCredentialsSet(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	accountID, err := ctx.RequireAccountID()
	if err != nil {
		return err
	}

	provider := args[0]
	credentials := make(map[string]string)

	switch provider {
	case "gandi":
		if dnsPat == "" {
			return fmt.Errorf("gandi requires --pat flag")
		}
		credentials["pat"] = dnsPat
	case "spaceship":
		if dnsApiKey == "" || dnsApiSecret == "" {
			return fmt.Errorf("spaceship requires --api-key and --api-secret flags")
		}
		credentials["api_key"] = dnsApiKey
		credentials["api_secret"] = dnsApiSecret
	default:
		return fmt.Errorf("unsupported provider %q (supported: gandi, spaceship)", provider)
	}

	reqBody, err := json.Marshal(map[string]interface{}{
		"account_id":  accountID,
		"provider":    provider,
		"credentials": credentials,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/cli/dns/credentials", ctx.APIBaseURL)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to set credentials: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var credResp api.DnsCredentialsResponse
	if err := json.Unmarshal(body, &credResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if credResp.Error != "" {
			return fmt.Errorf("failed: %s", credResp.Error)
		}
		return fmt.Errorf("failed (%d): %s", resp.StatusCode, string(body))
	}

	if dnsFormat == "json" {
		return printJSON(credResp)
	}

	fmt.Printf("✓ DNS credentials for %s saved\n", provider)
	return nil
}

func runDnsCredentialsCheck(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	accountID, err := ctx.RequireAccountID()
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/api/cli/dns/credentials?account_id=%s", ctx.APIBaseURL, accountID)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to check credentials: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var credResp api.DnsCredentialsResponse
	if err := json.Unmarshal(body, &credResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if credResp.Error != "" {
			return fmt.Errorf("failed: %s", credResp.Error)
		}
		return fmt.Errorf("failed (%d): %s", resp.StatusCode, string(body))
	}

	if dnsFormat == "json" {
		return printJSON(credResp)
	}

	if len(credResp.Providers) == 0 {
		fmt.Println("No DNS providers configured.")
		fmt.Println("\nSet up with:")
		fmt.Println("  kindship dns credentials set gandi --pat <token>")
		fmt.Println("  kindship dns credentials set spaceship --api-key <key> --api-secret <secret>")
		return nil
	}

	fmt.Println("Configured DNS providers:")
	for _, p := range credResp.Providers {
		fmt.Printf("  ✓ %s (configured: %s)\n", p.Provider, p.ConfiguredAt)
	}

	return nil
}

func runDnsCredentialsRemove(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	accountID, err := ctx.RequireAccountID()
	if err != nil {
		return err
	}

	reqBody, err := json.Marshal(map[string]string{
		"account_id": accountID,
		"provider":   args[0],
	})
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/cli/dns/credentials", ctx.APIBaseURL)
	req, err := http.NewRequest(http.MethodDelete, endpoint, bytes.NewBuffer(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to remove credentials: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var credResp api.DnsCredentialsResponse
	if err := json.Unmarshal(body, &credResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if credResp.Error != "" {
			return fmt.Errorf("failed: %s", credResp.Error)
		}
		return fmt.Errorf("failed (%d): %s", resp.StatusCode, string(body))
	}

	if dnsFormat == "json" {
		return printJSON(credResp)
	}

	fmt.Printf("✓ DNS credentials for %s removed\n", args[0])
	return nil
}
