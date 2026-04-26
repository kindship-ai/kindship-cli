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
	voicePromptMonoTagS    = "monologue-tag-system"
	voicePromptMonoTagU    = "monologue-tag-user"
	voicePromptPodIdeateS  = "podcast-ideate-system"
	voicePromptPodIdeateU  = "podcast-ideate-user"
	voicePromptPodAuthorS  = "podcast-author-system"
	voicePromptPodAuthorU  = "podcast-author-user"

	voiceOpusModel            = "claude-opus-4-6"
	voiceOpusMaxTokens        = 50000
	voiceOpusThinkBudget      = 16000
	voiceDefaultTargetMinutes = 3
	voiceDefaultPodcastLength = "5-7 minutes"

	// Voice-overs are per-video, one-off: they travel WITH the video,
	// not in a shared artifact library. Default to a cwd-relative
	// `narration/` directory — when the agent runs `kindship voice …`
	// from inside a video workspace, the WAV lands exactly where the
	// CLI's archive walker picks it up (see cmd/video.go's narration
	// sibling walk) and where the composition expects it via
	// `new URL('./narration/<slug>.wav', import.meta.url).href`.
	voiceDefaultMonoSubdir = "narration"
	// Podcasts are standalone artifacts, not tied to a video. Keep
	// them in the shared documents library.
	voiceDefaultPodDir = "/workspace/documents/podcasts"
	voiceBatchPacingSecs  = 7
	voiceDefaultSpeakerRole = "narrator"
)

