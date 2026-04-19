package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"

	"github.com/spf13/cobra"
)

var videoStatusFormat string

var videoStatusCmd = &cobra.Command{
	Use:   "status <slug>",
	Short: "Show per-video deep dive (revision, deploy, render state)",
	Long: `Print the latest revision's full state for one video, plus a
suggested next action.

The output answers three questions:
  1. Has the renderer site been uploaded to S3? (controls whether the UI
     Download button appears + whether 'render' needs to upload first.)
  2. Has an MP4 been cached? (controls whether 'download' is free or
     needs a fresh ~$0.01 Lambda invocation.)
  3. What was the last render's outcome — done, failed, in-flight?

Examples:
  kindship video status arcane-library-full-glory
  kindship video status arcane-library-full-glory --format json`,
	Args: cobra.ExactArgs(1),
	RunE: runVideoStatus,
}

func init() {
	videoStatusCmd.Flags().StringVar(&videoStatusFormat, "format", "text", "output format: text or json")
	videoCmd.AddCommand(videoStatusCmd)
}

func runVideoStatus(_ *cobra.Command, args []string) error {
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

	endpoint := fmt.Sprintf("%s/api/cli/videos/%s/status?agent_id=%s",
		ctx.APIBaseURL, url.PathEscape(slug), agentID)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch video status: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return fmt.Errorf("failed to fetch video status: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp api.VideoStatusResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("failed to fetch video status: %s", errResp.Error)
		}
		return fmt.Errorf("failed to fetch video status (%d): %s", resp.StatusCode, string(body))
	}

	var statusResp api.VideoStatusResponse
	if err := json.Unmarshal(body, &statusResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if videoStatusFormat == "json" {
		return printJSON(statusResp)
	}

	return printVideoStatusText(slug, &statusResp)
}

// printVideoStatusText writes the human-readable status block plus a
// next-action hint that maps directly to the actual CLI subcommand the
// agent should run next.
func printVideoStatusText(slug string, s *api.VideoStatusResponse) error {
	if s.Video == nil {
		fmt.Printf("Video %q: no record found.\n", slug)
		return nil
	}

	fmt.Printf("%s — %s\n", s.Video.Slug, s.Video.Title)
	fmt.Printf("  Video ID:    %s\n", s.Video.ID)

	if s.Revision == nil {
		fmt.Println("  Revision:    (none — publish failed mid-way; re-run 'kindship video publish')")
		return nil
	}

	r := s.Revision
	fmt.Printf("  Revision:    %s\n", r.ID)
	fmt.Printf("  Composition: %s (%dx%d @ %dfps, %d frames)\n",
		r.CompositionID, r.Width, r.Height, r.FPS, r.DurationInFrames)
	fmt.Printf("  Published:   %s\n", formatRelativeTime(r.PublishedAt))

	siteState := "not deployed"
	if r.HasSiteDeployed {
		siteState = "deployed"
	}
	fmt.Printf("  Site:        %s\n", siteState)

	mp4State := r.MP4RenderStatus
	if mp4State == "" {
		mp4State = "idle"
	}
	fmt.Printf("  MP4 status:  %s\n", mp4State)
	if r.MP4RenderCompletedAt != nil {
		fmt.Printf("  MP4 done at: %s\n", formatRelativeTime(*r.MP4RenderCompletedAt))
	}
	if r.MP4RenderError != nil && *r.MP4RenderError != "" {
		fmt.Printf("  MP4 error:   %s\n", *r.MP4RenderError)
	}

	fmt.Println()
	fmt.Printf("Next: %s\n", videoStatusHint(slug, r))
	return nil
}

// videoStatusHint maps the (site_deployed, mp4_status) cross-product to a
// concrete CLI invocation. Order matters: failed renders surface first
// so the agent doesn't think a stale 'rendering' is still in flight.
func videoStatusHint(slug string, r *api.VideoStatusRevision) string {
	switch r.MP4RenderStatus {
	case "failed":
		return fmt.Sprintf("last render failed — re-run with 'kindship video render %s --force'", slug)
	case "rendering", "pending":
		return fmt.Sprintf("render in flight — wait, then 'kindship video download %s' to fetch the MP4 (or re-run 'kindship video status %s' to check progress)", slug, slug)
	case "done":
		return fmt.Sprintf("MP4 cached — 'kindship video download %s' to fetch locally without re-rendering", slug)
	}
	if !r.HasSiteDeployed {
		return fmt.Sprintf("site not deployed — run 'kindship video render %s' to enable downloads (~$0.01)", slug)
	}
	return fmt.Sprintf("site deployed but no MP4 cached — 'kindship video render %s' to produce one", slug)
}
