package cmd

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/spf13/cobra"
)

const giteaCredentialHost = "gitea.kindship.ai"
const defaultKindshipAPIURL = "https://kindship.ai"

var gitCredentialCmd = &cobra.Command{
	Use:    "git-credential",
	Short:  "Internal Git credential helper commands",
	Hidden: true,
}

var gitCredentialGiteaCmd = &cobra.Command{
	Use:    "gitea [get|store|erase]",
	Short:  "Serve Gitea credentials from the Kindship auth vault",
	Hidden: true,
	Args:   cobra.MaximumNArgs(1),
	RunE:   runGitCredentialGitea,
}

type gitCredentialInput struct {
	Protocol string
	Host     string
	Path     string
}

func readGitCredentialInput() (gitCredentialInput, error) {
	return readGitCredentialInputFrom(os.Stdin)
}

func readGitCredentialInputFrom(r io.Reader) (gitCredentialInput, error) {
	var input gitCredentialInput
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "protocol":
			input.Protocol = value
		case "host":
			input.Host = value
		case "path":
			input.Path = value
		}
	}
	if err := scanner.Err(); err != nil {
		return input, err
	}
	return input, nil
}

func runGitCredentialGitea(_ *cobra.Command, args []string) error {
	operation := "get"
	if len(args) > 0 {
		operation = args[0]
	}
	if operation != "get" {
		return nil
	}

	input, err := readGitCredentialInput()
	if err != nil {
		return err
	}

	agentID := os.Getenv("AGENT_ID")
	if agentID == "" {
		return fmt.Errorf("AGENT_ID environment variable is required")
	}

	apiURL := os.Getenv("KINDSHIP_API_URL")
	if apiURL == "" {
		apiURL = defaultKindshipAPIURL
	}

	if input.Protocol != "https" {
		return nil
	}
	if input.Path == "" {
		return nil
	}
	if !isKindshipGitProxyCredential(input, apiURL, agentID) {
		return nil
	}

	serviceKey := os.Getenv("KINDSHIP_SERVICE_KEY")
	if serviceKey == "" {
		return fmt.Errorf("KINDSHIP_SERVICE_KEY environment variable is required")
	}

	client := api.NewClient(apiURL, false)
	credential, err := client.FetchGitCredential(agentID, serviceKey, api.GitCredentialRequest{
		Protocol:  input.Protocol,
		Host:      input.Host,
		Path:      input.Path,
		Operation: operation,
	})
	if err != nil {
		return err
	}

	fmt.Printf("username=%s\npassword=%s\n", credential.Username, credential.Password)
	return nil
}

func isKindshipGitProxyCredential(input gitCredentialInput, apiURL, agentID string) bool {
	parsed, err := url.Parse(apiURL)
	if err != nil || parsed.Host == "" {
		return false
	}
	expectedPrefix := fmt.Sprintf("api/agent-containers/%s/git-proxy/", agentID)
	return input.Host == parsed.Host && strings.HasPrefix(strings.TrimPrefix(input.Path, "/"), expectedPrefix)
}

func runAuthGitea(args []string) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 || args[0] != "git" {
		return fmt.Errorf("kindship auth gitea only supports `git` subprocesses")
	}
	return execGitWithGiteaCredentials(args[1:])
}

func execGitWithGiteaCredentials(gitArgs []string) error {
	executable, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("command not found: git (check PATH)")
	}
	agentID := os.Getenv("AGENT_ID")
	if agentID == "" {
		return fmt.Errorf("AGENT_ID environment variable is required")
	}
	apiURL := os.Getenv("KINDSHIP_API_URL")
	if apiURL == "" {
		apiURL = defaultKindshipAPIURL
	}
	proxyPrefix, err := gitProxyPrefix(apiURL, agentID)
	if err != nil {
		return err
	}
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	execArgs := append([]string{
		"git",
		"-c", "credential.helper=",
		"-c", "credential.helper=!kindship git-credential gitea",
		"-c", "credential.useHttpPath=true",
		"-c", fmt.Sprintf("url.%s.insteadOf=https://%s/", proxyPrefix, giteaCredentialHost),
		"-c", fmt.Sprintf("url.%s.insteadOf=http://%s/", proxyPrefix, giteaCredentialHost),
	}, gitArgs...)
	return syscall.Exec(executable, execArgs, env)
}

func gitProxyPrefix(apiURL, agentID string) (string, error) {
	parsed, err := url.Parse(apiURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("KINDSHIP_API_URL must be an https URL")
	}
	base := strings.TrimRight(parsed.String(), "/")
	return fmt.Sprintf("%s/api/agent-containers/%s/git-proxy/", base, agentID), nil
}

func init() {
	gitCredentialCmd.AddCommand(gitCredentialGiteaCmd)
	rootCmd.AddCommand(gitCredentialCmd)
}
