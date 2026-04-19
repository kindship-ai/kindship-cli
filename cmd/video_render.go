package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"

	"github.com/spf13/cobra"
)

var (
	videoRenderDir         string
	videoRenderOutput      string
	videoRenderPrepareOnly bool
	videoRenderForce       bool
	videoRenderAsync       bool
	videoRenderFormat      string
)

// uploadConcurrency caps simultaneous S3 PUTs from the CLI. Webpack
// site/ bundles average ~50 files, so a small pool stays gentle on
// container egress and presigned-URL lifetimes (10 min) while still
// completing in seconds.
const uploadConcurrency = 8

// isJSONFormat is the gate for human-readable output — when --format json
// is set, the only thing that can hit stdout is the final JSON payload
// (otherwise the output isn't machine-parseable).
func isJSONFormat() bool { return videoRenderFormat == "json" }

// videoRenderResult is the JSON shape printed when --format json. Mirrors
// the text output's facts so consumers can pipe one shape regardless of
// where they sit in the flow (prepare-only, async, full).
type videoRenderResult struct {
	Slug         string  `json:"slug"`
	RevisionID   string  `json:"revision_id"`
	SiteUploaded bool    `json:"site_uploaded"`
	FilesUpload  int     `json:"files_uploaded,omitempty"`
	BytesUpload  int64   `json:"bytes_uploaded,omitempty"`
	RenderID     string  `json:"render_id,omitempty"`
	MP4Status    string  `json:"mp4_status"`
	OutputPath   string  `json:"output_path,omitempty"`
	OutputBytes  int64   `json:"output_bytes,omitempty"`
	DurationSec  float64 `json:"duration_seconds,omitempty"`
	Action       string  `json:"action"`
}

var videoRenderCmd = &cobra.Command{
	Use:   "render <slug>",
	Short: "Upload renderer site + invoke Lambda + save MP4 locally",
	Long: `Render the latest revision of a video to MP4 via Remotion Lambda.

Pipeline (default flags):
  1. Read latest revision (errors if you haven't 'kindship video publish'd).
  2. If site not deployed (or --force), build '<dir>/site/' with
     'npx remotion bundle ./src/index.ts --out-dir <dir>/site/' and
     upload directly to S3 via presigned PUTs (8-way parallel).
  3. POST to the render dispatcher — Lambda spins up, runs the render.
  4. Poll until done, then stream the MP4 to '<output>'.

Cost: ~$0.01 per fresh Lambda render. Re-running on the same revision
is FREE when the MP4 is already cached (CAS guard short-circuits at
step 3 with a 'done' response). Use --force to re-upload site + force a
fresh render.

Examples:
  kindship video render arcane-library-full-glory
  kindship video render arcane-library-full-glory --output ./demo.mp4
  kindship video render arcane-library-full-glory --prepare-only   # site only
  kindship video render arcane-library-full-glory --force           # rebuild site
  kindship video render arcane-library-full-glory --async           # don't wait`,
	Args: cobra.ExactArgs(1),
	RunE: runVideoRender,
}

func init() {
	videoRenderCmd.Flags().StringVar(&videoRenderDir, "dir", "", "video workspace dir (default /workspace/videos/<slug>/)")
	videoRenderCmd.Flags().StringVar(&videoRenderOutput, "output", "", "MP4 output path (default ./<slug>.mp4)")
	videoRenderCmd.Flags().BoolVar(&videoRenderPrepareOnly, "prepare-only", false, "upload site + skip Lambda render")
	videoRenderCmd.Flags().BoolVar(&videoRenderForce, "force", false, "re-upload site even if already deployed; force fresh render")
	videoRenderCmd.Flags().BoolVar(&videoRenderAsync, "async", false, "don't wait for Lambda; print render id and return")
	videoRenderCmd.Flags().StringVar(&videoRenderFormat, "format", "text", "output format: text or json")
	videoCmd.AddCommand(videoRenderCmd)
}

