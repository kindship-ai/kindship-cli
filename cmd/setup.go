package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
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
	setupAgentID   string
	setupSkipHooks bool
	setupForce     bool
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
		upgradeCtx, upgradeErr := auth.GetAuthContext()
		if upgradeErr != nil {
			return upgradeErr
		}

		repoName := deriveRepoName(repoRoot)

		if existingConfig.ProcessID == "" {
			fmt.Printf("Repository linked to agent: %s (upgrading to dev loop)\n\n", existingConfig.AgentID)
			resp, processErr := createDevLoopProcess(upgradeCtx, existingConfig.AgentID, repoName)
			if processErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: Could not create dev loop process: %v\n", processErr)
				return nil
			}
			existingConfig.ProcessID = resp.ProcessID
			if err := config.SaveRepoConfig(existingConfig, repoRoot); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Could not save process ID: %v\n", err)
				return nil
			}
			fmt.Printf("✓ Dev loop process created\n")
			installAttachmentSkill(upgradeCtx, existingConfig.AgentID, resp.Tasks, repoName)
			return nil
		}

		// Process already exists — call API to trigger blueprint update propagation (WS4)
		fmt.Printf("Repository linked to agent: %s (checking for blueprint updates)\n", existingConfig.AgentID)
		resp, syncErr := createDevLoopProcess(upgradeCtx, existingConfig.AgentID, repoName)
		if syncErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: Blueprint sync failed: %v\n", syncErr)
		} else {
			fmt.Println("✓ Blueprint sync complete")
		}
		if resp != nil {
			installAttachmentSkill(upgradeCtx, existingConfig.AgentID, resp.Tasks, repoName)
		}
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

	// Create dev loop process via server-side blueprint (idempotent).
	repoName := deriveRepoName(repoRoot)
	var devLoopTasks []api.DevLoopTask
	if repoConfig.ProcessID == "" || setupForce {
		resp, err := createDevLoopProcess(ctx, repoConfig.AgentID, repoName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nWarning: Could not create dev loop process: %v\n", err)
		} else {
			repoConfig.ProcessID = resp.ProcessID
			devLoopTasks = resp.Tasks
			if err := config.SaveRepoConfig(repoConfig, repoRoot); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Could not save process ID: %v\n", err)
			} else {
				fmt.Printf("\n✓ Dev loop process created\n")
			}
		}
	}

	// Install attachment skill to agent container (non-fatal)
	if repoConfig.ProcessID != "" {
		installAttachmentSkill(ctx, repoConfig.AgentID, devLoopTasks, repoName)
	}

	// Step 7: Install Claude Code hooks (if not skipped)
	if !setupSkipHooks {
		if err := installClaudeHooks(repoRoot); err != nil {
			fmt.Fprintf(os.Stderr, "\nWarning: Failed to install Claude Code hooks: %v\n", err)
			fmt.Println("You can manually install hooks later or run 'kindship setup' again.")
		} else {
			fmt.Println("\n✓ Claude Code hooks installed")
		}
	}

	fmt.Println("\nSetup complete! You can now:")
	fmt.Println("  kindship status --local     Show current configuration and active run")
	fmt.Println("  kindship run local-next     Fetch and start the next task (local mode)")
	fmt.Println("  kindship run local-complete Mark current task as completed")
	fmt.Println("  kindship run local-fail     Mark current task as failed")

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

func createDevLoopProcess(ctx *auth.Context, agentID string, repoName string) (*api.CreateDevLoopResponse, error) {
	client := api.NewClient(ctx.APIBaseURL, verbose)
	resp, err := client.CreateDevLoopWithBearer(agentID, repoName, ctx.Token)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// findDecideEntityID finds the Decide task entity ID (sequence_order == 1) from tasks.
func findDecideEntityID(tasks []api.DevLoopTask) string {
	for _, t := range tasks {
		if t.SequenceOrder == 1 {
			return t.ID
		}
	}
	return ""
}

func deriveRepoName(repoRoot string) string {
	// Try git remote first
	cmd := exec.Command("git", "-C", repoRoot, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err == nil {
		url := strings.TrimSpace(string(out))
		// Extract "org/repo" from github.com/org/repo.git or git@github.com:org/repo.git
		url = strings.TrimSuffix(url, ".git")
		// Handle SSH format: git@github.com:org/repo
		if idx := strings.LastIndex(url, ":"); idx > 0 && strings.Contains(url[:idx], "@") {
			url = url[idx+1:]
		}
		if parts := strings.Split(url, "/"); len(parts) >= 2 {
			return parts[len(parts)-2] + "/" + parts[len(parts)-1]
		}
	}
	// Fallback: parent folder name
	return filepath.Base(repoRoot)
}

func installAttachmentSkill(ctx *auth.Context, agentID string, tasks []api.DevLoopTask, repoName string) {
	decideEntityID := findDecideEntityID(tasks)
	if decideEntityID == "" {
		fmt.Fprintf(os.Stderr, "Warning: Could not find Decide task entity — skill not installed\n")
		return
	}

	skillName := sanitizeSkillName(repoName)
	skillContent := generateAttachmentSkill(repoName, decideEntityID)

	client := api.NewClient(ctx.APIBaseURL, verbose)
	if err := client.InstallSkill(agentID, skillName, skillContent, ctx.Token); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Could not install feature request skill: %v\n", err)
		return
	}
	fmt.Printf("✓ Feature request skill installed (skills/%s/SKILL.md)\n", skillName)
}

func sanitizeSkillName(repoName string) string {
	// Extract just the repo part (after the last /)
	name := repoName
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	// Replace non-alphanumeric with hyphens
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		result = "project"
	}
	return "feature-request-" + result
}

