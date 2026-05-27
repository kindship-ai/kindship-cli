package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/logging"
	"github.com/spf13/cobra"
)

var verbose bool

var authCmd = &cobra.Command{
	Use:   "auth <command> [args...]",
	Short: "Execute a command with injected secrets",
	Long: `Fetch secrets from the Kindship API and execute the specified command
with those secrets injected as environment variables.

The command reads AGENT_ID and KINDSHIP_SERVICE_KEY from environment variables
to authenticate with the Kindship API.

Example:
  kindship auth claude -p "what is 2+2"     # Claude headless mode
  kindship auth codex "fix this bug"
  kindship auth gemini "explain this code"
  kindship auth vault -- ./scripts/use-token.sh
  kindship auth -v claude -p "debug mode"   # verbose logging`,
	Args: cobra.MinimumNArgs(1),
	RunE: runAuth,
}

// maskSecret returns a masked version of a secret for logging
func maskSecret(s string) string {
	if len(s) <= 8 {
		return "***"
	}
	return s[:4] + "..." + s[len(s)-4:]
}

func runAuth(cmd *cobra.Command, args []string) error {
	startTime := time.Now()
	command := args[0]
	commandArgs := args[1:]
	execCommand, execCommandArgs, err := parseAuthExec(command, commandArgs)
	if err != nil {
		return err
	}

	if command == "gitea" && execCommand == command {
		return runAuthGitea(commandArgs)
	}

	// Read agent ID early so we can initialize logging
	agentID := os.Getenv("AGENT_ID")

	// Initialize Axiom logging
	log := logging.Init(agentID, command, verbose)
	defer log.FlushSync() // Ensure logs are sent before exit

	log.Info("Starting auth", map[string]interface{}{
		"args": execCommandArgs,
	})

	if agentID == "" {
		log.Error("AGENT_ID environment variable is not set", nil)
		return fmt.Errorf("AGENT_ID environment variable is required")
	}
	log.Debug("Agent ID validated", map[string]interface{}{"agent_id": agentID})

	serviceKey := os.Getenv("KINDSHIP_SERVICE_KEY")
	if serviceKey == "" {
		log.Error("KINDSHIP_SERVICE_KEY environment variable is not set", nil)
		return fmt.Errorf("KINDSHIP_SERVICE_KEY environment variable is required")
	}
	log.Debug("Service key validated", map[string]interface{}{
		"service_key_prefix": maskSecret(serviceKey),
	})

	apiURL := os.Getenv("KINDSHIP_API_URL")
	if apiURL == "" {
		apiURL = "https://kindship.ai"
	}
	log.Debug("Using API URL", map[string]interface{}{"api_url": apiURL})

	// Fetch secrets from API
	log.Info("Fetching secrets from API")
	fetchStart := time.Now()
	client := api.NewClient(apiURL, verbose)
	secrets, err := client.FetchSecrets(agentID, command, serviceKey)
	fetchDuration := time.Since(fetchStart)

	if err != nil {
		log.Error("Failed to fetch secrets", err, map[string]interface{}{
			"duration_ms": fetchDuration.Milliseconds(),
		})
		return fmt.Errorf("failed to fetch secrets: %w", err)
	}

	// Log fetched secrets (keys only, values masked)
	secretKeys := make([]string, 0, len(secrets))
	for key := range secrets {
		secretKeys = append(secretKeys, key)
	}
	log.WithDuration("Fetched secrets", fetchDuration, map[string]interface{}{
		"secret_count": len(secrets),
		"secret_keys":  secretKeys,
	})

	// Build environment with injected secrets
	env := os.Environ()
	for key, value := range secrets {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
	}

	// Find the command executable
	executable, err := exec.LookPath(execCommand)
	if err != nil {
		log.Error("Command not found in PATH", err, map[string]interface{}{
			"command": execCommand,
			"path":    os.Getenv("PATH"),
		})
		return fmt.Errorf("command not found: %s (check PATH)", execCommand)
	}
	log.Debug("Found executable", map[string]interface{}{"executable": executable})

	// Log final setup
	setupDuration := time.Since(startTime)
	log.WithDuration("Setup complete, executing command", setupDuration, map[string]interface{}{
		"executable": executable,
		"args":       execCommandArgs,
	})

	// Flush logs before exec (exec replaces the process)
	log.FlushSync()

	// Exec the command (replaces the current process)
	execArgs := append([]string{execCommand}, execCommandArgs...)

	// syscall.Exec replaces the current process entirely
	// If it returns, an error occurred
	execErr := syscall.Exec(executable, execArgs, env)

	// If we get here, exec failed - reinitialize logger for error reporting
	errLog := logging.Init(agentID, command, verbose)
	errLog.Error("syscall.Exec failed", execErr, map[string]interface{}{
		"executable": executable,
		"args":       execArgs,
	})

	// Provide helpful hints for common errors
	if os.IsPermission(execErr) {
		errLog.Error("Permission denied", execErr, map[string]interface{}{
			"hint": fmt.Sprintf("chmod +x %s", executable),
		})
	} else if os.IsNotExist(execErr) {
		errLog.Error("Executable not found at path", execErr, map[string]interface{}{
			"path": executable,
		})
	}

	errLog.FlushSync()
	return fmt.Errorf("failed to exec %s: %w", execCommand, execErr)
}

func indexOf(values []string, target string) int {
	for i, value := range values {
		if value == target {
			return i
		}
	}
	return -1
}

func parseAuthExec(
	secretCommand string,
	commandArgs []string,
) (string, []string, error) {
	if secretCommand != "vault" {
		return secretCommand, commandArgs, nil
	}

	if delimiter := indexOf(commandArgs, "--"); delimiter >= 0 {
		execArgs := commandArgs[delimiter+1:]
		if len(execArgs) == 0 {
			return "", nil, fmt.Errorf("missing executable after --")
		}
		return execArgs[0], execArgs[1:], nil
	}

	return secretCommand, commandArgs, nil
}

func init() {
	authCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose logging for debugging")
	// Stop parsing flags after the first positional argument (the command name)
	// This allows flags like -p to be passed through to the underlying command
	authCmd.Flags().SetInterspersed(false)
}