func runVideoRender(_ *cobra.Command, args []string) error {
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

	startedAt := time.Now()

	// 1. Status — figures out whether we need to upload site, whether
	//    MP4 is already cached, and gives us the revision id for logging.
	status, err := fetchVideoStatus(ctx, agentID, slug)
	if err != nil {
		return err
	}
	if status.Revision == nil {
		return fmt.Errorf("no published revision for %q — run 'kindship video publish %s --title \"...\"' first", slug, slug)
	}
	revisionID := status.Revision.ID
	dir := resolveVideoDir(slug, videoRenderDir)

	result := videoRenderResult{
		Slug:       slug,
		RevisionID: revisionID,
		MP4Status:  status.Revision.MP4RenderStatus,
	}

	needsSite := !status.Revision.HasSiteDeployed || videoRenderForce
	if needsSite {
		uploaded, bytes, err := buildAndUploadSite(ctx, agentID, slug, dir, status.Video.ID, revisionID)
		if err != nil {
			return err
		}
		result.SiteUploaded = true
		result.FilesUpload = uploaded
		result.BytesUpload = bytes
		if !isJSONFormat() {
			fmt.Printf("Site uploaded: %d files, %s\n", uploaded, formatBytes(bytes))
		}
	} else if !isJSONFormat() {
		fmt.Println("Site already deployed — skipping upload (use --force to re-upload).")
	}

	if videoRenderPrepareOnly {
		result.Action = "prepared"
		result.DurationSec = time.Since(startedAt).Seconds()
		return printRenderResult(result, "Site ready. Run without --prepare-only to render the MP4.")
	}

	// Refresh status — site finalize may have flipped has_site_deployed.
	if needsSite {
		refreshed, err := fetchVideoStatus(ctx, agentID, slug)
		if err == nil && refreshed.Revision != nil {
			status = refreshed
		}
	}

	// 2. Dispatch render. The CAS guard on the server short-circuits to
	//    'done' if the MP4 was already cached (and --force didn't reset
	//    the row, which it doesn't — finalize CAS only writes when
	//    lambda_site_bucket is null, but render dispatch resets the MP4
	//    fields on every claim).
	dispatch, err := dispatchRender(ctx, agentID, slug, videoRenderForce)
	if err != nil {
		return err
	}
	result.RenderID = dispatch.RenderID
	result.MP4Status = dispatch.Status

	if dispatch.Ready {
		if !isJSONFormat() {
			fmt.Println("MP4 already cached — skipping Lambda invocation.")
		}
	} else if videoRenderAsync {
		result.Action = "dispatched"
		result.DurationSec = time.Since(startedAt).Seconds()
		return printRenderResult(result, fmt.Sprintf("Render dispatched (id=%s). Poll with 'kindship video status %s'.", dispatch.RenderID, slug))
	} else {
		if !isJSONFormat() {
			fmt.Printf("Rendering on Lambda (id=%s) — polling every 2s...\n", dispatch.RenderID)
		}
		final, err := pollUntilDone(ctx, agentID, slug)
		if err != nil {
			return err
		}
		result.MP4Status = final.Status
		dispatch = final
	}

	if !dispatch.Ready {
		return fmt.Errorf("render did not complete (status=%s)", dispatch.Status)
	}

	// 3. Download MP4.
	outputPath := videoRenderOutput
	if outputPath == "" {
		outputPath = fmt.Sprintf("./%s.mp4", slug)
	}
	bytes, err := downloadMP4(ctx, agentID, slug, outputPath)
	if err != nil {
		return err
	}
	result.OutputPath = outputPath
	result.OutputBytes = bytes
	result.Action = "rendered"
	result.DurationSec = time.Since(startedAt).Seconds()
	return printRenderResult(result, fmt.Sprintf("MP4 saved to %s (%s, %.1fs).", outputPath, formatBytes(bytes), result.DurationSec))
}

// fetchVideoStatus is the shared status-fetcher for render + download.
// It uses the same /status endpoint the `kindship video status` command
// hits so the data shapes stay aligned.
func fetchVideoStatus(ctx *auth.Context, agentID, slug string) (*api.VideoStatusResponse, error) {
	endpoint := fmt.Sprintf("%s/api/cli/videos/%s/status?agent_id=%s",
		ctx.APIBaseURL, url.PathEscape(slug), agentID)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create status request: %w", err)
	}
	ctx.SetAuthHeaders(req)
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch status: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read status response: %w", err)
	}
	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return nil, fmt.Errorf("status fetch failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var errResp api.VideoStatusResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("status fetch failed: %s", errResp.Error)
		}
		return nil, fmt.Errorf("status fetch failed (%d): %s", resp.StatusCode, string(body))
	}

	var statusResp api.VideoStatusResponse
	if err := json.Unmarshal(body, &statusResp); err != nil {
		return nil, fmt.Errorf("failed to parse status response: %w", err)
	}
	return &statusResp, nil
}

