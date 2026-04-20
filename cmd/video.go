package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
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
  publish   Upload a built Remotion composition
  list      List your videos
  status    Show per-video deep dive (revision, deploy, render state)
  render    Upload renderer site + invoke Lambda + save MP4 locally
  download  Fetch a previously-rendered MP4 from the cache
  review    Send the cached MP4 to Gemini 3.1 Pro for a scene-by-scene UI review
  review-frames  Render N stills locally + Gemini frame-level review (skips render)
  scenes    Detect scenes in src/Composition.tsx (used by review-frames)
  delete    Delete a video (not yet implemented)

See the kindship-video skill (~/.claude/skills/kindship-video/SKILL.md) for
the full workflow. TL;DR:

  cd /workspace/videos/<slug>
  npx esbuild src/Composition.tsx --bundle --format=esm --target=es2022 \
    --jsx=automatic --loader:.tsx=tsx --loader:.ts=ts --loader:.css=css \
    --external:react --external:react-dom \
    --external:remotion --external:'@remotion/*' \
    --outfile=composition.mjs
  npx remotion compositions ./src/index.ts --json > compositions.json
  npx remotion still <composition-id> poster.png --frame=<N> --scale=0.5  # optional
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
	Short: "Publish a built Remotion composition",
	Long: `Publish the current video composition. Expects:

  <dir>/composition.mjs    — single ESM module with default-exported component
                             (produced by esbuild — see SKILL.md)
  <dir>/compositions.json  — Remotion compositions manifest
                             ('npx remotion compositions ./src/index.ts --json > compositions.json')
  <dir>/public/            — optional, included if present (images, fonts; audio goes in music/ or narration/)
  <dir>/music/             — optional, signature audio sidecar (reusable across videos)
                             (referenced by compositions via new URL('./music/<slug>.mp3', import.meta.url).href);
                             see kindship-video SKILL step 4 for the <Audio> wiring
  <dir>/narration/         — optional, per-video voice-over WAV(s) from 'kindship voice …'
                             (referenced by compositions via new URL('./narration/<slug>.wav', import.meta.url).href);
                             unlike music, narration is rendered fresh per video, not curated
  <dir>/poster.png         — optional, used as the Videos tab thumbnail
                             (produced by 'npx remotion still <id> poster.png --frame=<N> --scale=0.5');
                             only the exact lowercase filename 'poster.png' at root is recognized

By default <dir> is /workspace/videos/<slug>/ (or the current directory if
you're standing in it). Override with --dir.

Note: composition.mjs MUST come from esbuild (the Kindship player loads it
directly via dynamic-import; webpack chunks are not browser-importable as
ESM). To make the video downloadable as MP4, see 'kindship video render',
which uploads the renderer bundle to AWS S3 separately from publish.

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
	Short: "List your videos",
	Long: `List the videos owned by the current agent.

Each row reflects the latest revision: whether the renderer site has
been deployed to S3 ('site') and whether an MP4 is cached and ready
to download ('mp4'). Use 'kindship video render <slug>' to populate
either, 'kindship video download <slug>' to grab a cached MP4.

