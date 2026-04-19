package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"

	"github.com/spf13/cobra"
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

	voiceMultiSlug            string
	voiceMultiNarratorVoice   string
	voiceMultiNarratorStyle   string
	voiceMultiNarratorName    string
	voiceMultiCompanionVoice  string
	voiceMultiCompanionStyle  string
	voiceMultiCompanionName   string
	voiceMultiTargetLength    string
	voiceMultiOutput          string
)

// voiceCmd doubles as the single-speaker monologue path (`kindship voice
// "<narrative>"`) and the parent for `exact` / `multi` subcommands. Cobra
// dispatches by subcommand name first, so the positional narrative only
// kicks in when the first arg isn't "exact" or "multi".
var voiceCmd = &cobra.Command{
	Use:   "voice [narrative]",
	Short: "Voice-over and podcast generation (Gemini + Opus)",
	Long: `Generate voice-over monologues and two-speaker podcasts via a
three-stage pipeline: Opus plans, Opus writes, Gemini performs.

  kindship voice "<narrative>"          single-speaker voice-over
  kindship voice exact  ...             verbatim render, one or more lines
  kindship voice multi  "<narrative>"   two-speaker podcast

For the monologue path, pass the raw narrative as the positional arg.
The server runs two Opus passes (ideate → author) and renders via Gemini
Live using the narrator voice from your STYLE.md Sound section
(overridable with --voice and --style).

Output lands at /workspace/documents/voice/<slug>.wav by default.
Rate limit: 10 generations per minute per account.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runVoiceGenerate,
}

var voiceExactCmd = &cobra.Command{
	Use:   "exact",
	Short: "Render one or more texts verbatim — no Opus rewrite, dry voiceover mode",
	Long: `Render each text with the prescribed voice + behavioral clause,
verbatim. No Opus authoring — useful for pre-written voice-overs, video
chapter markers, fixed hooks.

Single-text mode:
  kindship voice exact --voice Kore --style "gentle, measured" \
    --text "Chapter one" --output out/01-chapter-one.wav

Batch mode (array of texts → directory of WAVs + manifest.json):
  kindship voice exact --voice Kore --style "gentle, measured" \
    --texts-file lines.json --output-dir voice-clips/

lines.json shape: {"texts": ["line 1", "line 2", ...]}
Batch mode paces requests 7 seconds apart to stay under rate limits.`,
	RunE: runVoiceExact,
}

var voiceMultiCmd = &cobra.Command{
	Use:   "multi <narrative>",
	Short: "Generate a two-speaker podcast from a narrative",
	Long: `Pass the raw narrative as the positional argument. The server
runs two Opus passes (ideate → author) to produce a two-speaker
dialogue, then renders via Gemini TTS multi-speaker mode.

Both speaker voices come from your STYLE.md Sound section (Narrator +
Companion), pick them for deliberate contrast. Overridable with the
--narrator-* and --companion-* flags.

Output lands at /workspace/documents/podcasts/<slug>.wav plus a sibling
<slug>.meta.json with title + cold_open_note. Rate limit: 2 podcasts
per minute per account.`,
	Args: cobra.ExactArgs(1),
	RunE: runVoiceMulti,
}

func init() {
	// voice (single-speaker / monologue) flags — attached to voiceCmd directly
	// because the monologue is the parent's RunE, not a subcommand.
	voiceCmd.Flags().StringVar(&voiceSlug, "slug", "", "output slug (kebab-case 2-63 chars); default derived from narrative")
	voiceCmd.Flags().StringVar(&voiceVoice, "voice", "", "Gemini voice ID; default from STYLE.md narrator entry")
	voiceCmd.Flags().StringVar(&voiceStyle, "style", "", "behavioral clause e.g. \"gravelly, measured, older scholar\"")
	voiceCmd.Flags().IntVar(&voiceTargetMinutes, "target-minutes", 0, "finished audio target length (default server-chosen, ~3)")
	voiceCmd.Flags().StringVar(&voiceOutput, "output", "", "destination path (default /workspace/documents/voice/<slug>.wav)")
	voiceCmd.Flags().StringVar(&voiceFormat, "format", "text", "success summary format: text (default) or json")

	// voice exact flags
	voiceExactCmd.Flags().StringVar(&voiceExactVoice, "voice", "", "Gemini voice ID (required)")
	voiceExactCmd.Flags().StringVar(&voiceExactStyle, "style", "", "behavioral clause (required)")
	voiceExactCmd.Flags().StringVar(&voiceExactText, "text", "", "single text to render; mutually exclusive with --texts-file")
	voiceExactCmd.Flags().StringVar(&voiceExactTextsFile, "texts-file", "", "JSON file with shape {\"texts\":[...]}; writes one WAV per text")
	voiceExactCmd.Flags().StringVar(&voiceExactOutput, "output", "", "single-text output path")
	voiceExactCmd.Flags().StringVar(&voiceExactOutputDir, "output-dir", "", "batch-mode output directory")
	_ = voiceExactCmd.MarkFlagRequired("voice")
	_ = voiceExactCmd.MarkFlagRequired("style")

	// voice multi (podcast) flags
	voiceMultiCmd.Flags().StringVar(&voiceMultiSlug, "slug", "", "output slug (kebab-case 2-63 chars); default derived from narrative")
	voiceMultiCmd.Flags().StringVar(&voiceMultiNarratorVoice, "narrator-voice", "", "override STYLE.md narrator voice")
	voiceMultiCmd.Flags().StringVar(&voiceMultiNarratorStyle, "narrator-style", "", "override STYLE.md narrator behavioral clause")
	voiceMultiCmd.Flags().StringVar(&voiceMultiNarratorName, "narrator-name", "", "override narrator display name")
	voiceMultiCmd.Flags().StringVar(&voiceMultiCompanionVoice, "companion-voice", "", "override STYLE.md companion voice")
	voiceMultiCmd.Flags().StringVar(&voiceMultiCompanionStyle, "companion-style", "", "override STYLE.md companion behavioral clause")
	voiceMultiCmd.Flags().StringVar(&voiceMultiCompanionName, "companion-name", "", "override companion display name")
	voiceMultiCmd.Flags().StringVar(&voiceMultiTargetLength, "target-length", "", "target episode length e.g. \"5-7 minutes\"")
	voiceMultiCmd.Flags().StringVar(&voiceMultiOutput, "output", "", "destination path (default /workspace/documents/podcasts/<slug>.wav)")

	voiceCmd.AddCommand(voiceExactCmd)
	voiceCmd.AddCommand(voiceMultiCmd)
	rootCmd.AddCommand(voiceCmd)
}

// deriveSlug builds a kebab-case slug from the first 40 chars of text.
// Falls back to "voice-<timestamp>" when the text has no usable tokens.
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

func runVoiceGenerate(cmd *cobra.Command, args []string) error {
	// With 0 args, show the help text — the parent command is also the
	// dispatcher for the `exact` and `multi` subcommands.
	if len(args) == 0 {
		return cmd.Help()
	}

	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}
	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	narrative := args[0]
	if len(strings.TrimSpace(narrative)) < 20 {
		return fmt.Errorf("narrative must be at least 20 chars")
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
		outputPath = filepath.Join("/workspace", "documents", "voice", slug+".wav")
	}

	reqBody := api.VoiceGenerateRequest{
		AgentID:       agentID,
		Slug:          slug,
		Narrative:     narrative,
		VoiceOverride: voiceVoice,
		StyleOverride: voiceStyle,
		TargetMinutes: voiceTargetMinutes,
	}
	resp, body, err := postVoiceAudio(ctx, "/api/cli/voice/generate", reqBody, 310*time.Second)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := writeAudioResponse(resp, body, outputPath); err != nil {
		return fmt.Errorf("voice generate failed: %w", err)
	}

	if voiceFormat == "json" {
		return printJSON(map[string]any{
			"slug": slug,
			"path": outputPath,
		})
	}
	fmt.Printf("Generated %s → %s\n", slug, outputPath)
	return nil
}

func runVoiceExact(_ *cobra.Command, _ []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}
	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	// Exactly one of --text or --texts-file is required.
	hasSingle := voiceExactText != ""
	hasBatch := voiceExactTextsFile != ""
	if hasSingle == hasBatch {
		return fmt.Errorf("exactly one of --text or --texts-file must be provided")
	}

	if hasSingle {
		if voiceExactOutput == "" {
			return fmt.Errorf("--output is required with --text")
		}
		bytesWritten, err := renderExactOne(ctx, agentID, voiceExactText, voiceExactOutput)
		if err != nil {
			return err
		}
		fmt.Printf("Rendered %d bytes → %s\n", bytesWritten, voiceExactOutput)
		return nil
	}

	if voiceExactOutputDir == "" {
		return fmt.Errorf("--output-dir is required with --texts-file")
	}
	return runVoiceExactBatch(ctx, agentID, voiceExactTextsFile, voiceExactOutputDir)
}

func renderExactOne(
	ctx *auth.Context,
	agentID string,
	text string,
	outputPath string,
) (int64, error) {
	reqBody := api.VoiceExactRequest{
		AgentID: agentID,
		Voice:   voiceExactVoice,
		Style:   voiceExactStyle,
		Text:    text,
	}
	resp, body, err := postVoiceAudio(ctx, "/api/cli/voice/exact", reqBody, 150*time.Second)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return writeAudioResponseReturningBytes(resp, body, outputPath)
}

func runVoiceExactBatch(ctx *auth.Context, agentID, textsFile, outDir string) error {
	raw, err := os.ReadFile(textsFile)
	if err != nil {
		return fmt.Errorf("failed to read --texts-file: %w", err)
	}
	var batch api.VoiceExactBatchFile
	if err := json.Unmarshal(raw, &batch); err != nil {
		return fmt.Errorf("failed to parse --texts-file (expected {\"texts\":[...]}): %w", err)
	}
	if len(batch.Texts) == 0 {
		return fmt.Errorf("--texts-file has no texts")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output dir: %w", err)
	}

	manifest := api.VoiceExactManifest{
		Voice:   voiceExactVoice,
		Style:   voiceExactStyle,
		Entries: make([]api.VoiceExactManifestEntry, 0, len(batch.Texts)),
	}

	const pacing = 7 * time.Second
	for i, text := range batch.Texts {
		slug := deriveSlug(text)
		name := fmt.Sprintf("%02d-%s.wav", i+1, slug)
		outPath := filepath.Join(outDir, name)

		bytesWritten, err := renderExactOne(ctx, agentID, text, outPath)
		if err != nil {
			return fmt.Errorf("line %d (%q): %w", i+1, truncateVoicePreview(text, 40), err)
		}
		manifest.Entries = append(manifest.Entries, api.VoiceExactManifestEntry{
			Index: i + 1,
			Text:  text,
			File:  name,
			Bytes: bytesWritten,
		})
		fmt.Printf("  [%d/%d] %s (%d bytes)\n", i+1, len(batch.Texts), name, bytesWritten)
		if i < len(batch.Texts)-1 {
			time.Sleep(pacing)
		}
	}

	manifestPath := filepath.Join(outDir, "manifest.json")
	f, err := os.Create(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(manifest); err != nil {
		return fmt.Errorf("failed to encode manifest: %w", err)
	}
	fmt.Printf("Wrote %d clips + %s\n", len(manifest.Entries), manifestPath)
	return nil
}

func runVoiceMulti(_ *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}
	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	narrative := args[0]
	if len(strings.TrimSpace(narrative)) < 100 {
		return fmt.Errorf("narrative must be at least 100 chars for a podcast")
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
		outputPath = filepath.Join("/workspace", "documents", "podcasts", slug+".wav")
	}

	reqBody := api.VoicePodcastRequest{
		AgentID:                agentID,
		Slug:                   slug,
		Narrative:              narrative,
		NarratorVoiceOverride:  voiceMultiNarratorVoice,
		NarratorStyleOverride:  voiceMultiNarratorStyle,
		NarratorNameOverride:   voiceMultiNarratorName,
		CompanionVoiceOverride: voiceMultiCompanionVoice,
		CompanionStyleOverride: voiceMultiCompanionStyle,
		CompanionNameOverride:  voiceMultiCompanionName,
		TargetLength:           voiceMultiTargetLength,
	}
	resp, body, err := postVoiceAudio(ctx, "/api/cli/voice/podcast", reqBody, 310*time.Second)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := writeAudioResponse(resp, body, outputPath); err != nil {
		return fmt.Errorf("voice multi failed: %w", err)
	}

	// Write the sidecar .meta.json from X-Kindship-* response headers.
	// The sidecar is required — a failure here is a failure of the whole
	// command, not a warning. (Per review: the contract promises both files.)
	title, _ := url.QueryUnescape(resp.Header.Get("X-Kindship-Podcast-Title"))
	coldOpen, _ := url.QueryUnescape(resp.Header.Get("X-Kindship-Cold-Open-Note"))
	lineCountHeader := resp.Header.Get("X-Kindship-Line-Count")
	meta := map[string]any{
		"slug":           slug,
		"title":          title,
		"cold_open_note": coldOpen,
		"line_count":     lineCountHeader,
	}
	metaPath := strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + ".meta.json"
	if err := writeMetaSidecar(metaPath, meta); err != nil {
		return fmt.Errorf("podcast WAV written but sidecar failed: %w", err)
	}

	fmt.Printf("Generated podcast %s → %s (+ %s)\n", slug, outputPath, metaPath)
	return nil
}

func writeMetaSidecar(path string, meta map[string]any) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create sidecar file: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(meta); err != nil {
		return fmt.Errorf("failed to encode sidecar JSON: %w", err)
	}
	return nil
}

// postVoiceAudio is the shared HTTP POST for all three voice subcommands.
// Returns the Response + pre-read error body (on non-2xx / non-audio) so
// the caller can route success vs. failure. On success the body is NOT
// read — caller streams it to disk.
func postVoiceAudio(
	ctx *auth.Context,
	path string,
	body any,
	timeout time.Duration,
) (*http.Response, []byte, error) {
	reqJSON, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to encode request: %w", err)
	}
	endpoint := ctx.APIBaseURL + path
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(reqJSON))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}
	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to call %s: %w", path, err)
	}

	contentType := resp.Header.Get("Content-Type")
	if resp.StatusCode == http.StatusOK && startsWith(contentType, "audio/") {
		return resp, nil, nil // success; caller streams body
	}

	errBody, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return resp, nil, fmt.Errorf("failed to read error response: %w", readErr)
	}
	// Reopen a no-op body so defer resp.Body.Close() in the caller is safe.
	resp.Body = io.NopCloser(bytes.NewReader(nil))

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := resp.Header.Get("Retry-After")
		if retryAfter == "" {
			retryAfter = "60"
		}
		var errResp api.VoiceErrorResponse
		_ = json.Unmarshal(errBody, &errResp)
		msg := errResp.Error
		if msg == "" {
			msg = "rate limit"
		}
		return resp, errBody, fmt.Errorf("rate-limited (%s): retry after %s seconds", msg, retryAfter)
	}

	if err := handleNonJSONResponse(resp.StatusCode, errBody); err != nil {
		return resp, errBody, err
	}
	var errResp api.VoiceErrorResponse
	if jsonErr := json.Unmarshal(errBody, &errResp); jsonErr != nil {
		return resp, errBody, fmt.Errorf("failed (%d): %s", resp.StatusCode, string(errBody))
	}
	if errResp.Error != "" {
		return resp, errBody, fmt.Errorf("failed (%d): %s", resp.StatusCode, errResp.Error)
	}
	return resp, errBody, fmt.Errorf("failed (%d)", resp.StatusCode)
}

func writeAudioResponse(resp *http.Response, _ []byte, outputPath string) error {
	_, err := writeAudioResponseReturningBytes(resp, nil, outputPath)
	return err
}

func writeAudioResponseReturningBytes(
	resp *http.Response,
	_ []byte,
	outputPath string,
) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return 0, fmt.Errorf("failed to create output dir: %w", err)
	}
	out, err := os.Create(outputPath)
	if err != nil {
		return 0, fmt.Errorf("failed to create output file: %w", err)
	}
	defer out.Close()

	n, err := io.Copy(out, resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to write audio: %w", err)
	}
	return n, nil
}

func truncateVoicePreview(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