// buildAndUploadSite shells out to `npx remotion bundle` if site/ is
// missing or stale relative to composition.mjs, then walks the bundle,
// requests presigned PUTs, uploads in parallel, and finalizes.
//
// Returns (file_count, total_bytes).
func buildAndUploadSite(ctx *auth.Context, agentID, slug, dir, videoID, revisionID string) (int, int64, error) {
	siteDir := filepath.Join(dir, "site")
	compositionMjs := filepath.Join(dir, "composition.mjs")

	// --force always re-bundles. Otherwise rebundle only if site/ is
	// missing or older than composition.mjs. Re-bundling on --force
	// matters because the CLI may have rolled forward to a Remotion
	// version with a different bundle layout (e.g. publicPath fix) —
	// agents typing --force expect a clean rebuild, not just re-upload.
	if videoRenderForce || needsBundle(siteDir, compositionMjs) {
		if !isJSONFormat() {
			fmt.Println("Bundling renderer with `npx remotion bundle`...")
		}
		if err := runRemotionBundle(dir, siteDir); err != nil {
			return 0, 0, err
		}
	} else if !isJSONFormat() {
		fmt.Println("Reusing existing site/ bundle (newer than composition.mjs).")
	}

	files, totalBytes, err := walkSiteBundle(siteDir)
	if err != nil {
		return 0, 0, err
	}
	if len(files) == 0 {
		return 0, 0, fmt.Errorf("site/ bundle is empty at %s", siteDir)
	}

	initResp, err := requestPresignedUploads(ctx, agentID, videoID, revisionID, files)
	if err != nil {
		return 0, 0, err
	}
	if !isJSONFormat() {
		fmt.Printf("Got %d presigned URLs (bucket=%s, key_prefix=%s)\n",
			len(initResp.Uploads), initResp.Bucket, initResp.KeyPrefix)
	}

	if err := putFilesToS3(siteDir, initResp.Uploads); err != nil {
		return 0, 0, err
	}

	expectedPaths := make([]string, len(files))
	for i, f := range files {
		expectedPaths[i] = f.Path
	}
	if err := finalizeSiteUpload(ctx, agentID, videoID, revisionID, expectedPaths); err != nil {
		return 0, 0, err
	}
	return len(files), totalBytes, nil
}

// needsBundle returns true when site/ is missing OR composition.mjs is
// newer (= the visual code changed and the bundle is stale). This is the
// cheap heuristic — agents who want a forced rebuild use --force which
// the caller checks separately.
func needsBundle(siteDir, compositionMjs string) bool {
	siteInfo, err := os.Stat(filepath.Join(siteDir, "index.html"))
	if err != nil {
		return true
	}
	mjsInfo, err := os.Stat(compositionMjs)
	if err != nil {
		// Can't compare — fall back to assuming the bundle is fresh,
		// since the upload-time index.html validation will catch a truly
		// broken site.
		return false
	}
	return mjsInfo.ModTime().After(siteInfo.ModTime())
}

// runRemotionBundle invokes the Remotion CLI to produce a webpack
// bundle. Streams output so the agent sees progress; falls back to
// captured stderr in the error message when the command fails.
//
// --public-path="./" makes index.html load bundle.js relative to its
// own URL — required because each revision's site lives at a unique
// S3 key prefix (sites-<env>/<rev>/) and an absolute "/bundle.js"
// would resolve to the bucket root and 404. Requires Remotion >= 4.0.127.
func runRemotionBundle(dir, siteDir string) error {
	cmd := exec.Command("npx", "remotion", "bundle", "./src/index.ts", "--out-dir", siteDir, "--public-path=./")
	cmd.Dir = dir
	if isJSONFormat() {
		// JSON callers want a parseable result on stdout — silence the bundler.
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npx remotion bundle failed in %s: %w (run it manually to see the full error)", dir, err)
	}
	return nil
}

// siteFile is one entry in the upload list — held in memory only as a
// path/size/content-type tuple, never as the actual bytes.
type siteFile struct {
	Path        string
	Size        int64
	ContentType string
}