Examples:
  kindship video list
  kindship video list --format json`,
	RunE: runVideoList,
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

	compositionMjs := filepath.Join(dir, "composition.mjs")
	compositionsFile := filepath.Join(dir, "compositions.json")
	publicDir := filepath.Join(dir, "public")

	if _, err := os.Stat(compositionMjs); err != nil {
		return fmt.Errorf(
			"%s not found; build it with esbuild before publishing (see kindship-video SKILL.md). Do NOT use 'npx remotion bundle' — its webpack output is not browser-importable as ESM",
			compositionMjs,
		)
	}
	if _, err := os.Stat(compositionsFile); err != nil {
		return fmt.Errorf(
			"%s not found; run 'npx remotion compositions ./src/index.ts --json > compositions.json' inside %s before publishing",
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

	// Pack composition.mjs + compositions.json (both at archive root) and
	// every file under <dir>/public/ (preserving its public/ prefix) into
	// an in-memory tar.gz. Pure Go tar.Writer — safe regardless of shell.
	archiveBuf, fileCount, err := createVideoArchive(compositionMjs, compositionsFile, publicDir)
	if err != nil {
		return err
	}
	if fileCount < 2 {
		return fmt.Errorf("expected at least composition.mjs + compositions.json in archive, got %d files", fileCount)
	}

	// Two-step upload: init mints a signed URL, we PUT the tarball
	// straight to Storage, then finalize triggers the server-side
	// publish. Old single-step multipart POST hit Vercel's 4.5MB
	// function body cap the moment anyone tried to ship a narration
	// WAV — this path has no such ceiling (staging bucket caps at
	// 200MB instead).
	archiveBytes := archiveBuf.Bytes()
	archiveSize := len(archiveBytes)

	initResp, err := postPublishInit(ctx, publishInitRequest{
		AgentID:      agentID,
		Slug:         slug,
		Title:        videoPublishTitle,
		Description:  videoPublishDescription,
		ArchiveSize:  archiveSize,
	})
	if err != nil {
		return fmt.Errorf("publish init failed: %w", err)
	}

	if err := putTarballToSignedURL(initResp.UploadURL, archiveBytes); err != nil {
		return fmt.Errorf("upload to signed URL failed: %w", err)
	}

	videoResp, err := postPublishFinalize(ctx, publishFinalizeRequest{
		AgentID:               agentID,
		VideoID:               initResp.VideoID,
		Slug:                  slug,
		StagingPath:           initResp.StagingPath,
		CompositionID:         videoPublishCompositionID,
		InputProps:            videoPublishInputProps,
	})
	if err != nil {
		return fmt.Errorf("publish finalize failed: %w", err)
	}
	if videoResp.Error != "" {
		return fmt.Errorf("publish failed: %s", videoResp.Error)
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
//   - composition.mjs at the archive root,
//   - compositions.json at the archive root,
//   - every regular file under publicDir (if publicDir exists), placed at
//     its <slug-dir>/-relative path (so public/fonts/foo.woff2 → "public/fonts/foo.woff2"),
//   - every regular file under <slug-dir>/music/ (if present), placed at
//     "music/<...>" — the signature-audio sidecar referenced by compositions
//     via `new URL('./music/<slug>.mp3', import.meta.url).href`.
//   - optional poster.png at the archive root (a sibling of composition.mjs),
//     used as the Videos tab thumbnail — only the exact lowercase filename
//     "poster.png" is recognized; missing poster is silent (not an error).
//
// The Lambda renderer bundle (webpack output of `npx remotion bundle`) is
// NOT packed here — it ships through the separate `kindship video render`
// flow that uploads files directly to S3 via presigned URLs. Keeps publish
// under Vercel's body limit and decouples player iteration from renderer-
// bundle deployment.
func createVideoArchive(compositionMjs, compositionsFile, publicDir string) (*bytes.Buffer, int, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	fileCount := 0

	// composition.mjs at root.
	mjsInfo, err := os.Stat(compositionMjs)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to stat composition.mjs: %w", err)
	}
	if err := writeTarEntry(tw, "composition.mjs", mjsInfo, compositionMjs); err != nil {
		return nil, 0, fmt.Errorf("failed to add composition.mjs to archive: %w", err)
	}
	fileCount++

	// compositions.json at root.
	compInfo, err := os.Stat(compositionsFile)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to stat compositions.json: %w", err)
	}
	if err := writeTarEntry(tw, "compositions.json", compInfo, compositionsFile); err != nil {
		return nil, 0, fmt.Errorf("failed to add compositions.json to archive: %w", err)
	}
	fileCount++

	// Optional: walk publicDir if present. The slug-dir parent is the base
	// for the relative path so paths come out as "public/<file>".
	if info, err := os.Stat(publicDir); err == nil && info.IsDir() {
		baseDir := filepath.Dir(publicDir) // <slug-dir>
		walkErr := filepath.Walk(publicDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || info.IsDir() || !info.Mode().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(baseDir, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel) // "public/<...>"
			if err := writeTarEntry(tw, rel, info, path); err != nil {
				return err
			}
			fileCount++
			return nil
		})
		if walkErr != nil {
			return nil, 0, fmt.Errorf("failed to walk public dir: %w", walkErr)
		}
	}

	// Optional: walk <slug-dir>/music/ — signature-audio sidecar. Kept
	// separate from public/ because compositions resolve audio via
	// `new URL('./music/<slug>.mp3', import.meta.url).href`, which
	// requires the file to be a sibling of composition.mjs (not nested
	// under public/). Mirrors the public/ walk's hygiene: skip symlinks
	// and non-regular files; silent on missing dir.
	musicDir := filepath.Join(filepath.Dir(compositionMjs), "music")
	if info, err := os.Stat(musicDir); err == nil && info.IsDir() {
		baseDir := filepath.Dir(musicDir) // <slug-dir>
		walkErr := filepath.Walk(musicDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || info.IsDir() || !info.Mode().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(baseDir, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel) // "music/<...>"
			if err := writeTarEntry(tw, rel, info, path); err != nil {
				return err
			}
			fileCount++
			return nil
		})
		if walkErr != nil {
			return nil, 0, fmt.Errorf("failed to walk music dir: %w", walkErr)
		}
	}

	// Optional: walk <slug-dir>/narration/ — per-video voice-over
	// WAVs. Structurally identical to music/ (sibling of composition.
	// mjs, resolved via `new URL('./narration/<slug>.wav', import.meta
	// .url).href`), but conceptually one-off: `kindship voice …` writes
	// straight here, no library semantics. Kept distinct from music/
	// so the two roles — reusable signature bed vs disposable per-video
	// narration — stay legible on disk and in the bundle.
	narrationDir := filepath.Join(filepath.Dir(compositionMjs), "narration")
	if info, err := os.Stat(narrationDir); err == nil && info.IsDir() {
		baseDir := filepath.Dir(narrationDir) // <slug-dir>
		walkErr := filepath.Walk(narrationDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || info.IsDir() || !info.Mode().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(baseDir, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel) // "narration/<...>"
			if err := writeTarEntry(tw, rel, info, path); err != nil {
				return err
			}
			fileCount++
			return nil
		})
		if walkErr != nil {
			return nil, 0, fmt.Errorf("failed to walk narration dir: %w", walkErr)
		}
	}

	// Optional: poster.png at the video-dir root (sibling of composition.mjs).
	// The server recognizes exactly this filename, lowercase; JPG/WebP here
	// are ignored even if present. Missing poster is silent — publish is
	// expected to succeed without one, the UI just falls back to a Film icon.
	posterPath := filepath.Join(filepath.Dir(compositionMjs), "poster.png")
	if info, statErr := os.Stat(posterPath); statErr == nil && info.Mode().IsRegular() {
		if err := writeTarEntry(tw, "poster.png", info, posterPath); err != nil {
			return nil, 0, fmt.Errorf("failed to add poster.png to archive: %w", err)
		}
		fileCount++
	}

	if err := tw.Close(); err != nil {
		return nil, 0, fmt.Errorf("failed to close tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, 0, fmt.Errorf("failed to close gzip: %w", err)
	}

	return &buf, fileCount, nil
}