var (
	voiceSlug          string
	voiceVoice         string
	voiceStyle         string
	voicePersonality   string
	voiceKeepDry       bool
	voiceTargetMinutes int
	voiceOutput        string
	voiceFormat        string

	voiceExactVoice     string
	voiceExactStyle     string
	voiceExactText      string
	voiceExactTextsFile string
	voiceExactOutput    string
	voiceExactOutputDir string

	voiceMultiSlug               string
	voiceMultiNarratorVoice      string
	voiceMultiNarratorStyle      string
	voiceMultiNarratorName       string
	voiceMultiNarratorPersonality string
	voiceMultiCompanionVoice     string
	voiceMultiCompanionStyle     string
	voiceMultiCompanionName      string
	voiceMultiCompanionPersonality string
	voiceMultiTargetLength       string
	voiceMultiOutput             string

	voiceUnderstandPrompt     string
	voiceUnderstandPromptFile string
	voiceUnderstandSchema     string
	voiceUnderstandSchemaFile string
	voiceUnderstandModel      string
	voiceUnderstandOutput     string

	voiceNoTranscript bool
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
renders via Gemini TTS. Narrator voice + behavioral clause + optional
personality come in as --voice / --style / --personality flags; your
agent skill (kindship-voice) extracts them from STYLE.md and passes
them here. The CLI itself no longer parses STYLE.md.

By default the WAV lands at ./narration/<slug>.wav relative to the
current directory. Run from inside a video workspace (cd /workspace/
videos/<video>) and the file drops into place next to composition.mjs,
ready for the canonical audio import:

  const voice = new URL('./narration/<slug>.wav', import.meta.url).href;
  <Audio src={voice} />

The CLI's video archive walker picks up narration/ automatically.`,
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

var voiceUnderstandCmd = &cobra.Command{
	Use:   "understanding <audio-path>",
	Short: "Send audio to Gemini with a caller-supplied prompt and (optional) JSON schema",
	Long: `Generic audio-understanding primitive. You supply the prompt and
(optionally) a JSON schema; the CLI returns what the model said —
raw, unwrapped, unnormalized.

The same primitive serves sentence-level alignment, plain
transcription, speaker classification, emotion arcs, chapter
extraction — anything Gemini will do to audio. See the
` + "`kindship-voice`" + ` skill for the sentence-alignment recipe you'll
reach for most of the time.

Examples:
  # sentence-level alignment
  kindship voice understanding narration/foo.wav \
    --output narration/foo.aligned.json \
    --prompt "Transcribe with sentence-level timestamps as JSON matching the schema." \
    --schema '{"type":"object",…}'

  # plain transcription to stdout
  kindship voice understanding clip.wav \
    --prompt "Transcribe this audio verbatim."`,
	Args: cobra.ExactArgs(1),
	RunE: runVoiceUnderstand,
}

var voiceMultiCmd = &cobra.Command{
	Use:   "multi <narrative>",
	Short: "Generate a two-speaker podcast from a narrative",
	Long: `Pass the raw narrative as the positional argument. The CLI
runs two Opus passes (ideate → author) to produce a two-speaker
dialogue, then renders via Gemini TTS multi-speaker mode.

Narrator + companion voice / behavioral clause / optional personality
arrive as --narrator-* / --companion-* flags. Your agent skill
(create-podcast) extracts them from the STYLE.md Sound section and
passes them here; the CLI no longer parses STYLE.md directly.

Output lands at /workspace/documents/podcasts/<slug>.wav plus a
sibling <slug>.meta.json with title + cold_open_note.`,
	Args: cobra.ExactArgs(1),
	RunE: runVoiceMulti,
}

func init() {
	voiceCmd.Flags().StringVar(&voiceSlug, "slug", "", "output slug (kebab-case 2-63 chars); default derived from narrative")
	voiceCmd.Flags().StringVar(&voiceVoice, "voice", "", "Gemini voice ID from the 30-voice roster (required)")
	voiceCmd.Flags().StringVar(&voiceStyle, "style", "", "behavioral clause, e.g. \"gravelly, measured, older scholar\" (required)")
	voiceCmd.Flags().StringVar(&voicePersonality, "personality", "", "optional personality paragraph fed to the Opus ideate/author passes")
	voiceCmd.Flags().BoolVar(&voiceKeepDry, "keep-dry", false, "skip the Opus audio-tag injection pass (no [pause]/[breath] tags inserted)")
	voiceCmd.Flags().IntVar(&voiceTargetMinutes, "target-minutes", 0, "finished audio target length (default server-chosen, ~3)")
	voiceCmd.Flags().StringVar(&voiceOutput, "output", "", "destination path (default ./narration/<slug>.wav relative to cwd — run from inside a video dir)")
	voiceCmd.Flags().StringVar(&voiceFormat, "format", "text", "success summary format: text (default) or json")
	voiceCmd.Flags().BoolVar(&voiceNoTranscript, "no-transcript", false,
		"skip writing the <slug>.transcript.txt sidecar next to the WAV")
	// --voice / --style are semantically required (see flag descriptions
	// + runVoiceGenerate's check) but we do NOT call MarkFlagRequired on
	// them — Cobra's required-flag error short-circuits to a terse
	// "required flag(s) ... not set" message that skips our rich
	// transparency message pointing agents at the STYLE.md Sound section
	// + kindship-voice skill extraction recipe. The runtime check in
	// runVoiceGenerate surfaces the detailed guidance instead.

	voiceExactCmd.Flags().StringVar(&voiceExactVoice, "voice", "", "Gemini voice ID (required)")
	voiceExactCmd.Flags().StringVar(&voiceExactStyle, "style", "", "behavioral clause (required)")
	voiceExactCmd.Flags().StringVar(&voiceExactText, "text", "", "single text to render; mutually exclusive with --texts-file")
	voiceExactCmd.Flags().StringVar(&voiceExactTextsFile, "texts-file", "", "JSON file with shape {\"texts\":[...]}; writes one WAV per text")
	voiceExactCmd.Flags().StringVar(&voiceExactOutput, "output", "", "single-text output path")
	voiceExactCmd.Flags().StringVar(&voiceExactOutputDir, "output-dir", "", "batch-mode output directory")
	_ = voiceExactCmd.MarkFlagRequired("voice")
	_ = voiceExactCmd.MarkFlagRequired("style")

	voiceMultiCmd.Flags().StringVar(&voiceMultiSlug, "slug", "", "output slug (kebab-case 2-63 chars); default derived from narrative")
	voiceMultiCmd.Flags().StringVar(&voiceMultiNarratorVoice, "narrator-voice", "", "Gemini voice ID for the narrator (required)")
	voiceMultiCmd.Flags().StringVar(&voiceMultiNarratorStyle, "narrator-style", "", "narrator behavioral clause (required)")
	voiceMultiCmd.Flags().StringVar(&voiceMultiNarratorName, "narrator-name", "", "narrator display name (default: \"Host\")")
	voiceMultiCmd.Flags().StringVar(&voiceMultiNarratorPersonality, "narrator-personality", "", "optional narrator personality paragraph for the Opus passes")
	voiceMultiCmd.Flags().StringVar(&voiceMultiCompanionVoice, "companion-voice", "", "Gemini voice ID for the companion (required)")
	voiceMultiCmd.Flags().StringVar(&voiceMultiCompanionStyle, "companion-style", "", "companion behavioral clause (required)")
	voiceMultiCmd.Flags().StringVar(&voiceMultiCompanionName, "companion-name", "", "companion display name (default: \"Guest\")")
	voiceMultiCmd.Flags().StringVar(&voiceMultiCompanionPersonality, "companion-personality", "", "optional companion personality paragraph for the Opus passes")
	voiceMultiCmd.Flags().StringVar(&voiceMultiTargetLength, "target-length", "", "target episode length e.g. \"6-8 minutes\" (default \"5-7 minutes\")")
	voiceMultiCmd.Flags().StringVar(&voiceMultiOutput, "output", "", "destination path (default /workspace/documents/podcasts/<slug>.wav)")
	// Same reasoning as the monologue path: the rich runtime error in
	// resolvePodcastSpeaker points at STYLE.md + the create-podcast
	// skill, so we skip Cobra's MarkFlagRequired which would short-
	// circuit to a terse "required flag(s) ... not set" message and
	// swallow our guidance.

	voiceUnderstandCmd.Flags().StringVar(&voiceUnderstandPrompt, "prompt", "",
		"inline prompt for Gemini; mutually exclusive with --prompt-file")
	voiceUnderstandCmd.Flags().StringVar(&voiceUnderstandPromptFile, "prompt-file", "",
		"read the prompt from this file")
	voiceUnderstandCmd.Flags().StringVar(&voiceUnderstandSchema, "schema", "",
		"JSON schema for structured output; forces responseMimeType=application/json")
	voiceUnderstandCmd.Flags().StringVar(&voiceUnderstandSchemaFile, "schema-file", "",
		"read the JSON schema from this file")
	voiceUnderstandCmd.Flags().StringVar(&voiceUnderstandModel, "model", "gemini-2.5-flash",
		"Gemini model id (gemini-2.5-flash default; gemini-2.5-pro for paragraphs / longer context)")
	voiceUnderstandCmd.Flags().StringVar(&voiceUnderstandOutput, "output", "",
		"output path (default: stdout). Use e.g. narration/<slug>.aligned.json to land a sidecar next to the WAV.")

	voiceCmd.AddCommand(voiceExactCmd)
	voiceCmd.AddCommand(voiceMultiCmd)
	voiceCmd.AddCommand(voiceUnderstandCmd)
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

// validateSlug lives in cmd/video.go — shared across commands.
// STYLE.md parsing now lives in the skills (kindship-voice, create-podcast);
// the CLI takes voice + behavioral clause + personality as explicit flags.

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
			// Match the probe-4 pinned request shape so CLI-generated
			// audio stays at full Opus 4.6 effort.
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
		// Cwd-relative — so running from inside a video workspace lands
		// the narration next to composition.mjs, and the publish walker
		// picks it up under "narration/<slug>.wav".
		outputPath = filepath.Join(voiceDefaultMonoSubdir, slug+".wav")
	}

	// Resolve the narrator profile from CLI flags. STYLE.md extraction
	// now lives in the kindship-voice skill — the CLI is purely a
	// parameter-driven renderer.
	narratorVoice := voiceVoice
	narratorStyle := voiceStyle
	narratorPersonality := voicePersonality
	if narratorVoice == "" || narratorStyle == "" {
		return fmt.Errorf(
			"--voice and --style are required. Extract them from the Narrator voice bullet in your STYLE.md Sound section and pass as flags; see the kindship-voice skill for the extraction recipe.",
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
		return fmt.Errorf("Opus ideate pass failed: %w (check LiteLLM key + LITELLM_BASE_URL)", err)
	}
	preScript := ideateResp.TextOutput()
	if preScript == "" {
		return fmt.Errorf("Opus ideate pass returned no text (stop_reason=%q, model=%s). Usually the narrative was rejected or the thinking budget ran out — shorten the narrative or retry", ideateResp.StopReason, voiceOpusModel)
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
		return fmt.Errorf("Opus author pass failed: %w", err)
	}
	script, err := voice.ParseMonologueScript(authorResp.TextOutput())
	if err != nil {
		return err
	}

	// Pass 3 — tag (skippable with --keep-dry).
	//
	// Opus inserts Gemini 3.1 Flash TTS audio tags ([pause], [breath],
	// [softly], etc.) into beat.text. The authored prose is preserved
	// verbatim — the pass is additive, not a rewrite. On parse failure
	// or a prose-rewrite detection we log and fall back to the untagged
	// script (render still succeeds; no tagged sidecar written).
	renderScript := script
	tagPassUsed := false
	if !voiceKeepDry {
		tagged, tagErr := runMonologueTagPass(ctx, secrets, profile, script)
		if tagErr != nil {
			fmt.Fprintf(os.Stderr, "→ tag (Opus): falling back to untagged script — %v\n", tagErr)
		} else {
			renderScript = tagged
			tagPassUsed = true
			fmt.Fprintln(os.Stderr, "→ tag (Opus 4.6): inserted audio tags")
		}
	}

	// Render via Gemini TTS REST (single voice, audio-out). Swapped off
	// Gemini Live — Live is for realtime bidirectional audio; Flash TTS
	// renders higher-quality voice-over with the same voice roster.
	geminiPrompt := voice.RenderMonologueGeminiPrompt(renderScript, profile)
	fmt.Fprintln(os.Stderr, "→ render (Gemini TTS)…")
	pcm, err := llm.SingleSpeakerTTS(ctx, secrets.GeminiKey, narratorVoice, geminiPrompt)
	if err != nil {
		return fmt.Errorf("Gemini TTS render failed (voice=%s): %w", narratorVoice, err)
	}
	if err := writeWav(outputPath, pcm); err != nil {
		return err
	}

	// Transcript sidecars — the authored raw beat text (no audio tags)
	// lands at <slug>.transcript.txt; when the tag pass ran, a sibling
	// <slug>.transcript.tagged.txt captures the tagged text Gemini
	// actually saw. Raw is best for captions / re-alignment / SEO;
	// tagged is for debugging delivery. --no-transcript suppresses both.
	transcriptPath := ""
	taggedTranscriptPath := ""
	if !voiceNoTranscript {
		transcriptPath = transcriptSidecarPath(outputPath)
		parts := make([]string, 0, len(script.Beats))
		for _, b := range script.Beats {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		transcript := strings.Join(parts, "\n\n")
		if err := os.WriteFile(transcriptPath, []byte(transcript), 0o644); err != nil {
			return fmt.Errorf("write transcript sidecar: %w", err)
		}

		if tagPassUsed {
			taggedTranscriptPath = taggedTranscriptSidecarPath(outputPath)
			taggedParts := make([]string, 0, len(renderScript.Beats))
			for _, b := range renderScript.Beats {
				if b.Text != "" {
					taggedParts = append(taggedParts, b.Text)
				}
			}
			taggedTranscript := strings.Join(taggedParts, "\n\n")
			if err := os.WriteFile(taggedTranscriptPath, []byte(taggedTranscript), 0o644); err != nil {
				return fmt.Errorf("write tagged transcript sidecar: %w", err)
			}
		}
	}

	if voiceFormat == "json" {
		out := map[string]any{
			"slug":  slug,
			"path":  outputPath,
			"title": script.Title,
			"beats": len(script.Beats),
		}
		if transcriptPath != "" {
			out["transcript_path"] = transcriptPath
		}
		if taggedTranscriptPath != "" {
			out["tagged_transcript_path"] = taggedTranscriptPath
		}
		out["tag_pass_used"] = tagPassUsed
		return printJSON(out)
	}
	tail := ""
	if transcriptPath != "" {
		if taggedTranscriptPath != "" {
			tail = fmt.Sprintf(" (+ %s + %s)", filepath.Base(transcriptPath), filepath.Base(taggedTranscriptPath))
		} else {
			tail = fmt.Sprintf(" (+ %s)", filepath.Base(transcriptPath))
		}
	}
	fmt.Printf("Generated %s → %s (%d beats)%s\n", slug, outputPath, len(script.Beats), tail)
	return nil
}

// transcriptSidecarPath returns the conventional <audio-stem>.transcript.txt
// path alongside the WAV. kindship voice understanding pairs with this
// artifact — agents who want sentence-level timings after rendering
// pass --text-file narration/<slug>.transcript.txt into the
// understanding subcommand via a --prompt-file if desired. We keep
// the transcript in a distinct file so other downstream tools
// (caption generators, re-alignment, diffing against Gemini's own
// transcription) can read it without parsing metadata.
func transcriptSidecarPath(audioPath string) string {
	ext := filepath.Ext(audioPath)
	return strings.TrimSuffix(audioPath, ext) + ".transcript.txt"
}

// taggedTranscriptSidecarPath is the sibling <audio-stem>.transcript.tagged.txt
// that captures the exact text Gemini TTS received — authored prose plus
// audio tags ([pause], [breath], etc.) inserted by the Opus tag pass.
// Useful for debugging delivery issues: if the render sounds off, read
// the tagged transcript to see what the TTS was asked to do.
func taggedTranscriptSidecarPath(audioPath string) string {
	ext := filepath.Ext(audioPath)
	return strings.TrimSuffix(audioPath, ext) + ".transcript.tagged.txt"
}

// audioTagPattern matches ONLY the tag vocabulary the Opus tag pass is
// allowed to emit — match exactly what monologue-tag-system.md
// whitelists, nothing else. An unknown-token bracket span is treated
// as prose (not stripped), which surfaces as a validator mismatch and
// triggers the soft fallback. This is deliberate: a loose `\[[^\]]*\]`
// regex would hide prose rewrites behind bracket-shaped edits and
// would also mangle authored prose that happens to contain brackets.
//
// Keep in sync with kindship-voice/prompts/monologue-tag-system.md
// AUDIO TAG VOCABULARY section.
var audioTagPattern = regexp.MustCompile(`\[(pause|short pause|long pause|breath|inhale|softly|emphasizes|slowly|thoughtfully)\]`)

// normalizeForProseCheck strips known audio tags from `s` and collapses
// whitespace, returning a form suitable for comparing a tagged beat
// against its authored original. Authored prose (which may legitimately
// contain bracketed spans like [sic] or [redacted]) passes through
// untouched; only the whitelisted audio tags are removed.
func normalizeForProseCheck(s string) string {
	return strings.Join(strings.Fields(audioTagPattern.ReplaceAllString(s, " ")), " ")
}

// runMonologueTagPass runs the third Opus pass (tag) over an already-
// authored monologue script. Returns a new *MonologueScript with audio
// tags inlined into beat.text, or an error if the pass fails or the
// tagged output is not prose-preserving.
//
// Callers (runVoiceGenerate) should treat errors as a soft fallback —
// log to stderr, continue rendering with the untagged script. The tag
// pass is an enhancement, not a requirement.
func runMonologueTagPass(
	ctx context.Context,
	secrets *voiceSecrets,
	profile voice.SpeakerProfile,
	authored *voice.MonologueScript,
) (*voice.MonologueScript, error) {
	authoredJSON, err := json.Marshal(authored)
	if err != nil {
		return nil, fmt.Errorf("serialize authored script: %w", err)
	}
	tagSystem, err := prompts.LoadAndRender(voiceSkillMono, voicePromptMonoTagS, map[string]string{
		"speaker_profile":      voice.RenderSpeakerProfile(profile),
		"authored_script_json": string(authoredJSON),
	})
	if err != nil {
		return nil, fmt.Errorf("load tag system prompt: %w", err)
	}
	tagUser, err := prompts.LoadAndRender(voiceSkillMono, voicePromptMonoTagU, map[string]string{})
	if err != nil {
		return nil, fmt.Errorf("load tag user prompt: %w", err)
	}
	fmt.Fprintln(os.Stderr, "→ tag (Opus 4.6 + 16k thinking)…")
	tagResp, err := callOpus(ctx, secrets, tagSystem, tagUser)
	if err != nil {
		return nil, fmt.Errorf("Opus tag pass failed: %w", err)
	}
	raw := tagResp.TextOutput()
	if raw == "" {
		return nil, fmt.Errorf("Opus tag pass returned empty text (stop_reason=%q)", tagResp.StopReason)
	}
	tagged, err := voice.ParseMonologueScript(raw)
	if err != nil {
		return nil, fmt.Errorf("parse tagged script: %w", err)
	}
	if len(tagged.Beats) != len(authored.Beats) {
		return nil, fmt.Errorf("tag pass changed beat count (%d → %d)", len(authored.Beats), len(tagged.Beats))
	}
	// Prose-preservation validator — tagged.Beats[i].text minus bracket
	// tags and whitespace must equal authored.Beats[i].text minus
	// whitespace. If it doesn't, Opus ignored the tags-only rule.
	for i := range tagged.Beats {
		if normalizeForProseCheck(tagged.Beats[i].Text) != normalizeForProseCheck(authored.Beats[i].Text) {
			return nil, fmt.Errorf("tag pass rewrote prose in beat %d (expected tags-only insertion)", i+1)
		}
	}
	return tagged, nil
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

	// Resolve narrator + companion from CLI flags. STYLE.md extraction
	// now lives in the create-podcast skill.
	narrator, err := resolvePodcastSpeaker(
		"narrator", voiceMultiNarratorVoice, voiceMultiNarratorStyle, voiceMultiNarratorName, voiceMultiNarratorPersonality, "Host",
	)
	if err != nil {
		return err
	}
	companion, err := resolvePodcastSpeaker(
		"companion", voiceMultiCompanionVoice, voiceMultiCompanionStyle, voiceMultiCompanionName, voiceMultiCompanionPersonality, "Guest",
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
		return fmt.Errorf("Opus ideate pass failed: %w (check LiteLLM key + LITELLM_BASE_URL)", err)
	}
	preScript := ideateResp.TextOutput()
	if preScript == "" {
		return fmt.Errorf("Opus ideate pass returned no text (stop_reason=%q, model=%s). Usually the narrative was rejected or the thinking budget ran out — lengthen / simplify the narrative or retry", ideateResp.StopReason, voiceOpusModel)
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
		return fmt.Errorf("Opus author pass failed: %w", err)
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

	// Chunked multi-speaker render. Gemini 3.1 Flash TTS preview has
	// long-horizon autoregressive drift — on episodes past ~3-4 min the
	// dominant voice loses ~20 dB of signal. Rendering in ~180s chunks
	// resets the model's hidden state each call, per-chunk EBU R128
	// loudnorm flattens level mismatches, and an equal-power linear
	// crossfade at every chunk boundary masks the residual per-chunk
	// voice-interpretation variance (Gemini picks slightly different
	// prosody for the same voice between separate calls). Chunk size
	// overridable via KINDSHIP_VOICE_CHUNK_SECONDS; 0 disables chunking.
	chunks := voice.ChunkDialogue(script.Dialogue, voice.ChunkTargetSeconds())
	if len(chunks) == 0 {
		return fmt.Errorf("authored dialogue produced no chunks (dialogue length=%d)", len(script.Dialogue))
	}
	fmt.Fprintf(os.Stderr, "→ render (Gemini TTS multi-speaker, %s)…\n", voice.SplitAnnouncement(chunks))

	speakers := []llm.Speaker{
		{Speaker: narrator.Name, VoiceName: narrator.VoiceID},
		{Speaker: companion.Name, VoiceName: companion.VoiceID},
	}
	chunkPCMs := make([][]byte, 0, len(chunks))
	for i, chunk := range chunks {
		chunkScript := &voice.PodcastScript{
			Title:    script.Title,
			Dialogue: chunk,
		}
		// ColdOpenNote belongs to the episode's opening, not every
		// chunk — only the first chunk carries it so the subsequent
		// chunks don't repeat the cold-open framing as director notes.
		if i == 0 {
			chunkScript.ColdOpenNote = script.ColdOpenNote
		}
		chunkPrompt := voice.RenderPodcastGeminiPrompt(chunkScript, narrator, companion)
		chunkStart := time.Now()
		chunkPCM, err := llm.MultiSpeakerTTS(ctx, secrets.GeminiKey, chunkPrompt, speakers)
		if err != nil {
			return fmt.Errorf("gemini multi-speaker (chunk %d/%d): %w", i+1, len(chunks), err)
		}
		normalizedPCM, err := voice.NormalizePCM(ctx, chunkPCM, 24000, voice.DefaultLoudnormTarget)
		if err != nil {
			return fmt.Errorf("loudnorm (chunk %d/%d): %w", i+1, len(chunks), err)
		}
		fmt.Fprintf(os.Stderr, "  chunk %d/%d: %d lines, %d→%d bytes, %.1fs\n",
			i+1, len(chunks), len(chunk), len(chunkPCM), len(normalizedPCM), time.Since(chunkStart).Seconds())
		chunkPCMs = append(chunkPCMs, normalizedPCM)
	}
	fullPCM, err := voice.ConcatWithCrossfade(chunkPCMs, 24000, voice.DefaultCrossfadeSeconds)
	if err != nil {
		return fmt.Errorf("concat chunks: %w", err)
	}
	if len(chunks) > 1 {
		fmt.Fprintf(os.Stderr, "  concatenated %d chunks with %.0fms crossfade\n",
			len(chunks), voice.DefaultCrossfadeSeconds*1000)
	}
	if err := writeWav(outputPath, fullPCM); err != nil {
		return err
	}

	// Sidecar .meta.json — summary metadata for show notes / listings.
	metaPath := strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + ".meta.json"
	meta := map[string]any{
		"slug":           slug,
		"title":          script.Title,
		"cold_open_note": script.ColdOpenNote,
		"line_count":     len(script.Dialogue),
		"chunk_count":    len(chunks),
	}
	if err := writeMetaSidecar(metaPath, meta); err != nil {
		return fmt.Errorf("podcast WAV written but sidecar failed: %w", err)
	}

	// Sidecar .script.json — full authored PodcastScript (title,
	// cold_open_note, every dialogue turn with speaker/text/
	// performance_hint). Ground truth for future debugging and the
	// dialogue-rule sanity checklist described in the create-podcast
	// skill. Suppressed by --no-transcript. Soft-fail: if we can't
	// write it, the WAV is already on disk and the episode is still
	// usable — log to stderr and continue. Agents can re-run if the
	// script sidecar is load-bearing for their workflow.
	scriptPath := ""
	if !voiceNoTranscript {
		candidate := strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + ".script.json"
		if err := writeScriptSidecar(candidate, script); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: script sidecar %s failed: %v (WAV still landed)\n", candidate, err)
		} else {
			scriptPath = candidate
		}
	}

	if scriptPath != "" {
		fmt.Printf("Generated podcast %s → %s (+ %s + %s)\n", slug, outputPath, filepath.Base(metaPath), filepath.Base(scriptPath))
	} else {
		fmt.Printf("Generated podcast %s → %s (+ %s)\n", slug, outputPath, filepath.Base(metaPath))
	}
	return nil
}

// writeScriptSidecar writes a PodcastScript to `path` as 2-space indented
// JSON. Closes the file even on encode failure. Caller treats any error
// as non-fatal — the WAV is the primary artifact.
func writeScriptSidecar(path string, script *voice.PodcastScript) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(script); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	return nil
}

// resolvePodcastSpeaker builds a SpeakerProfile from CLI flag values.
// STYLE.md extraction is a concern of the create-podcast skill; this
// function is purely a validator + default-filler.
func resolvePodcastSpeaker(
	role, flagVoice, flagStyle, flagName, flagPersonality, defaultName string,
) (voice.SpeakerProfile, error) {
	if flagVoice == "" || flagStyle == "" {
		return voice.SpeakerProfile{}, fmt.Errorf(
			"--%s-voice and --%s-style are required. Extract them from the %s bullet in your STYLE.md Sound section and pass as flags; see the create-podcast skill for the extraction recipe.",
			role, role, capitalize(role),
		)
	}
	if !voice.IsValidGeminiVoice(flagVoice) {
		return voice.SpeakerProfile{}, fmt.Errorf("%s voice %q is not in the Gemini roster", role, flagVoice)
	}
	name := flagName
	if name == "" {
		name = defaultName
	}
	return voice.SpeakerProfile{
		Name:             name,
		Role:             role,
		VoiceID:          flagVoice,
		BehavioralClause: flagStyle,
		Personality:      flagPersonality,
	}, nil
}

// capitalize is a local tiny helper — used only for error message
// pretty-printing ("narrator" → "Narrator"). strings.Title is deprecated
// and golang.org/x/text/cases would be overkill for this single use.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// runVoiceUnderstand wires `kindship voice understanding <audio>` to
// llm.UnderstandAudio. The subcommand is intentionally thin: the
// prompt + optional schema are caller-supplied; the CLI doesn't
// normalize or validate the response beyond "Gemini returned text".
// That leaves the subcommand composable across sentence alignment,
// transcription, chapter extraction, speaker turns, emotion arcs, or
// anything else agents invent on top.
//
// Gemini's inline-audio request body caps at ~15 MB base64 (~11 MB
// raw), roughly 3.5 min of 24 kHz mono WAV. Larger files need the
// Files API — call site out of scope for v1, reject here with a
// clear pointer.
const voiceUnderstandMaxAudioBytes = 15 * 1024 * 1024

func runVoiceUnderstand(_ *cobra.Command, args []string) error {
	ctx := context.Background()
	authCtx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}
	agentID, err := authCtx.RequireAgentID()
	if err != nil {
		return err
	}

	// Exactly one of --prompt / --prompt-file.
	hasInline := voiceUnderstandPrompt != ""
	hasFile := voiceUnderstandPromptFile != ""
	if hasInline == hasFile {
		return fmt.Errorf("exactly one of --prompt or --prompt-file must be provided")
	}
	prompt := voiceUnderstandPrompt
	if hasFile {
		raw, err := os.ReadFile(voiceUnderstandPromptFile)
		if err != nil {
			return fmt.Errorf("read --prompt-file: %w", err)
		}
		prompt = strings.TrimSpace(string(raw))
		if prompt == "" {
			return fmt.Errorf("--prompt-file %s is empty", voiceUnderstandPromptFile)
		}
	}

	// At most one of --schema / --schema-file; parse into a map or
	// leave nil to skip structured output.
	var schemaBytes []byte
	if voiceUnderstandSchema != "" && voiceUnderstandSchemaFile != "" {
		return fmt.Errorf("pass --schema OR --schema-file, not both")
	}
	if voiceUnderstandSchema != "" {
		schemaBytes = []byte(voiceUnderstandSchema)
	} else if voiceUnderstandSchemaFile != "" {
		raw, err := os.ReadFile(voiceUnderstandSchemaFile)
		if err != nil {
			return fmt.Errorf("read --schema-file: %w", err)
		}
		schemaBytes = raw
	}
	var schema map[string]any
	if schemaBytes != nil {
		if err := json.Unmarshal(schemaBytes, &schema); err != nil {
			return fmt.Errorf("parse --schema as JSON object: %w", err)
		}
	}

	// Read audio + reject oversized payloads up front. The 15 MB
	// ceiling is Gemini's, not ours.
	audioPath := args[0]
	audioBytes, err := os.ReadFile(audioPath)
	if err != nil {
		return fmt.Errorf("read audio file %s: %w", audioPath, err)
	}
	if len(audioBytes) > voiceUnderstandMaxAudioBytes {
		return fmt.Errorf(
			"audio file %s is %d bytes; Gemini inline audio caps at %d bytes (~3.5 min of 24kHz mono WAV). For longer files, use a shorter audio segment or wait for the Files-API follow-up",
			audioPath, len(audioBytes), voiceUnderstandMaxAudioBytes,
		)
	}
	audioMime, err := mimeForAudioExt(audioPath)
	if err != nil {
		return err
	}

	// Fetch the Gemini API key via the existing account-level
	// secret helper — same flow as voice / voice exact / voice multi.
	if authCtx.Method != auth.AuthMethodServiceKey {
		return fmt.Errorf(
			"kindship voice understanding must run inside an agent container (secrets endpoint is IP-whitelisted)",
		)
	}
	apiClient := api.NewClient(authCtx.APIBaseURL, false)
	secrets, err := apiClient.FetchSecrets(agentID, "gemini", authCtx.Token)
	if err != nil {
		return fmt.Errorf("fetch gemini secrets: %w", err)
	}
	apiKey := secrets["GEMINI_API_KEY"]
	if apiKey == "" {
		return fmt.Errorf("gemini secret missing GEMINI_API_KEY")
	}

	fmt.Fprintf(os.Stderr,
		"→ generating audio understanding via %s (%d bytes audio, ~15-55s depending on model + prompt)…\n",
		voiceUnderstandModel, len(audioBytes))

	result, err := llm.UnderstandAudio(
		ctx, apiKey, voiceUnderstandModel,
		audioBytes, audioMime, prompt, schema,
	)
	if err != nil {
		return fmt.Errorf("gemini understand: %w", err)
	}

	// Write to --output (atomic) or stdout.
	if voiceUnderstandOutput == "" {
		_, err := os.Stdout.Write([]byte(result))
		return err
	}
	if err := os.MkdirAll(filepath.Dir(voiceUnderstandOutput), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	tmp := voiceUnderstandOutput + ".tmp"
	// Atomic-write with cleanup on any failure path — matches the
	// pattern in cmd/update.go. Without the defer, a partial write
	// leaves <output>.tmp lying around next to the WAV and agents
	// see stray files they didn't ask for.
	writeOK := false
	defer func() {
		if !writeOK {
			_ = os.Remove(tmp)
		}
	}()
	if err := os.WriteFile(tmp, []byte(result), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, voiceUnderstandOutput); err != nil {
		return fmt.Errorf("rename %s → %s: %w", tmp, voiceUnderstandOutput, err)
	}
	writeOK = true
	fmt.Fprintf(os.Stderr, "→ wrote %s (%d bytes)\n", voiceUnderstandOutput, len(result))
	return nil
}

// mimeForAudioExt returns the MIME Gemini expects for the audio
// formats it supports inline. Pulled from
// https://ai.google.dev/gemini-api/docs/audio ("Supported audio
// formats"): wav, mp3, aiff, aac, ogg vorbis, flac. Unknown
// extensions reject locally with a clear list rather than being
// sent as audio/wav and surfacing as a remote 400.
func mimeForAudioExt(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".wav":
		return "audio/wav", nil
	case ".mp3":
		return "audio/mp3", nil
	case ".aiff", ".aif":
		return "audio/aiff", nil
	case ".aac":
		return "audio/aac", nil
	case ".ogg":
		return "audio/ogg", nil
	case ".flac":
		return "audio/flac", nil
	default:
		return "", fmt.Errorf(
			"unsupported audio extension %q (Gemini inline audio accepts: wav, mp3, aiff, aac, ogg, flac)",
			ext,
		)
	}
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
