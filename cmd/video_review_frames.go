package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"

	"github.com/spf13/cobra"
)

var (
	videoReviewFramesDir     string
	videoReviewFramesFrames  string
	videoReviewFramesOutput  string
	videoReviewFramesFormat  string
	videoReviewFramesPrompt  string
	videoReviewFramesModel   string
	videoReviewFramesKeep    bool
)

const defaultFramesPerScene = 4
const defaultEvenFrameCount = 8

var videoReviewFramesCmd = &cobra.Command{
	Use:   "review-frames <slug>",
	Short: "Review the video as a batch of stills with Gemini 3.1 Pro (skips render)",
	Long: `Render N stills locally with 'npx remotion still', ship them to
Gemini 3.1 Pro for a frame-level UI review. Skips the ~$0.01 + 2-min
Lambda render entirely — perfect for tight iteration loops where you
care about layout/spacing/edge-safety rather than motion.

Frame selection precedence:
  1. --frames "00:00,00:04,..." (explicit override) → use exactly those
  2. <dir>/scenes.json present → 4 frames per scene at 0/0.25/0.5/0.75
  3. fallback → 8 evenly-spaced frames across compositions.json duration

scenes.json shape (sibling of compositions.json):
  [
    { "name": "intro", "from": 0, "durationInFrames": 120 },
    { "name": "main",  "from": 120, "durationInFrames": 720 }
  ]

The motion-related rubric checks (transitions, flicker, dynamic
stability) are intentionally dropped — stills carry no motion info.

Examples:
  kindship video review-frames arcane-library-full-glory
  kindship video review-frames arcane-library-full-glory --frames "0:00,0:24,0:48"
  kindship video review-frames arcane-library-full-glory --output review.md`,
	Args: cobra.ExactArgs(1),
	RunE: runVideoReviewFrames,
}

func init() {
	videoReviewFramesCmd.Flags().StringVar(&videoReviewFramesDir, "dir", "", "video workspace dir (default /workspace/videos/<slug>/)")
	videoReviewFramesCmd.Flags().StringVar(&videoReviewFramesFrames, "frames", "", "comma-separated timestamps to render (mm:ss[.fff]); overrides scenes.json")
	videoReviewFramesCmd.Flags().StringVar(&videoReviewFramesOutput, "output", "", "save review to file (default: print to stdout)")
	videoReviewFramesCmd.Flags().StringVar(&videoReviewFramesFormat, "format", "text", "output format: text, markdown, or json")
	videoReviewFramesCmd.Flags().StringVar(&videoReviewFramesPrompt, "prompt", "", "override the bundled review rubric")
	videoReviewFramesCmd.Flags().StringVar(&videoReviewFramesModel, "model", "", "override the Gemini model id (default: gemini-3.1-pro-preview)")
	videoReviewFramesCmd.Flags().BoolVar(&videoReviewFramesKeep, "keep", false, "keep the rendered PNG frames on disk after review (default: clean up)")
	videoCmd.AddCommand(videoReviewFramesCmd)
}

