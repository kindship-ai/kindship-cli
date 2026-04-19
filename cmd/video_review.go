package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"

	"github.com/spf13/cobra"
)

var (
	videoReviewOutput string
	videoReviewFormat string
	videoReviewPrompt string
	videoReviewModel  string
)

var videoReviewCmd = &cobra.Command{
	Use:   "review <slug>",
	Short: "Review the cached MP4 with Gemini 3.1 Pro (scene-by-scene UI critique)",
	Long: `Send the cached MP4 of <slug> to Gemini 3.1 Pro along with the
bundled scene-by-scene UI review rubric. The model walks the video
chronologically and reports layout, alignment, hierarchy, edge-safety,
and visual-stability issues for every scene.

Requires the latest revision to have a cached MP4 — run
'kindship video render <slug>' first if you haven't already.

The video stays on Kindship's servers (no local download); we stream
it from S3 directly to Gemini's Files API. Cost: ~$0.10–0.30 per
review depending on video length.

Examples:
  kindship video review arcane-library-full-glory
  kindship video review arcane-library-full-glory --output review.md
  kindship video review arcane-library-full-glory --format json > review.json
  kindship video review arcane-library-full-glory --prompt "Just rate the typography"`,
	Args: cobra.ExactArgs(1),
	RunE: runVideoReview,
}

func init() {
	videoReviewCmd.Flags().StringVar(&videoReviewOutput, "output", "", "save the review to a file (default: print to stdout)")
	videoReviewCmd.Flags().StringVar(&videoReviewFormat, "format", "text", "output format: text, markdown, or json")
	videoReviewCmd.Flags().StringVar(&videoReviewPrompt, "prompt", "", "override the bundled review rubric (server falls back to default if empty)")
	videoReviewCmd.Flags().StringVar(&videoReviewModel, "model", "", "override the Gemini model id (default: gemini-3.1-pro-preview)")
	videoCmd.AddCommand(videoReviewCmd)
}

func runVideoReview(_ *cobra.Command, args []string) error {
	slug := args[0]
	if err := validateSlug(slug); err != nil {
		return err
	}

	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}
	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	// Pre-flight status so we can surface a clean error before the server
	// has to stream the (missing) MP4 to Gemini.
	status, err := fetchVideoStatus(ctx, agentID, slug)
	if err != nil {
		return err
	}
	if status.Revision == nil {
		return fmt.Errorf("no published revision for %q — run 'kindship video publish %s --title \"...\"' first", slug, slug)
	}
	if status.Revision.MP4RenderStatus != "done" {
		return fmt.Errorf("no cached MP4 for %q (status=%s) — run 'kindship video render %s' first", slug, status.Revision.MP4RenderStatus, slug)
	}

	body := api.VideoReviewRequest{
		Prompt: videoReviewPrompt,
		Model:  videoReviewModel,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/cli/videos/%s/review?agent_id=%s",
		ctx.APIBaseURL, url.PathEscape(slug), agentID)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create review request: %w", err)
	}
	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kindship-CLI-Version", Version)

	if videoReviewFormat != "json" {
		fmt.Printf("Reviewing %s with Gemini... (this typically takes 30-90s)\n", slug)
	}

	// Match the server's 300s maxDuration with a 30s network buffer.
	client := &http.Client{Timeout: 330 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("review request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}
	if err := handleNonJSONResponse(resp.StatusCode, respBody); err != nil {
		return fmt.Errorf("review failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var errResp api.VideoReviewResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("review failed: %s", errResp.Error)
		}
		return fmt.Errorf("review failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var reviewResp api.VideoReviewResponse
	if err := json.Unmarshal(respBody, &reviewResp); err != nil {
		return fmt.Errorf("failed to parse review response: %w", err)
	}

	if videoReviewOutput != "" {
		if err := os.WriteFile(videoReviewOutput, []byte(reviewResp.Review), 0o644); err != nil {
			return fmt.Errorf("failed to write %s: %w", videoReviewOutput, err)
		}
		if videoReviewFormat != "json" {
			fmt.Printf("Saved review to %s (%d chars, model=%s, %.1fs total)\n",
				videoReviewOutput, len(reviewResp.Review), reviewResp.Model,
				float64(reviewResp.TotalMS)/1000)
		}
	}

	switch videoReviewFormat {
	case "json":
		return printJSON(reviewResp)
	case "markdown":
		if videoReviewOutput == "" {
			fmt.Println(reviewResp.Review)
		}
		return nil
	default: // text
		if videoReviewOutput == "" {
			fmt.Println(reviewResp.Review)
			fmt.Printf("\n— %s, %.1fMB video, %.1fs total (upload %.1fs, generate %.1fs)\n",
				reviewResp.Model, reviewResp.VideoSizeMB,
				float64(reviewResp.TotalMS)/1000,
				float64(reviewResp.UploadMS)/1000,
				float64(reviewResp.GenerateMS)/1000)
		}
		return nil
	}
}