// runVideoList calls GET /api/cli/videos/list and renders a tabular
// summary. The server returns one entry per video already filtered to
// rows with a current_revision_id, so the CLI doesn't need to second-
// guess which rows to show.
func runVideoList(_ *cobra.Command, _ []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/api/cli/videos/list?agent_id=%s", ctx.APIBaseURL, agentID)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to list videos: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return fmt.Errorf("failed to list videos: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp api.VideoListResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("failed to list videos: %s", errResp.Error)
		}
		return fmt.Errorf("failed to list videos (%d): %s", resp.StatusCode, string(body))
	}

	var listResp api.VideoListResponse
	if err := json.Unmarshal(body, &listResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if videoFormat == "json" {
		return printJSON(listResp)
	}

	if len(listResp.Videos) == 0 {
		fmt.Println("No videos found. Publish one with 'kindship video publish <slug> --title \"...\"'.")
		return nil
	}

	fmt.Printf("%-32s %-8s %-10s %s\n", "SLUG", "SITE", "MP4", "UPDATED")
	for _, v := range listResp.Videos {
		site := "no"
		if v.HasMP4Render {
			site = "yes"
		}
		fmt.Printf("%-32s %-8s %-10s %s\n", v.Slug, site, v.MP4Status, formatRelativeTime(v.UpdatedAt))
	}
	return nil
}

