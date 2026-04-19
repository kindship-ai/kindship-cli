package cmd

import (
	"fmt"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/auth"

	"github.com/spf13/cobra"
)

var (
	videoDownloadOutput string
	videoDownloadFormat string
)

// videoDownloadResult is the JSON shape printed when --format json.
type videoDownloadResult struct {
	Slug        string  `json:"slug"`
	OutputPath  string  `json:"output_path"`
	OutputBytes int64   `json:"output_bytes"`
	DurationSec float64 `json:"duration_seconds"`
}

var videoDownloadCmd = &cobra.Command{
	Use:   "download <slug>",
	Short: "Fetch a previously-rendered MP4 from the cache",
	Long: `Stream the cached MP4 for a video to a local file. No Lambda
invocation, no S3 writes — pure read.

Errors clearly when no MP4 has been rendered yet, pointing at
'kindship video render <slug>' (which costs ~$0.01 per fresh render).

Examples:
  kindship video download arcane-library-full-glory
  kindship video download arcane-library-full-glory --output ./demo.mp4`,
	Args: cobra.ExactArgs(1),
	RunE: runVideoDownload,
}

func init() {
	videoDownloadCmd.Flags().StringVar(&videoDownloadOutput, "output", "", "MP4 output path (default ./<slug>.mp4)")
	videoDownloadCmd.Flags().StringVar(&videoDownloadFormat, "format", "text", "output format: text or json")
	videoCmd.AddCommand(videoDownloadCmd)
}

func runVideoDownload(_ *cobra.Command, args []string) error {
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

	// Pre-flight: check status so we can give a clear "no cache, run render"
	// message instead of letting the stream endpoint return a generic 422.
	status, err := fetchVideoStatus(ctx, agentID, slug)
	if err != nil {
		return err
	}
	if status.Revision == nil {
		return fmt.Errorf("no published revision for %q — run 'kindship video publish %s --title \"...\"' first", slug, slug)
	}
	if status.Revision.MP4RenderStatus != "done" {
		return fmt.Errorf("no cached MP4 for %q (status=%s) — run 'kindship video render %s' to produce one (~$0.01)", slug, status.Revision.MP4RenderStatus, slug)
	}

	outputPath := videoDownloadOutput
	if outputPath == "" {
		outputPath = fmt.Sprintf("./%s.mp4", slug)
	}

	startedAt := time.Now()
	bytes, err := downloadMP4(ctx, agentID, slug, outputPath)
	if err != nil {
		return err
	}
	duration := time.Since(startedAt).Seconds()

	if videoDownloadFormat == "json" {
		return printJSON(videoDownloadResult{
			Slug:        slug,
			OutputPath:  outputPath,
			OutputBytes: bytes,
			DurationSec: duration,
		})
	}
	fmt.Printf("MP4 saved to %s (%s, %.1fs).\n", outputPath, formatBytes(bytes), duration)
	return nil
}
