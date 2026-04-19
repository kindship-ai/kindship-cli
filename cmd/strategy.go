package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/kindship-ai/kindship-cli/internal/llm"
	"github.com/kindship-ai/kindship-cli/internal/prompts"
	"github.com/spf13/cobra"
)

const (
	strategyModel        = "claude-opus-4-6"
	strategyMaxTokens    = 50000
	strategyThinkBudget  = 16000
	strategyDefaultPath  = "/workspace/documents/STRATEGY.md"
	strategySkillSystem  = "generate-system"
	strategySkillUser    = "generate-user"
	strategySkillName    = "kindship-strategy"
	litellmSecretsPath   = "litellm"
)

var (
	strategyDryRun   bool
	strategyOutput   string
	strategyVerbose  bool
)

var strategyCmd = &cobra.Command{
	Use:   "strategy",
	Short: "Manage this agent's STRATEGY.md",
	Long: `Work with STRATEGY.md — the long-form document that frames
what this agent is for, who it serves, and the arc of the work.

Subcommands:
  generate   Rewrite STRATEGY.md from scratch via Opus (16k thinking).`,
}

var strategyGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Regenerate STRATEGY.md from scratch",
	Long: `Regenerate STRATEGY.md by sending Opus the agent's user_vision,
title, and posture, plus the kindship-strategy skill's system prompt.
The output lands at /workspace/documents/STRATEGY.md and is pushed to
the agent's docs repo by the maintenance heartbeat.

Regenerate when the agent's vision has shifted, accumulated learnings
change the arc, or the current STRATEGY.md has drifted from what
you're actually doing — not on a cadence.`,
	RunE: runStrategyGenerate,
}

func init() {
	strategyGenerateCmd.Flags().BoolVar(
		&strategyDryRun, "dry-run", false,
		"print generated markdown to stdout instead of writing to disk",
	)
	strategyGenerateCmd.Flags().StringVar(
		&strategyOutput, "output", strategyDefaultPath,
		"destination path for the generated STRATEGY.md",
	)
	strategyGenerateCmd.Flags().BoolVar(
		&strategyVerbose, "verbose", false,
		"log LiteLLM call timing + token usage",
	)

	strategyCmd.AddCommand(strategyGenerateCmd)
	rootCmd.AddCommand(strategyCmd)
}

