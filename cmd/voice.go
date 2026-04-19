package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/kindship-ai/kindship-cli/internal/llm"
	"github.com/kindship-ai/kindship-cli/internal/prompts"
	"github.com/kindship-ai/kindship-cli/internal/voice"

	"github.com/spf13/cobra"
)

// Voice pipeline commands. Everything runs on the agent container,
// calling LiteLLM (for Opus) and Gemini (for audio) directly. Vercel
// only issues credentials via the short secrets endpoint — no CF in
// the LLM path.
//
// Three subcommands live here:
//   voice "<narrative>"                single-speaker monologue
//   voice exact --voice ... --text ... verbatim render, no Opus
//   voice multi "<narrative>"          two-speaker podcast

const (
	voiceSkillMono         = "kindship-voice"
	voiceSkillPod          = "create-podcast"
	voicePromptMonoIdeateS = "monologue-ideate-system"
	voicePromptMonoIdeateU = "monologue-ideate-user"
	voicePromptMonoAuthorS = "monologue-author-system"
	voicePromptMonoAuthorU = "monologue-author-user"
	voicePromptPodIdeateS  = "podcast-ideate-system"
	voicePromptPodIdeateU  = "podcast-ideate-user"
	voicePromptPodAuthorS  = "podcast-author-system"
	voicePromptPodAuthorU  = "podcast-author-user"

	voiceOpusModel            = "claude-opus-4-6"
	voiceOpusMaxTokens        = 50000
	voiceOpusThinkBudget      = 16000
	voiceDefaultTargetMinutes = 3
	voiceDefaultPodcastLength = "5-7 minutes"

	voiceDefaultMonoDir   = "/workspace/documents/voice"
	voiceDefaultPodDir    = "/workspace/documents/podcasts"
	voiceStyleMdPath      = "/workspace/documents/STYLE.md"
	voiceBatchPacingSecs  = 7
	voiceDefaultSpeakerRole = "narrator"
)

var (
	voiceSlug          string
	voiceVoice         string
	voiceStyle         string
	voiceTargetMinutes int
	voiceOutput        string
	voiceFormat        string

	voiceExactVoice     string
	voiceExactStyle     string
	voiceExactText      string
	voiceExactTextsFile string
	voiceExactOutput    string
	voiceExactOutputDir string

	voiceMultiSlug           string
	voiceMultiNarratorVoice  string
	voiceMultiNarratorStyle  string
	voiceMultiNarratorName   string
	voiceMultiCompanionVoice string
	voiceMultiCompanionStyle string
	voiceMultiCompanionName  string
	voiceMultiTargetLength   string
	voiceMultiOutput         string
)