// walkSiteBundle recursively lists regular files under siteDir, returning
// paths relative to siteDir with content-types inferred from extension.
// Skips symlinks and non-regular entries; bubbles up any walk error.
func walkSiteBundle(siteDir string) ([]siteFile, int64, error) {
	var files []siteFile
	var total int64
	err := filepath.Walk(siteDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(siteDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		ct := mime.TypeByExtension(filepath.Ext(rel))
		if ct == "" {
			ct = "application/octet-stream"
		}
		files = append(files, siteFile{Path: rel, Size: info.Size(), ContentType: ct})
		total += info.Size()
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to walk %s: %w", siteDir, err)
	}
	return files, total, nil
}

// requestPresignedUploads POSTs the file list to /api/cli/videos/site/init
// and returns the presigned PUT slots. Honors --force via ?force=true.
func requestPresignedUploads(
	ctx *auth.Context,
	agentID, videoID, revisionID string,
	files []siteFile,
) (*api.SiteInitResponse, error) {
	apiFiles := make([]api.SiteUploadFile, len(files))
	for i, f := range files {
		apiFiles[i] = api.SiteUploadFile{
			Path:        f.Path,
			Size:        f.Size,
			ContentType: f.ContentType,
		}
	}
	reqBody := api.SiteInitRequest{
		AgentID:    agentID,
		VideoID:    videoID,
		RevisionID: revisionID,
		Files:      apiFiles,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal init request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/cli/videos/site/init", ctx.APIBaseURL)
	if videoRenderForce {
		endpoint += "?force=true"
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create init request: %w", err)
	}
	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("site init request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read init response: %w", err)
	}
	if err := handleNonJSONResponse(resp.StatusCode, respBody); err != nil {
		return nil, fmt.Errorf("site init failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var errResp api.SiteInitResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("site init failed: %s", errResp.Error)
		}
		return nil, fmt.Errorf("site init failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var initResp api.SiteInitResponse
	if err := json.Unmarshal(respBody, &initResp); err != nil {
		return nil, fmt.Errorf("failed to parse init response: %w", err)
	}
	return &initResp, nil
}

// putFilesToS3 uploads the file body for each presigned slot. Bounded
// concurrency keeps egress + memory predictable; first error from any
// goroutine cancels via the shared error channel + waitgroup.
func putFilesToS3(siteDir string, uploads []api.SitePresignedUpload) error {
	if len(uploads) == 0 {
		return fmt.Errorf("no uploads to perform")
	}

	sem := make(chan struct{}, uploadConcurrency)
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	setErr := func(err error) {
		errMu.Lock()
		defer errMu.Unlock()
		if firstErr == nil {
			firstErr = err
		}
	}

	httpClient := &http.Client{Timeout: 120 * time.Second}

	for i := range uploads {
		u := uploads[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			localPath := filepath.Join(siteDir, filepath.FromSlash(u.Path))
			f, err := os.Open(localPath)
			if err != nil {
				setErr(fmt.Errorf("failed to open %s: %w", u.Path, err))
				return
			}
			defer f.Close()

			info, err := f.Stat()
			if err != nil {
				setErr(fmt.Errorf("failed to stat %s: %w", u.Path, err))
				return
			}

			req, err := http.NewRequest(http.MethodPut, u.URL, f)
			if err != nil {
				setErr(fmt.Errorf("failed to create PUT for %s: %w", u.Path, err))
				return
			}
			req.ContentLength = info.Size()
			for k, v := range u.Headers {
				req.Header.Set(k, v)
			}

			resp, err := httpClient.Do(req)
			if err != nil {
				setErr(fmt.Errorf("PUT %s failed: %w", u.Path, err))
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				body, _ := io.ReadAll(resp.Body)
				setErr(fmt.Errorf("PUT %s returned %d: %s", u.Path, resp.StatusCode, string(body)))
				return
			}
		}()
	}
	wg.Wait()
	return firstErr
}

// finalizeSiteUpload tells the server to HEAD-verify the uploaded paths
// and flip lambda_site_bucket on the revision row. Surfaces missing-file
// lists so the caller can report which files to retry (we don't auto-
// retry here because partial uploads are surprising enough to deserve
// a fresh agent decision).
func finalizeSiteUpload(
	ctx *auth.Context,
	agentID, videoID, revisionID string,
	expectedFiles []string,
) error {
	body := api.SiteFinalizeRequest{
		AgentID:       agentID,
		VideoID:       videoID,
		RevisionID:    revisionID,
		ExpectedFiles: expectedFiles,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal finalize request: %w", err)
	}
	endpoint := fmt.Sprintf("%s/api/cli/videos/site/finalize", ctx.APIBaseURL)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create finalize request: %w", err)
	}
	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("finalize request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read finalize response: %w", err)
	}
	if err := handleNonJSONResponse(resp.StatusCode, respBody); err != nil {
		return fmt.Errorf("finalize failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var errResp api.SiteFinalizeResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != "" {
			if len(errResp.Missing) > 0 {
				return fmt.Errorf("finalize: missing %d file(s) in S3 — first few: %s",
					len(errResp.Missing), strings.Join(firstN(errResp.Missing, 5), ", "))
			}
			return fmt.Errorf("finalize failed: %s", errResp.Error)
		}
		return fmt.Errorf("finalize failed (%d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// dispatchResult is the shape of POST/GET /api/cli/videos/[slug]/render.
type dispatchResult struct {
	Ready        bool    `json:"ready"`
	Status       string  `json:"status"`
	RenderID     string  `json:"render_id,omitempty"`
	DownloadPath string  `json:"download_path,omitempty"`
	Progress     float64 `json:"progress,omitempty"`
	Error        string  `json:"error,omitempty"`
}

func dispatchRender(ctx *auth.Context, agentID, slug string, force bool) (*dispatchResult, error) {
	endpoint := fmt.Sprintf("%s/api/cli/videos/%s/render?agent_id=%s",
		ctx.APIBaseURL, url.PathEscape(slug), agentID)
	if force {
		endpoint += "&force=true"
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, fmt.Errorf("failed to create render request: %w", err)
	}
	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kindship-CLI-Version", Version)

	// 130s — matches the server route's 120s maxDuration with a small
	// network buffer so the CLI never times out while the function still
	// has time to land the renderId.
	client := &http.Client{Timeout: 130 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("render dispatch failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read render response: %w", err)
	}
	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return nil, fmt.Errorf("render dispatch failed: %w", err)
	}

	var result dispatchResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse render response: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		if result.Error != "" {
			return nil, fmt.Errorf("render dispatch failed: %s", result.Error)
		}
		return nil, fmt.Errorf("render dispatch failed (%d): %s", resp.StatusCode, string(body))
	}
	return &result, nil
}

// pollUntilDone polls the render GET endpoint every 2s until ready or
// failed. 20-min timeout protects against runaway Lambda jobs (a 60s
// 1080p video typically renders in 10-30s; even worst-case + cold start
// is well under 5 min).
func pollUntilDone(ctx *auth.Context, agentID, slug string) (*dispatchResult, error) {
	deadline := time.Now().Add(20 * time.Minute)
	endpoint := fmt.Sprintf("%s/api/cli/videos/%s/render?agent_id=%s",
		ctx.APIBaseURL, url.PathEscape(slug), agentID)

	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("render timed out after 20 minutes — check 'kindship video status %s'", slug)
		}

		req, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create poll request: %w", err)
		}
		ctx.SetAuthHeaders(req)
		req.Header.Set("X-Kindship-CLI-Version", Version)

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("poll failed: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
			return nil, fmt.Errorf("poll failed: %w", err)
		}

		var result dispatchResult
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("failed to parse poll response: %w", err)
		}

		switch result.Status {
		case "done":
			if !isJSONFormat() {
				fmt.Printf("\rRender complete.                         \n")
			}
			return &result, nil
		case "failed":
			return nil, fmt.Errorf("render failed: %s", result.Error)
		case "rendering":
			if !isJSONFormat() {
				pct := result.Progress * 100
				fmt.Printf("\rRendering %.0f%%...   ", pct)
			}
		case "pending":
			if !isJSONFormat() {
				fmt.Printf("\rPending Lambda pickup...")
			}
		default:
			if !isJSONFormat() {
				fmt.Printf("\rStatus: %s        ", result.Status)
			}
		}
		time.Sleep(2 * time.Second)
	}
}