func runStrategyGenerate(_ *cobra.Command, _ []string) error {
	ctx := context.Background()
	authCtx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}
	agentID, err := authCtx.RequireAgentID()
	if err != nil {
		return err
	}

	// 1. Short, CF-safe call to the Vercel agent-context endpoint.
	apiClient := api.NewClient(authCtx.APIBaseURL, strategyVerbose)
	stratCtx, err := apiClient.FetchStrategyContext(authCtx, agentID)
	if err != nil {
		return fmt.Errorf("fetch strategy context: %w", err)
	}

	// 2. Short, CF-safe call to the secrets endpoint for LiteLLM
	// credentials. Bypasses the long-Opus-call path we're restructuring
	// away from.
	if authCtx.Method != auth.AuthMethodServiceKey {
		return fmt.Errorf(
			"kindship strategy generate must run inside an agent container (needs service-key + LiteLLM secrets)",
		)
	}
	secrets, err := apiClient.FetchSecrets(agentID, litellmSecretsPath, authCtx.Token)
	if err != nil {
		return fmt.Errorf("fetch litellm secrets: %w", err)
	}
	virtualKey := secrets["LITELLM_VIRTUAL_KEY"]
	baseURL := secrets["LITELLM_BASE_URL"]
	if virtualKey == "" || baseURL == "" {
		return fmt.Errorf(
			"litellm secrets missing LITELLM_VIRTUAL_KEY or LITELLM_BASE_URL — run scripts/backfill-litellm-keys.ts from apps/web",
		)
	}

	// 3. Load the kindship-strategy skill's prompts from the container
	// filesystem (shipped via rollout-skills-to-fleet.ts).
	systemPrompt, err := prompts.Load(strategySkillName, strategySkillSystem)
	if err != nil {
		return fmt.Errorf("load strategy system prompt: %w", err)
	}
	userTemplate, err := prompts.Load(strategySkillName, strategySkillUser)
	if err != nil {
		return fmt.Errorf("load strategy user prompt: %w", err)
	}

	userMessage := prompts.Render(userTemplate, buildStrategyVars(stratCtx))

	// 4. Call LiteLLM (Opus 4.6 + 16k thinking + 50k max_tokens,
	// streaming, on the Hetzner-internal network — no CF in the loop).
	if strategyVerbose {
		fmt.Fprintln(os.Stderr, "[strategy] calling LiteLLM /anthropic/v1/messages…")
	}
	started := time.Now()
	resp, err := llm.CallAnthropicStreaming(ctx, baseURL, virtualKey, llm.AnthropicRequest{
		Model:     strategyModel,
		MaxTokens: strategyMaxTokens,
		System:    systemPrompt,
		Thinking: &llm.AnthropicThinking{
			Type:         "enabled",
			BudgetTokens: strategyThinkBudget,
		},
		// Match the web-side generator so worker-driven and CLI-driven
		// regeneration land on the same prompt shape (probe 4 pinned).
		OutputConfig: &llm.AnthropicOutputConfig{Effort: "high"},
		Messages: []llm.AnthropicMessage{{
			Role: "user",
			Content: []llm.AnthropicContent{{
				Type: "text", Text: userMessage,
			}},
		}},
	})
	if err != nil {
		return fmt.Errorf("opus call failed: %w", err)
	}

	markdown := resp.TextOutput()
	if markdown == "" {
		return fmt.Errorf("opus returned no text (stop_reason=%q)", resp.StopReason)
	}
	if strategyVerbose {
		fmt.Fprintf(
			os.Stderr,
			"[strategy] %s elapsed — %d input tokens, %d output tokens\n",
			time.Since(started).Round(time.Second),
			resp.Usage.InputTokens, resp.Usage.OutputTokens,
		)
	}

	// 5. Emit — stdout for dry-run, disk otherwise. The maintenance
	// heartbeat handles `kindship-docs-sync` to commit + push.
	if strategyDryRun {
		fmt.Println(markdown)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(strategyOutput), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if err := os.WriteFile(strategyOutput, []byte(markdown), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", strategyOutput, err)
	}
	fmt.Printf("Wrote %s (%d bytes)\n", strategyOutput, len(markdown))
	return nil
}

// buildStrategyVars resolves the five placeholders the user-message
// template expects. Mirrors renderStrategyUserMessage in
// apps/web/lib/agents/strategy-generation.server.ts so worker-driven
// and CLI-driven regeneration land on the same prompt shape.
func buildStrategyVars(ctx *api.StrategyContext) map[string]string {
	vision := ""
	if ctx.UserVision != nil {
		vision = strings.TrimSpace(*ctx.UserVision)
	}
	if vision == "" {
		// Match renderStrategyUserMessage in strategy-generation.server.ts
		// — the web-side trims before deciding to fall back, so a
		// whitespace-only DB value must produce the same fallback.
		vision = "[No user_vision on file. Ground the strategy in my PRIME_DIRECTIVE.md and any explicit goals expressed in recent conversations with the human.]"
	}

	agentName := strings.TrimSpace(ctx.AgentName)
	if agentName == "" {
		agentName = "this agent"
	}

	presentation := "1 (in my own voice)"
	switch ctx.PublicPosture.Mode {
	case "collaborator":
		presentation = "2 (as a collaborator with a named human)"
	case "organization":
		presentation = "3 (under the banner of an organization)"
	}

	userName := "[N/A — posture is not collaborator]"
	if ctx.PublicPosture.Mode == "collaborator" {
		if ctx.PublicPosture.AttributionName != nil && *ctx.PublicPosture.AttributionName != "" {
			userName = *ctx.PublicPosture.AttributionName
		} else {
			userName = "[unset]"
		}
	}

	brandName := "[N/A — posture is not organization]"
	if ctx.PublicPosture.Mode == "organization" {
		if ctx.PublicPosture.AttributionName != nil && *ctx.PublicPosture.AttributionName != "" {
			brandName = *ctx.PublicPosture.AttributionName
		} else {
			brandName = "[unset]"
		}
	}

	return map[string]string{
		"vision":              vision,
		"agent_name":          agentName,
		"presentation_option": presentation,
		"user_name":           userName,
		"brand_name":          brandName,
	}
}