var voiceCmd = &cobra.Command{
	Use:   "voice [narrative]",
	Short: "Voice-over and podcast generation (Opus × 2 + Gemini)",
	Long: `Generate voice-over monologues and two-speaker podcasts via a
three-stage pipeline: Opus plans, Opus writes, Gemini performs.

  kindship voice "<narrative>"          single-speaker voice-over
  kindship voice exact  ...             verbatim render, one or more lines
  kindship voice multi  "<narrative>"   two-speaker podcast

For the monologue path, pass the raw narrative as the positional arg.
The CLI runs two Opus passes (ideate → author) via LiteLLM and
renders via Gemini Live using the narrator voice from your STYLE.md
Sound section (overridable with --voice and --style).

Output lands at /workspace/documents/voice/<slug>.wav by default.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runVoiceGenerate,
}

var voiceExactCmd = &cobra.Command{
	Use:   "exact",
	Short: "Render one or more texts verbatim — no Opus rewrite",
	Long: `Render each text with the prescribed voice + behavioral clause,
verbatim. Useful for pre-written voice-overs, video chapter markers,
fixed hooks.

Single-text mode:
  kindship voice exact --voice Kore --style "gentle, measured" \
    --text "Chapter one" --output out/01-chapter-one.wav

Batch mode (array of texts → directory of WAVs + manifest.json):
  kindship voice exact --voice Kore --style "gentle, measured" \
    --texts-file lines.json --output-dir voice-clips/

lines.json shape: {"texts": ["line 1", "line 2", ...]}
Batch mode paces requests 7 seconds apart.`,
	RunE: runVoiceExact,
}

var voiceMultiCmd = &cobra.Command{
	Use:   "multi <narrative>",
	Short: "Generate a two-speaker podcast from a narrative",
	Long: `Pass the raw narrative as the positional argument. The CLI
runs two Opus passes (ideate → author) to produce a two-speaker
dialogue, then renders via Gemini TTS multi-speaker mode.

Both speaker voices come from your STYLE.md Sound section (Narrator +
Companion) by default — pick them for deliberate contrast. Override
with the --narrator-* and --companion-* flags.

Output lands at /workspace/documents/podcasts/<slug>.wav plus a
sibling <slug>.meta.json with title + cold_open_note.`,
	Args: cobra.ExactArgs(1),
	RunE: runVoiceMulti,
}

func init() {
	voiceCmd.Flags().StringVar(&voiceSlug, "slug", "", "output slug (kebab-case 2-63 chars); default derived from narrative")
	voiceCmd.Flags().StringVar(&voiceVoice, "voice", "", "Gemini voice ID; default from STYLE.md narrator entry")
	voiceCmd.Flags().StringVar(&voiceStyle, "style", "", "behavioral clause e.g. \"gravelly, measured, older scholar\"")
	voiceCmd.Flags().IntVar(&voiceTargetMinutes, "target-minutes", 0, "finished audio target length (default server-chosen, ~3)")
	voiceCmd.Flags().StringVar(&voiceOutput, "output", "", "destination path (default /workspace/documents/voice/<slug>.wav)")
	voiceCmd.Flags().StringVar(&voiceFormat, "format", "text", "success summary format: text (default) or json")

	voiceExactCmd.Flags().StringVar(&voiceExactVoice, "voice", "", "Gemini voice ID (required)")
	voiceExactCmd.Flags().StringVar(&voiceExactStyle, "style", "", "behavioral clause (required)")
	voiceExactCmd.Flags().StringVar(&voiceExactText, "text", "", "single text to render; mutually exclusive with --texts-file")
	voiceExactCmd.Flags().StringVar(&voiceExactTextsFile, "texts-file", "", "JSON file with shape {\"texts\":[...]}; writes one WAV per text")
	voiceExactCmd.Flags().StringVar(&voiceExactOutput, "output", "", "single-text output path")
	voiceExactCmd.Flags().StringVar(&voiceExactOutputDir, "output-dir", "", "batch-mode output directory")
	_ = voiceExactCmd.MarkFlagRequired("voice")
	_ = voiceExactCmd.MarkFlagRequired("style")

	voiceMultiCmd.Flags().StringVar(&voiceMultiSlug, "slug", "", "output slug (kebab-case 2-63 chars); default derived from narrative")
	voiceMultiCmd.Flags().StringVar(&voiceMultiNarratorVoice, "narrator-voice", "", "override STYLE.md narrator voice")
	voiceMultiCmd.Flags().StringVar(&voiceMultiNarratorStyle, "narrator-style", "", "override STYLE.md narrator behavioral clause")
	voiceMultiCmd.Flags().StringVar(&voiceMultiNarratorName, "narrator-name", "", "override narrator display name (default: \"Host\")")
	voiceMultiCmd.Flags().StringVar(&voiceMultiCompanionVoice, "companion-voice", "", "override STYLE.md companion voice")
	voiceMultiCmd.Flags().StringVar(&voiceMultiCompanionStyle, "companion-style", "", "override STYLE.md companion behavioral clause")
	voiceMultiCmd.Flags().StringVar(&voiceMultiCompanionName, "companion-name", "", "override companion display name (default: \"Guest\")")
	voiceMultiCmd.Flags().StringVar(&voiceMultiTargetLength, "target-length", "", "target episode length e.g. \"6-8 minutes\" (default \"5-7 minutes\")")
	voiceMultiCmd.Flags().StringVar(&voiceMultiOutput, "output", "", "destination path (default /workspace/documents/podcasts/<slug>.wav)")

	voiceCmd.AddCommand(voiceExactCmd)
	voiceCmd.AddCommand(voiceMultiCmd)
	rootCmd.AddCommand(voiceCmd)
}

// ---------- shared helpers ----------

var slugSafeRune = regexp.MustCompile(`[^a-z0-9-]+`)

func deriveSlug(text string) string {
	s := strings.ToLower(strings.TrimSpace(text))
	s = slugSafeRune.ReplaceAllString(strings.ReplaceAll(s, " ", "-"), "")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = s[:40]
		s = strings.Trim(s, "-")
	}
	if len(s) < 2 {
		return fmt.Sprintf("voice-%d", time.Now().Unix())
	}
	return s
}

// voiceSecrets bundles the credentials the voice pipeline needs, one
// pass through the short Vercel secrets endpoint.
type voiceSecrets struct {
	LiteLLMKey     string
	LiteLLMBaseURL string
	GeminiKey      string
}

// fetchVoiceSecrets pulls litellm + gemini secrets for the current
// agent via the Vercel secrets endpoint. Requires container-mode
// auth (service key) because the secrets endpoint is IP-whitelisted
// to the agent server.
func fetchVoiceSecrets(
	authCtx *auth.Context, agentID string, needOpus bool,
) (*voiceSecrets, error) {
	if authCtx.Method != auth.AuthMethodServiceKey {
		return nil, fmt.Errorf(
			"kindship voice must run inside an agent container (secrets endpoint is IP-whitelisted)",
		)
	}
	apiClient := api.NewClient(authCtx.APIBaseURL, false)
	out := &voiceSecrets{}

	gem, err := apiClient.FetchSecrets(agentID, "gemini", authCtx.Token)
	if err != nil {
		return nil, fmt.Errorf("fetch gemini secrets: %w", err)
	}
	out.GeminiKey = gem["GEMINI_API_KEY"]
	if out.GeminiKey == "" {
		return nil, fmt.Errorf("gemini secret missing GEMINI_API_KEY")
	}

	if needOpus {
		lit, err := apiClient.FetchSecrets(agentID, "litellm", authCtx.Token)
		if err != nil {
			return nil, fmt.Errorf("fetch litellm secrets: %w", err)
		}
		out.LiteLLMKey = lit["LITELLM_VIRTUAL_KEY"]
		out.LiteLLMBaseURL = lit["LITELLM_BASE_URL"]
		if out.LiteLLMKey == "" || out.LiteLLMBaseURL == "" {
			return nil, fmt.Errorf(
				"litellm secrets missing LITELLM_VIRTUAL_KEY or LITELLM_BASE_URL — operator fix: run scripts/backfill-litellm-keys.ts",
			)
		}
	}
	return out, nil
}

// loadAgentSound reads the agent's STYLE.md from the container
// filesystem and parses the Sound section. Returns an empty
// StyleSound (both fields nil) if the file doesn't exist or can't be
// parsed — callers require CLI overrides in that case.
func loadAgentSound() voice.StyleSound {
	raw, err := os.ReadFile(voiceStyleMdPath)
	if err != nil {
		return voice.StyleSound{}
	}
	return voice.ParseStyleMdSound(string(raw))
}

// validateSlug lives in cmd/video.go — shared across commands.

// callOpus runs one streaming Anthropic request against LiteLLM with
// thinking enabled + the model/max_tokens the voice pipeline uses.
func callOpus(
	ctx context.Context, secrets *voiceSecrets,
	system, userMsg string,
) (*llm.AnthropicResponse, error) {
	return llm.CallAnthropicStreaming(ctx, secrets.LiteLLMBaseURL, secrets.LiteLLMKey,
		llm.AnthropicRequest{
			Model:     voiceOpusModel,
			MaxTokens: voiceOpusMaxTokens,
			System:    system,
			Thinking: &llm.AnthropicThinking{
				Type:         "enabled",
				BudgetTokens: voiceOpusThinkBudget,
			},
			// Match the web-side request shape (probe 4 pinned) — keeps
			// CLI-generated audio quality aligned with whatever the
			// worker-driven strategy/voice path produces.
			OutputConfig: &llm.AnthropicOutputConfig{Effort: "high"},
			Messages: []llm.AnthropicMessage{{
				Role: "user",
				Content: []llm.AnthropicContent{{
					Type: "text", Text: userMsg,
				}},
			}},
		})
}

func writeWav(path string, pcm []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	wav := llm.PCMToWAV(pcm, 0)
	if err := os.WriteFile(path, wav, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ---------- voice (monologue) ----------

func runVoiceGenerate(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}
	narrative := args[0]
	if len(strings.TrimSpace(narrative)) < 20 {
		return fmt.Errorf("narrative must be at least 20 chars")
	}

	ctx := context.Background()
	authCtx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}
	agentID, err := authCtx.RequireAgentID()
	if err != nil {
		return err
	}

	slug := voiceSlug
	if slug == "" {
		slug = deriveSlug(narrative)
	}
	if err := validateSlug(slug); err != nil {
		return err
	}

	outputPath := voiceOutput
	if outputPath == "" {
		outputPath = filepath.Join(voiceDefaultMonoDir, slug+".wav")
	}

	// Resolve the narrator profile — CLI flags win; otherwise STYLE.md.
	sound := loadAgentSound()
	narratorVoice := voiceVoice
	narratorStyle := voiceStyle
	var narratorPersonality string
	if narratorVoice == "" && sound.Narrator != nil {
		narratorVoice = sound.Narrator.Voice
	}
	if narratorStyle == "" && sound.Narrator != nil {
		narratorStyle = sound.Narrator.BehavioralClause
	}
	if sound.Narrator != nil {
		narratorPersonality = sound.Narrator.Personality
	}
	if narratorVoice == "" || narratorStyle == "" {
		return fmt.Errorf(
			"narrator voice + behavioral clause required — either fill STYLE.md Sound section or pass --voice and --style",
		)
	}
	if !voice.IsValidGeminiVoice(narratorVoice) {
		return fmt.Errorf("voice %q is not in the Gemini roster", narratorVoice)
	}

	targetMinutes := voiceTargetMinutes
	if targetMinutes <= 0 {
		targetMinutes = voiceDefaultTargetMinutes
	}

	secrets, err := fetchVoiceSecrets(authCtx, agentID, true)
	if err != nil {
		return err
	}

	profile := voice.SpeakerProfile{
		Name:             "narrator",
		Role:             "single speaker",
		VoiceID:          narratorVoice,
		BehavioralClause: narratorStyle,
		Personality:      narratorPersonality,
	}

	// Pass 1 — ideate
	ideateSystem, err := prompts.LoadAndRender(voiceSkillMono, voicePromptMonoIdeateS, map[string]string{
		"target_minutes":  fmt.Sprintf("%d", targetMinutes),
		"speaker_profile": voice.RenderSpeakerProfile(profile),
	})
	if err != nil {
		return err
	}
	ideateUser, err := prompts.LoadAndRender(voiceSkillMono, voicePromptMonoIdeateU, map[string]string{
		"narrative": narrative,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "→ ideate (Opus 4.6 + 16k thinking)…")
	ideateResp, err := callOpus(ctx, secrets, ideateSystem, ideateUser)
	if err != nil {
		return fmt.Errorf("ideate: %w", err)
	}
	preScript := ideateResp.TextOutput()
	if preScript == "" {
		return fmt.Errorf("ideate returned empty text (stop_reason=%q)", ideateResp.StopReason)
	}

	// Pass 2 — author
	authorSystem, err := prompts.LoadAndRender(voiceSkillMono, voicePromptMonoAuthorS, map[string]string{
		"speaker_profile": voice.RenderSpeakerProfile(profile),
		"pre_script_json": preScript,
	})
	if err != nil {
		return err
	}
	authorUser, err := prompts.LoadAndRender(voiceSkillMono, voicePromptMonoAuthorU, map[string]string{
		"narrative": narrative,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "→ author (Opus 4.6 + 16k thinking)…")
	authorResp, err := callOpus(ctx, secrets, authorSystem, authorUser)
	if err != nil {
		return fmt.Errorf("author: %w", err)
	}
	script, err := voice.ParseMonologueScript(authorResp.TextOutput())
	if err != nil {
		return err
	}

	// Render via Gemini Live (single voice, audio-out).
	geminiPrompt := voice.RenderMonologueGeminiPrompt(script, profile)
	fmt.Fprintln(os.Stderr, "→ render (Gemini Live)…")
	pcm, err := llm.SingleSpeakerLive(ctx, secrets.GeminiKey, narratorVoice, geminiPrompt)
	if err != nil {
		return fmt.Errorf("gemini live: %w", err)
	}
	if err := writeWav(outputPath, pcm); err != nil {
		return err
	}

	if voiceFormat == "json" {
		return printJSON(map[string]any{
			"slug":  slug,
			"path":  outputPath,
			"title": script.Title,
			"beats": len(script.Beats),
		})
	}
	fmt.Printf("Generated %s → %s (%d beats)\n", slug, outputPath, len(script.Beats))
	return nil
}

// ---------- voice exact ----------

func runVoiceExact(_ *cobra.Command, _ []string) error {
	ctx := context.Background()
	authCtx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}
	agentID, err := authCtx.RequireAgentID()
	if err != nil {
		return err
	}

	hasSingle := voiceExactText != ""
	hasBatch := voiceExactTextsFile != ""
	if hasSingle == hasBatch {
		return fmt.Errorf("exactly one of --text or --texts-file must be provided")
	}
	if hasSingle && strings.TrimSpace(voiceExactText) == "" {
		return fmt.Errorf("--text must contain non-whitespace content")
	}
	if !voice.IsValidGeminiVoice(voiceExactVoice) {
		return fmt.Errorf("voice %q is not in the Gemini roster", voiceExactVoice)
	}

	secrets, err := fetchVoiceSecrets(authCtx, agentID, false)
	if err != nil {
		return err
	}

	if hasSingle {
		if voiceExactOutput == "" {
			return fmt.Errorf("--output is required with --text")
		}
		bytesWritten, err := renderExactOne(ctx, secrets.GeminiKey, voiceExactVoice, voiceExactStyle, voiceExactText, voiceExactOutput)
		if err != nil {
			return err
		}
		fmt.Printf("Rendered %d bytes → %s\n", bytesWritten, voiceExactOutput)
		return nil
	}

	if voiceExactOutputDir == "" {
		return fmt.Errorf("--output-dir is required with --texts-file")
	}
	return runVoiceExactBatch(ctx, secrets.GeminiKey, voiceExactTextsFile, voiceExactOutputDir)
}

func renderExactOne(
	ctx context.Context,
	geminiKey, voiceName, style, text, outputPath string,
) (int64, error) {
	prompt := voice.RenderExactPrompt(voiceName, style, text)
	pcm, err := llm.SingleSpeakerLive(ctx, geminiKey, voiceName, prompt)
	if err != nil {
		return 0, fmt.Errorf("gemini live: %w", err)
	}
	if err := writeWav(outputPath, pcm); err != nil {
		return 0, err
	}
	// Report the audio payload size (not the WAV wrapper size) so the
	// number matches other tooling.
	return int64(len(pcm)), nil
}

type voiceExactBatchFile struct {
	Texts []string `json:"texts"`
}

type voiceExactManifestEntry struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
	File  string `json:"file"`
	Bytes int64  `json:"bytes"`
}

type voiceExactManifest struct {
	Voice   string                    `json:"voice"`
	Style   string                    `json:"style"`
	Entries []voiceExactManifestEntry `json:"entries"`
}

func runVoiceExactBatch(ctx context.Context, geminiKey, textsFile, outDir string) error {
	raw, err := os.ReadFile(textsFile)
	if err != nil {
		return fmt.Errorf("read --texts-file: %w", err)
	}
	var batch voiceExactBatchFile
	if err := json.Unmarshal(raw, &batch); err != nil {
		return fmt.Errorf("parse --texts-file (expected {\"texts\":[...]}): %w", err)
	}
	if len(batch.Texts) == 0 {
		return fmt.Errorf("--texts-file has no texts")
	}
	for i, t := range batch.Texts {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("--texts-file entry %d is blank", i+1)
		}
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	manifest := voiceExactManifest{
		Voice:   voiceExactVoice,
		Style:   voiceExactStyle,
		Entries: make([]voiceExactManifestEntry, 0, len(batch.Texts)),
	}

	pacing := time.Duration(voiceBatchPacingSecs) * time.Second
	for i, text := range batch.Texts {
		slug := deriveSlug(text)
		name := fmt.Sprintf("%02d-%s.wav", i+1, slug)
		outPath := filepath.Join(outDir, name)
		bytesWritten, err := renderExactOne(ctx, geminiKey, voiceExactVoice, voiceExactStyle, text, outPath)
		if err != nil {
			return fmt.Errorf("line %d (%q): %w", i+1, truncateVoicePreview(text, 40), err)
		}
		manifest.Entries = append(manifest.Entries, voiceExactManifestEntry{
			Index: i + 1, Text: text, File: name, Bytes: bytesWritten,
		})
		fmt.Printf("  [%d/%d] %s (%d bytes)\n", i+1, len(batch.Texts), name, bytesWritten)
		if i < len(batch.Texts)-1 {
			time.Sleep(pacing)
		}
	}

	manifestPath := filepath.Join(outDir, "manifest.json")
	f, err := os.Create(manifestPath)
	if err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(manifest); err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	fmt.Printf("Wrote %d clips + %s\n", len(manifest.Entries), manifestPath)
	return nil
}

// ---------- voice multi (podcast) ----------

func runVoiceMulti(_ *cobra.Command, args []string) error {
	narrative := args[0]
	if len(strings.TrimSpace(narrative)) < 100 {
		return fmt.Errorf("narrative must be at least 100 chars for a podcast")
	}

	ctx := context.Background()
	authCtx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}
	agentID, err := authCtx.RequireAgentID()
	if err != nil {
		return err
	}

	slug := voiceMultiSlug
	if slug == "" {
		slug = deriveSlug(narrative)
	}
	if err := validateSlug(slug); err != nil {
		return err
	}
	outputPath := voiceMultiOutput
	if outputPath == "" {
		outputPath = filepath.Join(voiceDefaultPodDir, slug+".wav")
	}

	// Resolve narrator + companion. CLI flag overrides beat STYLE.md.
	sound := loadAgentSound()
	narrator, err := resolvePodcastSpeaker(
		"narrator", voiceMultiNarratorVoice, voiceMultiNarratorStyle, voiceMultiNarratorName,
		sound.Narrator, "Host",
	)
	if err != nil {
		return err
	}
	companion, err := resolvePodcastSpeaker(
		"companion", voiceMultiCompanionVoice, voiceMultiCompanionStyle, voiceMultiCompanionName,
		sound.Companion, "Guest",
	)
	if err != nil {
		return err
	}
	if narrator.VoiceID == companion.VoiceID {
		return fmt.Errorf("narrator and companion must use different voices (both set to %q)", narrator.VoiceID)
	}

	targetLength := voiceMultiTargetLength
	if targetLength == "" {
		targetLength = voiceDefaultPodcastLength
	}

	secrets, err := fetchVoiceSecrets(authCtx, agentID, true)
	if err != nil {
		return err
	}

	// Pass 1 — ideate
	ideateSystem, err := prompts.LoadAndRender(voiceSkillPod, voicePromptPodIdeateS, map[string]string{
		"target_length":     targetLength,
		"speaker_1_profile": voice.RenderSpeakerProfile(narrator),
		"speaker_2_profile": voice.RenderSpeakerProfile(companion),
	})
	if err != nil {
		return err
	}
	ideateUser, err := prompts.LoadAndRender(voiceSkillPod, voicePromptPodIdeateU, map[string]string{
		"narrative": narrative,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "→ ideate (Opus 4.6 + 16k thinking)…")
	ideateResp, err := callOpus(ctx, secrets, ideateSystem, ideateUser)
	if err != nil {
		return fmt.Errorf("ideate: %w", err)
	}
	preScript := ideateResp.TextOutput()
	if preScript == "" {
		return fmt.Errorf("ideate returned empty text (stop_reason=%q)", ideateResp.StopReason)
	}

	// Pass 2 — author
	authorSystem, err := prompts.LoadAndRender(voiceSkillPod, voicePromptPodAuthorS, map[string]string{
		"target_length":     targetLength,
		"speaker_1_profile": voice.RenderSpeakerProfile(narrator),
		"speaker_2_profile": voice.RenderSpeakerProfile(companion),
		"pre_script_json":   preScript,
	})
	if err != nil {
		return err
	}
	authorUser, err := prompts.LoadAndRender(voiceSkillPod, voicePromptPodAuthorU, map[string]string{
		"narrative": narrative,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "→ author (Opus 4.6 + 16k thinking)…")
	authorResp, err := callOpus(ctx, secrets, authorSystem, authorUser)
	if err != nil {
		return fmt.Errorf("author: %w", err)
	}
	script, err := voice.ParsePodcastScript(authorResp.TextOutput())
	if err != nil {
		return err
	}

	// Validate that every line's speaker matches one of our two.
	for _, line := range script.Dialogue {
		if line.Speaker != narrator.Name && line.Speaker != companion.Name {
			return fmt.Errorf(
				"author pass emitted unknown speaker %q (expected %q or %q)",
				line.Speaker, narrator.Name, companion.Name,
			)
		}
	}

	fmt.Fprintln(os.Stderr, "→ render (Gemini TTS multi-speaker)…")
	geminiPrompt := voice.RenderPodcastGeminiPrompt(script, narrator, companion)
	pcm, err := llm.MultiSpeakerTTS(ctx, secrets.GeminiKey, geminiPrompt, []llm.Speaker{
		{Speaker: narrator.Name, VoiceName: narrator.VoiceID},
		{Speaker: companion.Name, VoiceName: companion.VoiceID},
	})
	if err != nil {
		return fmt.Errorf("gemini multi-speaker: %w", err)
	}
	if err := writeWav(outputPath, pcm); err != nil {
		return err
	}

	// Sidecar .meta.json with podcast-specific metadata.
	metaPath := strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + ".meta.json"
	meta := map[string]any{
		"slug":           slug,
		"title":          script.Title,
		"cold_open_note": script.ColdOpenNote,
		"line_count":     len(script.Dialogue),
	}
	if err := writeMetaSidecar(metaPath, meta); err != nil {
		return fmt.Errorf("podcast WAV written but sidecar failed: %w", err)
	}

	fmt.Printf("Generated podcast %s → %s (+ %s)\n", slug, outputPath, metaPath)
	return nil
}

// resolvePodcastSpeaker merges CLI flag overrides with the STYLE.md
// entry and returns a fully populated SpeakerProfile, or an actionable
// error if key fields are missing.
func resolvePodcastSpeaker(
	role, flagVoice, flagStyle, flagName string,
	styleEntry *voice.StyleVoice,
	defaultName string,
) (voice.SpeakerProfile, error) {
	voiceID := flagVoice
	clause := flagStyle
	name := flagName
	personality := ""
	if styleEntry != nil {
		if voiceID == "" {
			voiceID = styleEntry.Voice
		}
		if clause == "" {
			clause = styleEntry.BehavioralClause
		}
		personality = styleEntry.Personality
	}
	if voiceID == "" || clause == "" {
		return voice.SpeakerProfile{}, fmt.Errorf(
			"%s voice + behavioral clause required — either fill STYLE.md Sound section or pass --%s-voice / --%s-style",
			role, role, role,
		)
	}
	if !voice.IsValidGeminiVoice(voiceID) {
		return voice.SpeakerProfile{}, fmt.Errorf("%s voice %q is not in the Gemini roster", role, voiceID)
	}
	if name == "" {
		name = defaultName
	}
	return voice.SpeakerProfile{
		Name:             name,
		Role:             role,
		VoiceID:          voiceID,
		BehavioralClause: clause,
		Personality:      personality,
	}, nil
}

func writeMetaSidecar(path string, meta map[string]any) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create sidecar: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(meta); err != nil {
		return fmt.Errorf("encode sidecar: %w", err)
	}
	return nil
}

func truncateVoicePreview(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
