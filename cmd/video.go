package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"

	"github.com/spf13/cobra"
)

var videoCmd = &cobra.Command{
	Use:   "video",
	Short: "Video creation commands",
	Long: `Commands for publishing Remotion-based videos.

Subcommands:
  publish  Upload a built Remotion bundle
  list     List your videos (not yet implemented)
  delete   Delete a video (not yet implemented)

See the kindship-video skill (~/.claude/skills/kindship-video/SKILL.md) for
the full workflow. TL;DR:

  cd /workspace/videos/<slug>
  npx remotion bundle
  npx remotion compositions ./build --json > compositions.json
  kindship video publish <slug> --title "..."`,
}

var (
	videoPublishDir           string
	videoPublishTitle         string
	videoPublishDescription   string
	videoPublishCompositionID string
	videoPublishInputProps    string
	videoFormat               string
)

var videoPublishCmd = &cobra.Command{
	Use:   "publish <slug>",
	Short: "Publish a built Remotion bundle",
	Long: `Publish the current video bundle. Expects <dir>/build/ and
<dir>/compositions.json to exist (produced by 'npx remotion bundle' and
'npx remotion compositions ./build --json > compositions.json').

By default <dir> is /workspace/videos/<slug>/ (or the current directory if
you're standing in it). Override with --dir.

Examples:
  kindship video publish milestone-1-retro --title "Milestone 1 retro"
  kindship video publish tribe-intro --title "Meet the Tribe" --description "..."
  kindship video publish complex --title "..." --composition-id intro
  kindship video publish parametric --title "..." --input-props '{"name":"Ada"}'`,
	Args: cobra.ExactArgs(1),
	RunE: runVideoPublish,
}

var videoListCmd = &cobra.Command{
	Use:   "list",
	Short: "List your videos (coming soon)",
	RunE: func(_ *cobra.Command, _ []string) error {
		return fmt.Errorf("video list is not yet implemented; check the Videos tab in the web app")
	},
}

var videoDeleteCmd = &cobra.Command{
	Use:   "delete <slug>",
	Short: "Delete a video (coming soon)",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, _ []string) error {
		return fmt.Errorf("video delete is not yet implemented; delete the row in the DB if you need to")
	},
}

func init() {
	videoPublishCmd.Flags().StringVar(&videoPublishDir, "dir", "", "override the video workspace dir (default /workspace/videos/<slug>/)")
	videoPublishCmd.Flags().StringVar(&videoPublishTitle, "title", "", "human-readable video title (required)")
	videoPublishCmd.Flags().StringVar(&videoPublishDescription, "description", "", "optional one-sentence description")
	videoPublishCmd.Flags().StringVar(&videoPublishCompositionID, "composition-id", "", "explicit composition to publish (required when the bundle has >1 composition)")
	videoPublishCmd.Flags().StringVar(&videoPublishInputProps, "input-props", "", "default input props as JSON (object); falls back to composition.defaultProps")
	videoPublishCmd.Flags().StringVar(&videoFormat, "format", "text", "output format: text or json")
	_ = videoPublishCmd.MarkFlagRequired("title")

	videoListCmd.Flags().StringVar(&videoFormat, "format", "text", "output format: text or json")

	videoCmd.AddCommand(videoPublishCmd)
	videoCmd.AddCommand(videoListCmd)
	videoCmd.AddCommand(videoDeleteCmd)
	rootCmd.AddCommand(videoCmd)
}