func generateAttachmentSkill(repoName, decideEntityID string) string {
	return fmt.Sprintf(`---
name: feature-request-%s
description: Submit feature requests for %s development
---

## When to use this skill

Use this when the user wants to request a feature, improvement, bug fix, or
priority change for the **%s** project.

Examples: "add dark mode", "fix the login bug", "prioritize performance",
"I want a footer on the landing page"

## How to add a feature request

1. Read existing requests:
`+"```"+`bash
curl -s "$KINDSHIP_API_URL/api/cli/entity/%s/attachments?name=feature-requests.txt" \
  -H "X-Kindship-Service-Key: $KINDSHIP_SERVICE_KEY" | jq -r '.content // ""'
`+"```"+`

2. Append the new request to the existing content and write back:
`+"```"+`bash
EXISTING=$(curl -s "$KINDSHIP_API_URL/api/cli/entity/%s/attachments?name=feature-requests.txt" \
  -H "X-Kindship-Service-Key: $KINDSHIP_SERVICE_KEY" | jq -r '.content // ""')
CONTENT="$(echo "$EXISTING"; echo "- NEW REQUEST DESCRIPTION")"
jq -n --arg name "feature-requests.txt" --arg content "$CONTENT" \
  '{name: $name, content: $content}' | \
curl -s -X PUT "$KINDSHIP_API_URL/api/cli/entity/%s/attachments" \
  -H "X-Kindship-Service-Key: $KINDSHIP_SERVICE_KEY" \
  -H "Content-Type: application/json" -d @-
`+"```"+`

Replace "NEW REQUEST DESCRIPTION" with a clear, actionable summary.
Confirm to the user that it was recorded for the next development cycle.

## How to list feature requests

`+"```"+`bash
curl -s "$KINDSHIP_API_URL/api/cli/entity/%s/attachments?name=feature-requests.txt" \
  -H "X-Kindship-Service-Key: $KINDSHIP_SERVICE_KEY" | jq -r '.content // "No feature requests yet."'
`+"```"+`
`, sanitizeSkillName(repoName), repoName, repoName, decideEntityID, decideEntityID, decideEntityID, decideEntityID)
}

func installClaudeHooks(repoRoot string) error {
	// Claude Code reads hooks from .claude/settings.local.json.
	claudeDir := filepath.Join(repoRoot, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return fmt.Errorf("failed to create .claude directory: %w", err)
	}

	settingsPath := filepath.Join(claudeDir, "settings.local.json")
	settings := map[string]interface{}{}
	if data, err := os.ReadFile(settingsPath); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("failed to parse %s: %w", settingsPath, err)
		}
	}

	ensureHookCommand(settings, "SessionStart", "kindship hook start")
	ensureHookCommand(settings, "Stop", "kindship hook stop")

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings.local.json: %w", err)
	}
	if err := os.WriteFile(settingsPath, out, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", settingsPath, err)
	}

	// Cleanup old YAML hooks if present.
	_ = os.Remove(filepath.Join(repoRoot, ".claude", "hooks", "start.yaml"))
	_ = os.Remove(filepath.Join(repoRoot, ".claude", "hooks", "stop.yaml"))

	// Create .claude/skills directory and install kindship skill
	skillsDir := filepath.Join(repoRoot, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		return fmt.Errorf("failed to create skills directory: %w", err)
	}

	kindshipSkill := `name: kindship
version: 2
commands:
  - name: next
    description: Fetch and start the next Kindship task
    command: kindship run local-next
  - name: complete
    description: Mark current task as completed
    command: kindship run local-complete
  - name: fail
    description: Mark current task as failed
    command: kindship run local-fail --reason "{{reason}}"
  - name: status
    description: Show agent and task status
    command: kindship status --local
`
	if err := os.WriteFile(filepath.Join(skillsDir, "kindship.yaml"), []byte(kindshipSkill), 0644); err != nil {
		return fmt.Errorf("failed to write kindship skill: %w", err)
	}

	return nil
}

func ensureHookCommand(settings map[string]interface{}, event string, command string) {
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok || hooks == nil {
		hooks = map[string]interface{}{}
		settings["hooks"] = hooks
	}

	entries, ok := hooks[event].([]interface{})
	if !ok || entries == nil {
		entries = []interface{}{}
	}

	base := command
	if idx := strings.Index(command, " --"); idx > 0 {
		base = command[:idx]
	}

	// If a Kindship hook already exists under this event, update it in-place.
	for _, e := range entries {
		entry, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		hlist, ok := entry["hooks"].([]interface{})
		if !ok {
			continue
		}
		for _, h := range hlist {
			hm, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			if hm["type"] != "command" {
				continue
			}
			existing, _ := hm["command"].(string)
			if strings.HasPrefix(existing, base) {
				hm["command"] = command
				hooks[event] = entries
				return
			}
		}
	}

	// Append a new hook entry.
	entries = append(entries, map[string]interface{}{
		"matcher": "",
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    "command",
				"command": command,
			},
		},
	})
	hooks[event] = entries
}