// ---- Two-step publish helpers ------------------------------------
//
// The publish flow used to POST a multipart tarball directly to
// /api/cli/videos/publish, but Vercel caps serverless function
// bodies at 4.5MB and the first narrated video blew through that on
// its narration WAV. New flow: CLI asks the server for a signed
// upload URL, PUTs the tarball straight into Supabase Storage, then
// calls finalize to kick off the existing server-side publish
// pipeline against the already-staged path.

type publishInitRequest struct {
	AgentID     string `json:"agent_id,omitempty"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	ArchiveSize int    `json:"archive_size"`
}

type publishInitResponse struct {
	VideoID     string `json:"video_id"`
	StagingPath string `json:"staging_path"`
	UploadURL   string `json:"upload_url"`
	UploadToken string `json:"upload_token,omitempty"`
	Error       string `json:"error,omitempty"`
}

type publishFinalizeRequest struct {
	AgentID       string `json:"agent_id,omitempty"`
	VideoID       string `json:"video_id"`
	Slug          string `json:"slug"`
	StagingPath   string `json:"staging_path"`
	CompositionID string `json:"composition_id,omitempty"`
	// Kept as a raw string so we match the server's JSON-transform
	// zod shape (it parses the string itself; we pass through).
	InputProps string `json:"input_props,omitempty"`
}

// postPublishInit asks the server for a signed upload URL that the
// CLI can PUT the tarball against.
func postPublishInit(
	ctx *auth.Context, req publishInitRequest,
) (*publishInitResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal init request: %w", err)
	}
	httpReq, err := http.NewRequest(
		http.MethodPost,
		ctx.APIBaseURL+"/api/cli/videos/publish/init",
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("build init request: %w", err)
	}
	ctx.SetAuthHeaders(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("POST init: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read init response: %w", err)
	}
	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return nil, err
	}
	var out publishInitResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse init response: %w: %s", err, firstBytes(body, 300))
	}
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" {
			return nil, fmt.Errorf("init %d: %s", resp.StatusCode, out.Error)
		}
		return nil, fmt.Errorf("init %d: %s", resp.StatusCode, firstBytes(body, 300))
	}
	if out.UploadURL == "" || out.StagingPath == "" || out.VideoID == "" {
		return nil, fmt.Errorf("init response missing required fields: %s", firstBytes(body, 300))
	}
	return &out, nil
}

// putTarballToSignedURL PUTs the tarball body to the presigned URL
// the init endpoint returned. Supabase signed upload URLs accept
// the body directly with Content-Type signalling; no extra headers
// needed.
//
// Keeps a generous timeout: staging bucket caps at 200MB, uploads
// over common container egress (~25 Mbit/s) take ~65 seconds at
// that ceiling. 10 minutes is pathological headroom so even a
// throttled network won't time out before the upload completes.
func putTarballToSignedURL(uploadURL string, body []byte) error {
	req, err := http.NewRequest(http.MethodPut, uploadURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build PUT: %w", err)
	}
	req.Header.Set("Content-Type", "application/gzip")
	req.ContentLength = int64(len(body))

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("PUT: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PUT %d: %s", resp.StatusCode, firstBytes(raw, 300))
	}
	return nil
}

// postPublishFinalize tells the server the tarball is staged and
// ready to be extracted + processed.
func postPublishFinalize(
	ctx *auth.Context, req publishFinalizeRequest,
) (*api.VideoPublishResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal finalize request: %w", err)
	}
	httpReq, err := http.NewRequest(
		http.MethodPost,
		ctx.APIBaseURL+"/api/cli/videos/publish/finalize",
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("build finalize request: %w", err)
	}
	ctx.SetAuthHeaders(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("X-Kindship-CLI-Version", Version)

	// Finalize waits on the Hatchet workflow; the server budgets ~4.5
	// minutes for it internally before timing out. Our client timeout
	// is a touch longer so we see the server's 424 instead of a
	// connection reset.
	client := &http.Client{Timeout: 6 * time.Minute}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("POST finalize: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read finalize response: %w", err)
	}
	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return nil, err
	}
	var out api.VideoPublishResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse finalize response: %w: %s", err, firstBytes(body, 300))
	}
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" {
			return nil, fmt.Errorf("finalize %d: %s", resp.StatusCode, out.Error)
		}
		return nil, fmt.Errorf("finalize %d: %s", resp.StatusCode, firstBytes(body, 300))
	}
	return &out, nil
}

func firstBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
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