func runVideoPublish(_ *cobra.Command, args []string) error {
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

	dir := resolveVideoDir(slug, videoPublishDir)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("video workspace not found at %s; scaffold with 'npx create-video@latest --yes --blank --no-tailwind .' first, or pass --dir", dir)
		}
		return fmt.Errorf("failed to access %s: %w", dir, err)
	}

	buildDir := filepath.Join(dir, "build")
	compositionsFile := filepath.Join(dir, "compositions.json")

	if info, err := os.Stat(buildDir); err != nil || !info.IsDir() {
		return fmt.Errorf(
			"%s not found or not a directory; run 'npx remotion bundle' inside %s before publishing",
			buildDir, dir,
		)
	}
	if _, err := os.Stat(compositionsFile); err != nil {
		return fmt.Errorf(
			"%s not found; run 'npx remotion compositions ./build --json > compositions.json' inside %s before publishing",
			compositionsFile, dir,
		)
	}

	// Validate --input-props up front (so we fail fast instead of after upload).
	if videoPublishInputProps != "" {
		var probe map[string]any
		if err := json.Unmarshal([]byte(videoPublishInputProps), &probe); err != nil {
			return fmt.Errorf("--input-props must be a JSON object: %w", err)
		}
	}

	// Pack build/ contents (at archive root) + compositions.json (at archive root)
	// into an in-memory tar.gz. No shell heredocs; pure Go tar.Writer so the
	// packaging is safe regardless of shell environment.
	archiveBuf, fileCount, err := createVideoArchive(buildDir, compositionsFile)
	if err != nil {
		return err
	}
	if fileCount == 0 {
		return fmt.Errorf("build/ is empty at %s; did 'npx remotion bundle' succeed?", buildDir)
	}

	// Build multipart request
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)

	_ = writer.WriteField("slug", slug)
	_ = writer.WriteField("title", videoPublishTitle)
	_ = writer.WriteField("agent_id", agentID)
	if videoPublishDescription != "" {
		_ = writer.WriteField("description", videoPublishDescription)
	}
	if videoPublishCompositionID != "" {
		_ = writer.WriteField("composition_id", videoPublishCompositionID)
	}
	if videoPublishInputProps != "" {
		_ = writer.WriteField("input_props", videoPublishInputProps)
	}

	part, err := writer.CreateFormFile("archive", "bundle.tar.gz")
	if err != nil {
		return fmt.Errorf("failed to create form file: %w", err)
	}
	if _, err := io.Copy(part, archiveBuf); err != nil {
		return fmt.Errorf("failed to write archive to form: %w", err)
	}
	writer.Close()

	endpoint := fmt.Sprintf("%s/api/cli/videos/publish", ctx.APIBaseURL)
	req, err := http.NewRequest(http.MethodPost, endpoint, &requestBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to publish video: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return fmt.Errorf("publish failed: %w", err)
	}

	var videoResp api.VideoPublishResponse
	if err := json.Unmarshal(body, &videoResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if videoResp.Error != "" {
			return fmt.Errorf("publish failed: %s", videoResp.Error)
		}
		return fmt.Errorf("publish failed (%d): %s", resp.StatusCode, string(body))
	}

	if videoFormat == "json" {
		return printJSON(videoResp)
	}

	fmt.Printf("Published %s (%d files) — %s\n", slug, fileCount, videoPublishTitle)
	fmt.Printf("  Video:       %s\n", videoResp.VideoID)
	fmt.Printf("  Revision:    %s\n", videoResp.RevisionID)
	fmt.Printf("  Composition: %s (%dx%d @ %dfps, %d frames)\n",
		videoResp.CompositionID, videoResp.Width, videoResp.Height,
		videoResp.FPS, videoResp.DurationInFrames)
	if videoResp.BundleURL != "" {
		fmt.Printf("  Bundle URL:  %s%s\n", ctx.APIBaseURL, videoResp.BundleURL)
	}
	return nil
}

func resolveVideoDir(slug, override string) string {
	if override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return override
		}
		return abs
	}

	// Default: /workspace/videos/<slug>/ matches the convention agents follow
	// per the kindship-video skill. Fall back to cwd if that doesn't exist
	// (dev ergonomics — running `kindship video publish foo` from inside the
	// video directory should Just Work).
	defaultDir := filepath.Join("/workspace", "videos", slug)
	if info, err := os.Stat(defaultDir); err == nil && info.IsDir() {
		return defaultDir
	}

	cwd, err := os.Getwd()
	if err == nil {
		return cwd
	}
	return defaultDir
}

func validateSlug(slug string) error {
	if len(slug) < 2 || len(slug) > 63 {
		return fmt.Errorf("slug must be 2-63 characters")
	}
	for i, r := range slug {
		isLower := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		isDash := r == '-'
		if !isLower && !isDigit && !isDash {
			return fmt.Errorf("slug must contain only lowercase letters, digits, and dashes (got %q at position %d)", r, i)
		}
	}
	if strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") {
		return fmt.Errorf("slug must not start or end with a dash")
	}
	return nil
}

// createVideoArchive streams a gzip-compressed tar containing:
//   - every regular file under buildDir, placed at its buildDir-relative path
//     (e.g. buildDir/index.mjs → "index.mjs"),
//   - compositionsFile, placed at the archive root as "compositions.json".
//
// The server-side extractor recognizes "compositions.json" at root; every
// other file goes into the published bundle.
func createVideoArchive(buildDir, compositionsFile string) (*bytes.Buffer, int, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	fileCount := 0

	err := filepath.Walk(buildDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip symlinks, directories (tar creates them implicitly), and
		// any non-regular files. Mirrors the site-push policy.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		rel, err := filepath.Rel(buildDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// tar header paths use forward slashes on every platform.
		rel = filepath.ToSlash(rel)

		if err := writeTarEntry(tw, rel, info, path); err != nil {
			return err
		}
		fileCount++
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to walk build dir: %w", err)
	}

	// Sibling compositions.json at archive root.
	info, err := os.Stat(compositionsFile)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to stat compositions.json: %w", err)
	}
	if err := writeTarEntry(tw, "compositions.json", info, compositionsFile); err != nil {
		return nil, 0, fmt.Errorf("failed to add compositions.json to archive: %w", err)
	}
	fileCount++

	if err := tw.Close(); err != nil {
		return nil, 0, fmt.Errorf("failed to close tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, 0, fmt.Errorf("failed to close gzip: %w", err)
	}

	return &buf, fileCount, nil
}

func writeTarEntry(tw *tar.Writer, archivePath string, info os.FileInfo, sourcePath string) error {
	hdr := &tar.Header{
		Name:    archivePath,
		Mode:    int64(info.Mode().Perm()),
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("failed to write tar header for %s: %w", archivePath, err)
	}

	f, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", sourcePath, err)
	}
	defer f.Close()
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("failed to copy %s into tar: %w", sourcePath, err)
	}
	return nil
}
