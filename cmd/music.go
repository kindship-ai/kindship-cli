package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"

	"github.com/spf13/cobra"
)

var (
	musicGeneratePrompt       string
	musicGenerateDurationMs   int
	musicGenerateOutputFormat string
	musicGenerateOutput       string
)

var musicCmd = &cobra.Command{
	Use:   "music",
	Short: "Music generation commands",
	Long: `Generate short instrumental "signature" music tracks via ElevenLabs.

Signature tracks live at /workspace/documents/music/<slug>.mp3, inside
the agent's documents folder. kindship-docs-sync carries them to the
docs repo on GitHub, so your library survives container destroy.

See the kindship-music skill (~/.claude/skills/kindship-music/SKILL.md)
for the full workflow, prompt-craft notes, and incorporation into
Remotion videos.

TL;DR:

  kindship music generate signature-contemplative \
    --prompt "ambient neoclassical piano, 70 BPM, minor key, pensive" \
    --duration-ms 20000`,
}

var musicGenerateCmd = &cobra.Command{
	Use:   "generate <slug>",
	Short: "Generate a signature music track",
	Long: `Generate a short instrumental music track via the Kindship
proxy to ElevenLabs (the API key is server-side only — the CLI never
sees it).

The generated mp3 is streamed to <output> (default
/workspace/documents/music/<slug>.mp3). That directory is inside the
agent's documents folder and gets synced to GitHub via
kindship-docs-sync, so tracks are durable across container destroy.

Rate limit: 2 generations per minute per account. 429 responses include
a Retry-After header which this CLI surfaces in the error message.

Examples:
  kindship music generate signature-contemplative \
    --prompt "ambient neoclassical piano, 70 BPM, minor key, pensive" \
    --duration-ms 20000
  kindship music generate signature-kinetic \
    --prompt "upbeat electronic pulse, 110 BPM, light percussion, forward motion" \
    --duration-ms 12000`,
	Args: cobra.ExactArgs(1),
	RunE: runMusicGenerate,
}

func init() {
	musicGenerateCmd.Flags().StringVar(&musicGeneratePrompt, "prompt", "", "style prompt — genre, instrument, tempo, mood (required, 8-1000 chars)")
	musicGenerateCmd.Flags().IntVar(&musicGenerateDurationMs, "duration-ms", 0, "target duration in milliseconds (required, 3000-120000)")
	musicGenerateCmd.Flags().StringVar(&musicGenerateOutputFormat, "output-format", "", "output format (default mp3_44100_128; only supported format today)")
	musicGenerateCmd.Flags().StringVar(&musicGenerateOutput, "output", "", "destination path (default /workspace/documents/music/<slug>.mp3)")
	musicGenerateCmd.Flags().StringVar(&videoFormat, "format", "text", "CLI success summary: text (default) or json")
	_ = musicGenerateCmd.MarkFlagRequired("prompt")
	_ = musicGenerateCmd.MarkFlagRequired("duration-ms")

	musicCmd.AddCommand(musicGenerateCmd)
	rootCmd.AddCommand(musicCmd)
}

func runMusicGenerate(_ *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	slug := args[0]
	if err := validateSlug(slug); err != nil {
		return err
	}

	// Client-side sanity on --duration-ms so we fail fast without a round-
	// trip (numeric bounds are invariant across languages). We do NOT
	// range-check --prompt client-side: Go `len()` counts bytes while the
	// server Zod schema counts JavaScript string length (UTF-16 code
	// units), so any byte-length check here would disagree with the server
	// on non-ASCII prompts. The server is the authority for prompt length.
	if musicGenerateDurationMs < 3000 || musicGenerateDurationMs > 120000 {
		return fmt.Errorf("--duration-ms must be 3000-120000 (got %d)", musicGenerateDurationMs)
	}
	if musicGeneratePrompt == "" {
		return fmt.Errorf("--prompt must not be empty")
	}

	outputPath := resolveMusicOutputPath(slug, musicGenerateOutput)

	reqBody := api.MusicGenerateRequest{
		AgentID:      agentID,
		Slug:         slug,
		Prompt:       musicGeneratePrompt,
		DurationMs:   musicGenerateDurationMs,
		OutputFormat: musicGenerateOutputFormat,
	}
	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to encode request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/cli/music/generate", ctx.APIBaseURL)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(reqJSON))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kindship-CLI-Version", Version)

	// Music generation takes 8-30s typical; upstream maxDuration is 300s.
	// Give the HTTP client a matching ceiling so we don't abort mid-generate.
	client := &http.Client{Timeout: 310 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to call music generate: %w", err)
	}
	defer resp.Body.Close()

	// Success: status 200 + content-type audio/*. Stream body to disk.
	contentType := resp.Header.Get("Content-Type")
	if resp.StatusCode == http.StatusOK && startsWith(contentType, "audio/") {
		if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
			return fmt.Errorf("failed to create output dir: %w", err)
		}
		out, err := os.Create(outputPath)
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}
		defer out.Close()

		bytesWritten, err := io.Copy(out, resp.Body)
		if err != nil {
			return fmt.Errorf("failed to write audio: %w", err)
		}

		if videoFormat == "json" {
			return printJSON(map[string]any{
				"slug":        slug,
				"path":        outputPath,
				"bytes":       bytesWritten,
				"duration_ms": musicGenerateDurationMs,
			})
		}
		fmt.Printf("Generated %s (%d bytes, %.1fs) → %s\n",
			slug, bytesWritten, float64(musicGenerateDurationMs)/1000, outputPath)
		return nil
	}

	// Error paths: read the body, decode as JSON error if shaped right,
	// otherwise use handleNonJSONResponse for Cloudflare-style HTML pages.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read error response: %w", err)
	}

	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return fmt.Errorf("music generate failed: %w", err)
	}

	var errResp api.MusicGenerateErrorResponse
	if jsonErr := json.Unmarshal(body, &errResp); jsonErr != nil {
		return fmt.Errorf("music generate failed (%d): %s", resp.StatusCode, string(body))
	}

	// Surface Retry-After on 429 so the agent sees "wait 60s" rather
	// than a bare status code. The server sets the header; the message
	// body already says "2 generations per minute" but the header is
	// the canonical retry contract.
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := resp.Header.Get("Retry-After")
		if retryAfter == "" {
			retryAfter = "60"
		}
		msg := errResp.Error
		if msg == "" {
			msg = "rate limit"
		}
		return fmt.Errorf("music generate rate-limited (%s): retry after %s seconds", msg, retryAfter)
	}

	if errResp.Error != "" {
		return fmt.Errorf("music generate failed (%d): %s", resp.StatusCode, errResp.Error)
	}
	return fmt.Errorf("music generate failed (%d)", resp.StatusCode)
}

// resolveMusicOutputPath derives the default output path
// (/workspace/documents/music/<slug>.mp3) if --output was not set.
// Respects explicit --output verbatim.
func resolveMusicOutputPath(slug, explicit string) string {
	if explicit != "" {
		return explicit
	}
	return filepath.Join("/workspace", "documents", "music", slug+".mp3")
}

// startsWith is a tiny helper to avoid importing strings just for
// HasPrefix here — keeps the import set consistent with the rest of
// cmd/. The content-type check accepts "audio/mpeg", "audio/mp3",
// "audio/mpeg; charset=binary", etc.
func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
