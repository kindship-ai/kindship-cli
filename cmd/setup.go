package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/kindship-ai/kindship-cli/internal/config"

	"github.com/spf13/cobra"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Initialize repository with agent binding",
	Long: `Initialize a git repository for use with Kindship.

This command:
1. Verifies you are authenticated
2. Shows your available agents to select from
3. Creates .kindship/config.json with the agent binding
4. Optionally installs Claude Code hooks for integration

Run this command in the root of a git repository.

Examples:
  kindship setup                  # Interactive setup
  kindship setup --agent <id>     # Non-interactive with specific agent`,
	RunE: runSetup,
}

var (
	setupAgentID    string
	setupSkipHooks  bool
	setupForce      bool
)

func init() {
	setupCmd.Flags().StringVar(&setupAgentID, "agent", "", "Agent ID to bind (skips interactive selection)")
	setupCmd.Flags().BoolVar(&setupSkipHooks, "skip-hooks", false, "Skip Claude Code hooks installation")
	setupCmd.Flags().BoolVar(&setupForce, "force", false, "Overwrite existing configuration")
	rootCmd.AddCommand(setupCmd)
}

// AgentsResponse is the response from /api/cli/agents
type AgentsResponse struct {
	Agents    []AgentInfo `json:"agents"`
	UserEmail string      `json:"user_email"`
	Error     string      `json:"error,omitempty"`
}

// AgentInfo represents an agent in the list
type AgentInfo struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	AccountSlug string `json:"account_slug"`
	IsPersonal  bool   `json:"is_personal"`
	CreatedAt   string `json:"created_at"`
}

func runSetup(cmd *cobra.Command, args []string) error {
	// Step 1: Verify we're in a git repository
	repoRoot, err := config.FindRepoRoot()
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}

	fmt.Printf("Repository root: %s\n\n", repoRoot)

	// Step 2: Check for existing configuration
	existingConfig, _ := config.LoadRepoConfig()
	if existingConfig != nil && existingConfig.AgentID != "" && !setupForce {
		fmt.Printf("This repository is already linked to agent: %s\n", existingConfig.AgentID)
		fmt.Println("Use --force to overwrite the existing configuration.")
		return nil
	}

	// Step 3: Verify authentication
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	if !ctx.IsLocalMode() {
		return fmt.Errorf("setup is only available in local mode (not in containers)")
	}

	fmt.Printf("Authenticated as: %s\n\n", ctx.UserEmail)

	// Step 4: Fetch available agents
	agents, err := fetchAgents(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch agents: %w", err)
	}

	if len(agents) == 0 {
		fmt.Println("No agents found. Create an agent at https://kindship.ai first.")
		return nil
	}

	// Step 5: Select agent (interactive or from flag)
	var selectedAgent *AgentInfo

	if setupAgentID != "" {
		// Non-interactive: find the specified agent
		for i := range agents {
			if agents[i].ID == setupAgentID || agents[i].Slug == setupAgentID {
				selectedAgent = &agents[i]
				break
			}
		}
		if selectedAgent == nil {
			return fmt.Errorf("agent not found: %s", setupAgentID)
		}
	} else {
		// Interactive: prompt user to select
		selectedAgent, err = promptSelectAgent(agents)
		if err != nil {
			return err
		}
	}

	fmt.Printf("\nSelected agent: %s (%s)\n", selectedAgent.Title, selectedAgent.ID)

	// Step 6: Save repository configuration
	repoConfig := &config.RepoConfig{
		AgentID:   selectedAgent.ID,
		AgentSlug: selectedAgent.Slug,
		AccountID: selectedAgent.AccountID,
		BoundAt:   time.Now(),
	}

	if err := config.SaveRepoConfig(repoConfig, repoRoot); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("\n✓ Repository linked to agent '%s'\n", selectedAgent.Title)
	fmt.Printf("  Configuration saved to .kindship/config.json\n")

	// Step 7: Install Claude Code hooks (if not skipped)
	if !setupSkipHooks {
		if err := installClaudeHooks(repoRoot); err != nil {
			fmt.Fprintf(os.Stderr, "\nWarning: Failed to install Claude Code hooks: %v\n", err)
			fmt.Println("You can manually install hooks later or run 'kindship setup' again.")
		} else {
			fmt.Println("\n✓ Claude Code hooks installed")
		}
	}

	fmt.Println("\nSetup complete! You can now use:")
	fmt.Println("  kindship status    Show current configuration")
	fmt.Println("  kindship run next  Get the next work item")

	return nil
}

func fetchAgents(ctx *auth.Context) ([]AgentInfo, error) {
	endpoint := fmt.Sprintf("%s/api/cli/agents", ctx.APIBaseURL)

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", ctx.GetAuthHeader())
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		var errResp AgentsResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("API error: %s", errResp.Error)
		}
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var agentsResp AgentsResponse
	if err := json.Unmarshal(body, &agentsResp); err != nil {
		return nil, err
	}

	return agentsResp.Agents, nil
}

func promptSelectAgent(agents []AgentInfo) (*AgentInfo, error) {
	fmt.Println("Available agents:")
	fmt.Println()

	for i, agent := range agents {
		accountLabel := agent.AccountName
		if agent.IsPersonal {
			accountLabel = "Personal"
		}
		fmt.Printf("  [%d] %s (%s)\n", i+1, agent.Title, accountLabel)
	}

	fmt.Println()
	fmt.Print("Select an agent (enter number): ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read input: %w", err)
	}

	input = strings.TrimSpace(input)
	num, err := strconv.Atoi(input)
	if err != nil || num < 1 || num > len(agents) {
		return nil, fmt.Errorf("invalid selection: %s", input)
	}

	return &agents[num-1], nil
}

func installClaudeHooks(repoRoot string) error {
	// Create .claude/hooks directory
	hooksDir := repoRoot + "/.claude/hooks"
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return fmt.Errorf("failed to create hooks directory: %w", err)
	}

	// Install start hook
	startHook := `name: kindship-start
trigger: start
command: kindship hook start
env:
  KINDSHIP_HOOK_VERSION: "1"
`
	if err := os.WriteFile(hooksDir+"/start.yaml", []byte(startHook), 0644); err != nil {
		return fmt.Errorf("failed to write start hook: %w", err)
	}

	// Install stop hook
	stopHook := `name: kindship-stop
trigger: stop
command: kindship hook stop
env:
  KINDSHIP_HOOK_VERSION: "1"
args:
  - --summary-file
  - "{{summary_file}}"
`
	if err := os.WriteFile(hooksDir+"/stop.yaml", []byte(stopHook), 0644); err != nil {
		return fmt.Errorf("failed to write stop hook: %w", err)
	}

	// Create .claude/skills directory and install kindship skill
	skillsDir := repoRoot + "/.claude/skills"
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		return fmt.Errorf("failed to create skills directory: %w", err)
	}

	kindshipSkill := `name: kindship
version: 1
commands:
  - name: next
    description: Get next work item from planning
    command: kindship run next --format json
  - name: complete
    description: Mark current task complete
    command: kindship run complete {{entity_id}} --outputs "{{outputs}}"
  - name: status
    description: Show current repo and agent status
    command: kindship status --format json
`
	if err := os.WriteFile(skillsDir+"/kindship.yaml", []byte(kindshipSkill), 0644); err != nil {
		return fmt.Errorf("failed to write kindship skill: %w", err)
	}

	return nil
}