type compositionMeta struct {
	ID               string `json:"id"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	FPS              int    `json:"fps"`
	DurationInFrames int    `json:"durationInFrames"`
}

type sceneMeta struct {
	Name             string `json:"name"`
	From             int    `json:"from"`
	DurationInFrames int    `json:"durationInFrames"`
}

type framePick struct {
	timestamp string // human label, "mm:ss" or "mm:ss.fff"
	frame     int    // absolute frame number
	pngPath   string // populated after `npx remotion still` runs
}

func runVideoReviewFrames(_ *cobra.Command, args []string) error {
	slug := args[0]
	if err := validateSlug(slug); err != nil {
		return err
	}

	dir := resolveVideoDir(slug, videoReviewFramesDir)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("video workspace not found at %s; pass --dir or scaffold first", dir)
	}

	compFile := filepath.Join(dir, "compositions.json")
	composition, err := loadComposition(compFile)
	if err != nil {
		return err
	}

	picks, err := pickFrames(dir, composition, videoReviewFramesFrames)
	if err != nil {
		return err
	}
	if len(picks) == 0 {
		return fmt.Errorf("no frames selected — pass --frames or add scenes.json")
	}

	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}
	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	if videoReviewFramesFormat != "json" {
		fmt.Printf("Rendering %d frames via `npx remotion still`...\n", len(picks))
	}

	framesDir, err := os.MkdirTemp(dir, "review-frames-")
	if err != nil {
		return fmt.Errorf("failed to create temp frames dir: %w", err)
	}
	if !videoReviewFramesKeep {
		defer os.RemoveAll(framesDir)
	}

	if err := renderStills(dir, composition.ID, picks, framesDir); err != nil {
		return err
	}

	if videoReviewFramesFormat != "json" {
		fmt.Printf("Uploading frames to Kindship → Gemini... (typically 30-90s)\n")
	}

	resp, err := uploadFramesForReview(ctx, agentID, slug, picks)
	if err != nil {
		return err
	}

	if videoReviewFramesOutput != "" {
		if err := os.WriteFile(videoReviewFramesOutput, []byte(resp.Review), 0o644); err != nil {
			return fmt.Errorf("failed to write %s: %w", videoReviewFramesOutput, err)
		}
		if videoReviewFramesFormat != "json" {
			fmt.Printf("Saved review to %s (%d chars, model=%s, %.1fs total)\n",
				videoReviewFramesOutput, len(resp.Review), resp.Model,
				float64(resp.TotalMS)/1000)
		}
	}

	switch videoReviewFramesFormat {
	case "json":
		return printJSON(resp)
	case "markdown":
		if videoReviewFramesOutput == "" {
			fmt.Println(resp.Review)
		}
		return nil
	default:
		if videoReviewFramesOutput == "" {
			fmt.Println(resp.Review)
			fmt.Printf("\n— %s, %d frames (%.1f MB), %.1fs total (upload %.1fs, generate %.1fs)\n",
				resp.Model, resp.FramesUploaded,
				float64(resp.TotalBytes)/(1024*1024),
				float64(resp.TotalMS)/1000,
				float64(resp.UploadMS)/1000,
				float64(resp.GenerateMS)/1000)
			if videoReviewFramesKeep {
				fmt.Printf("Rendered frames kept at %s\n", framesDir)
			}
		}
		return nil
	}
}

// loadComposition reads compositions.json, expects an array, picks the
// first composition (the convention is one composition per slug-dir).
func loadComposition(path string) (*compositionMeta, error) {
	bs, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	var arr []compositionMeta
	if err := json.Unmarshal(bs, &arr); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("%s contained no compositions", path)
	}
	c := arr[0]
	if c.FPS <= 0 || c.DurationInFrames <= 0 {
		return nil, fmt.Errorf("%s composition missing fps or durationInFrames", path)
	}
	return &c, nil
}

// pickFrames implements the precedence:
//
//  1. --frames "0:00,..."     → use exactly those (explicit wins)
//  2. <dir>/scenes.json       → 4 frames per scene at 0/0.25/0.5/0.75
//  3. extract from src/       → same shape, but derived from
//                               sceneTimeline / literal Sequence regex
//  4. 8 evenly-spaced fallback
//
// Step 3 is the auto-discovery path so agents using the conventional
// patterns get scene-aware sampling without hand-authoring scenes.json.
// Returns frames sorted by absolute frame number with deduped timestamps.
func pickFrames(
	dir string,
	c *compositionMeta,
	framesArg string,
) ([]framePick, error) {
	if framesArg != "" {
		return parseExplicitFrames(framesArg, c.FPS, c.DurationInFrames)
	}

	scenesPath := filepath.Join(dir, "scenes.json")
	if _, err := os.Stat(scenesPath); err == nil {
		return picksFromScenes(scenesPath, c.FPS, c.DurationInFrames)
	}

	// Auto-extract from src/. Best-effort — falls back to evenly-spaced
	// when the composition uses computed Sequence props or a non-
	// conventional scene structure.
	if scenes, _ := extractScenesFromSrc(dir); len(scenes) > 0 {
		if !isJSONFormat() {
			fmt.Printf("Auto-detected %d scene(s) from src/ — sampling 4 frames per scene\n", len(scenes))
		}
		return picksFromSceneMetas(scenes, c.FPS, c.DurationInFrames), nil
	}

	if !isJSONFormat() {
		fmt.Printf("No scenes detected — sampling %d evenly-spaced frames\n", defaultEvenFrameCount)
	}
	return evenlySpacedPicks(c.FPS, c.DurationInFrames, defaultEvenFrameCount), nil
}

// picksFromSceneMetas converts in-memory scene metas to framePicks
// using the same 0/0.25/0.5/0.75 strategy as picksFromScenes (which
// reads from scenes.json). Kept separate so the auto-extract path
// doesn't have to round-trip through disk.
func picksFromSceneMetas(scenes []sceneMeta, fps, totalFrames int) []framePick {
	offsets := []float64{0, 0.25, 0.5, 0.75}
	picks := make([]framePick, 0, len(scenes)*defaultFramesPerScene)
	for _, scene := range scenes {
		if scene.DurationInFrames <= 0 {
			continue
		}
		for _, o := range offsets {
			frame := scene.From + int(float64(scene.DurationInFrames)*o)
			if frame < 0 {
				frame = 0
			}
			if frame >= totalFrames {
				frame = totalFrames - 1
			}
			picks = append(picks, framePick{timestamp: frameToTimestamp(frame, fps), frame: frame})
		}
	}
	return dedupeAndSort(picks)
}

func parseExplicitFrames(arg string, fps, totalFrames int) ([]framePick, error) {
	parts := strings.Split(arg, ",")
	picks := make([]framePick, 0, len(parts))
	for _, raw := range parts {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		frame, label, err := timestampToFrame(trimmed, fps)
		if err != nil {
			return nil, fmt.Errorf("invalid --frames entry %q: %w", trimmed, err)
		}
		if frame < 0 || frame >= totalFrames {
			return nil, fmt.Errorf("--frames %q resolves to frame %d, out of range [0, %d)", trimmed, frame, totalFrames)
		}
		picks = append(picks, framePick{timestamp: label, frame: frame})
	}
	return dedupeAndSort(picks), nil
}

func picksFromScenes(path string, fps, totalFrames int) ([]framePick, error) {
	bs, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	var scenes []sceneMeta
	if err := json.Unmarshal(bs, &scenes); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	if len(scenes) == 0 {
		return nil, fmt.Errorf("%s contained no scenes", path)
	}

	offsets := []float64{0, 0.25, 0.5, 0.75}
	picks := make([]framePick, 0, len(scenes)*defaultFramesPerScene)
	for _, scene := range scenes {
		if scene.DurationInFrames <= 0 {
			continue
		}
		for _, o := range offsets {
			frame := scene.From + int(float64(scene.DurationInFrames)*o)
			if frame < 0 {
				frame = 0
			}
			if frame >= totalFrames {
				frame = totalFrames - 1
			}
			label := frameToTimestamp(frame, fps)
			picks = append(picks, framePick{timestamp: label, frame: frame})
		}
	}
	return dedupeAndSort(picks), nil
}

func evenlySpacedPicks(fps, totalFrames, count int) []framePick {
	if count <= 0 || totalFrames <= 0 {
		return nil
	}
	picks := make([]framePick, 0, count)
	if count == 1 {
		f := totalFrames / 2
		picks = append(picks, framePick{timestamp: frameToTimestamp(f, fps), frame: f})
		return picks
	}
	for i := 0; i < count; i++ {
		// Evenly distribute across the [0, totalFrames-1] range.
		f := int(float64(totalFrames-1) * (float64(i) / float64(count-1)))
		if f < 0 {
			f = 0
		}
		if f >= totalFrames {
			f = totalFrames - 1
		}
		picks = append(picks, framePick{timestamp: frameToTimestamp(f, fps), frame: f})
	}
	return dedupeAndSort(picks)
}

func dedupeAndSort(picks []framePick) []framePick {
	if len(picks) <= 1 {
		return picks
	}
	sort.SliceStable(picks, func(i, j int) bool { return picks[i].frame < picks[j].frame })
	out := picks[:0]
	prev := -1
	for _, p := range picks {
		if p.frame == prev {
			continue
		}
		out = append(out, p)
		prev = p.frame
	}
	return out
}

// timestampToFrame parses "mm:ss[.fff]" or "ss[.fff]" and returns the
// resolved absolute frame number + the original label (preserved for
// downstream display).
func timestampToFrame(label string, fps int) (int, string, error) {
	parts := strings.Split(label, ":")
	var minutes int
	var secondsStr string
	switch len(parts) {
	case 1:
		secondsStr = parts[0]
	case 2:
		m, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, "", fmt.Errorf("minutes part not numeric: %q", parts[0])
		}
		minutes = m
		secondsStr = parts[1]
	default:
		return 0, "", fmt.Errorf("expected mm:ss or ss, got %q", label)
	}
	seconds, err := strconv.ParseFloat(secondsStr, 64)
	if err != nil {
		return 0, "", fmt.Errorf("seconds part not numeric: %q", secondsStr)
	}
	totalSeconds := float64(minutes)*60 + seconds
	frame := int(totalSeconds * float64(fps))
	return frame, label, nil
}

func frameToTimestamp(frame, fps int) string {
	totalMs := int(float64(frame) / float64(fps) * 1000)
	mm := totalMs / 60000
	ss := (totalMs / 1000) % 60
	ms := totalMs % 1000
	if ms == 0 {
		return fmt.Sprintf("%02d:%02d", mm, ss)
	}
	return fmt.Sprintf("%02d:%02d.%03d", mm, ss, ms)
}

// renderStills shells out to `npx remotion still` once per pick.
// Sequential to avoid overloading the agent's container; each render
// is fast (~1-3s) so wall time stays reasonable for ≤40 frames.
func renderStills(dir, compositionID string, picks []framePick, framesDir string) error {
	for i, p := range picks {
		// JPEG (not PNG) so 8-40 frames at 1080p fit in Vercel's 4.5MB
		// function body limit. Quality=85 keeps layout/text crisp enough
		// for Gemini's frame-level critique while landing each frame at
		// ~200-500KB. Filename uses the index so sort order matches the
		// picks slice; timestamp lives in the label we ship to Gemini.
		out := filepath.Join(framesDir, fmt.Sprintf("%03d.jpg", i))
		// `npx remotion still` signature is positional: entry, composition,
		// output. --frame is a flag. The composition can't be passed via
		// --composition (that flag doesn't exist on `still`); putting the
		// output path before the composition id makes Remotion try to load
		// it as the composition and errors out.
		args := []string{
			"remotion",
			"still",
			"./src/index.ts",
			compositionID,
			out,
			fmt.Sprintf("--frame=%d", p.frame),
			"--image-format=jpeg",
			"--jpeg-quality=85",
		}
		cmd := exec.Command("npx", args...)
		cmd.Dir = dir
		if videoReviewFramesFormat == "json" {
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard
		} else {
			cmd.Stdout = io.Discard
			cmd.Stderr = os.Stderr
		}
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("npx remotion still failed for frame %d (%s): %w (run manually for full error)", p.frame, p.timestamp, err)
		}
		if _, err := os.Stat(out); err != nil {
			return fmt.Errorf("expected output %s missing after `npx remotion still`: %w", out, err)
		}
		picks[i].pngPath = out
		if videoReviewFramesFormat != "json" {
			fmt.Printf("  [%d/%d] %s frame=%d\n", i+1, len(picks), p.timestamp, p.frame)
		}
	}
	return nil
}

// uploadFramesForReview builds the multipart body and POSTs to the
// review-frames endpoint. Buffered (not streamed) because Go's
// multipart writer + http.NewRequest doesn't support deferred body
// writes well, and the total payload caps at 100MB anyway.
func uploadFramesForReview(
	ctx *auth.Context,
	agentID, slug string,
	picks []framePick,
) (*api.VideoReviewFramesResponse, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	if videoReviewFramesPrompt != "" {
		_ = writer.WriteField("prompt", videoReviewFramesPrompt)
	}
	if videoReviewFramesModel != "" {
		_ = writer.WriteField("model", videoReviewFramesModel)
	}

	for _, p := range picks {
		_ = writer.WriteField("timestamps", p.timestamp)
		f, err := os.Open(p.pngPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open %s: %w", p.pngPath, err)
		}
		part, err := writer.CreateFormFile("frames", filepath.Base(p.pngPath))
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("failed to create form file: %w", err)
		}
		if _, err := io.Copy(part, f); err != nil {
			f.Close()
			return nil, fmt.Errorf("failed to copy %s: %w", p.pngPath, err)
		}
		f.Close()
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("failed to finalize multipart: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/cli/videos/%s/review-frames?agent_id=%s",
		ctx.APIBaseURL, url.PathEscape(slug), agentID)
	req, err := http.NewRequest(http.MethodPost, endpoint, &buf)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 330 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("review-frames request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return nil, fmt.Errorf("review-frames failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var errResp api.VideoReviewFramesResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("review-frames failed: %s", errResp.Error)
		}
		return nil, fmt.Errorf("review-frames failed (%d): %s", resp.StatusCode, string(body))
	}

	var out api.VideoReviewFramesResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &out, nil
}