// downloadMP4 streams the cached render to disk. Used by render's last
// step + the standalone download command.
func downloadMP4(ctx *auth.Context, agentID, slug, outputPath string) (int64, error) {
	endpoint := fmt.Sprintf("%s/api/cli/videos/%s/mp4?agent_id=%s",
		ctx.APIBaseURL, url.PathEscape(slug), agentID)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create download request: %w", err)
	}
	ctx.SetAuthHeaders(req)
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 600 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("MP4 download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
			return 0, fmt.Errorf("MP4 download failed: %w", err)
		}
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return 0, fmt.Errorf("MP4 download failed: %s", errResp.Error)
		}
		return 0, fmt.Errorf("MP4 download failed (%d): %s", resp.StatusCode, string(body))
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return 0, fmt.Errorf("failed to create %s: %w", outputPath, err)
	}
	defer out.Close()
	n, err := io.Copy(out, resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to write %s: %w", outputPath, err)
	}
	return n, nil
}

func printRenderResult(result videoRenderResult, humanLine string) error {
	if videoRenderFormat == "json" {
		return printJSON(result)
	}
	fmt.Println(humanLine)
	return nil
}

func formatBytes(n int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case n < kb:
		return fmt.Sprintf("%d B", n)
	case n < mb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	case n < gb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	default:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	}
}

func firstN(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return xs[:n]
}
