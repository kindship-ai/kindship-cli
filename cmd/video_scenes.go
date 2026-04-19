package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	videoScenesDir    string
	videoScenesWrite  bool
	videoScenesFormat string
)

var videoScenesCmd = &cobra.Command{
	Use:   "scenes <slug>",
	Short: "Detect scenes in src/Composition.tsx and (optionally) write scenes.json",
	Long: `Scan the composition source for scene definitions, print the result.

Detection strategies (tried in order):
  1. const sceneTimeline = [{ label/name/title, duration }, ...]
  2. <Sequence from={N} durationInFrames={M}> with NUMERIC LITERALS
     (Sequences with computed props are skipped silently)

Used by 'kindship video review-frames' for scene-aware frame sampling
(4 frames per scene at 0/0.25/0.5/0.75) — running 'scenes' first lets
you see what the extractor detected before the review burns Gemini
budget on stills you didn't intend.

By default, prints scenes to stdout. Pass --write to also persist
to <dir>/scenes.json so the extractor result is reproducible.

Examples:
  kindship video scenes arcane-library-full-glory
  kindship video scenes arcane-library-full-glory --write
  kindship video scenes arcane-library-full-glory --format json`,
	Args: cobra.ExactArgs(1),
	RunE: runVideoScenes,
}

func init() {
	videoScenesCmd.Flags().StringVar(&videoScenesDir, "dir", "", "video workspace dir (default /workspace/videos/<slug>/)")
	videoScenesCmd.Flags().BoolVar(&videoScenesWrite, "write", false, "also write the result to <dir>/scenes.json")
	videoScenesCmd.Flags().StringVar(&videoScenesFormat, "format", "text", "output format: text or json")
	videoCmd.AddCommand(videoScenesCmd)
}

func runVideoScenes(_ *cobra.Command, args []string) error {
	slug := args[0]
	if err := validateSlug(slug); err != nil {
		return err
	}

	dir := resolveVideoDir(slug, videoScenesDir)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("video workspace not found at %s; pass --dir or scaffold first", dir)
	}

	scenes, err := extractScenesFromSrc(dir)
	if err != nil {
		return fmt.Errorf("scene extraction failed: %w", err)
	}

	if len(scenes) == 0 {
		if videoScenesFormat == "json" {
			fmt.Println("[]")
			return nil
		}
		fmt.Println("No scenes detected.")
		fmt.Println()
		fmt.Println("The extractor looks for either:")
		fmt.Println("  - const sceneTimeline = [{ label, duration }, ...]")
		fmt.Println("  - <Sequence from={N} durationInFrames={M}> with numeric literals")
		fmt.Println()
		fmt.Println("Sequences using computed values (variables, expressions) are skipped.")
		fmt.Println("'kindship video review-frames' will fall back to 8 evenly-spaced frames.")
		return nil
	}

	if videoScenesWrite {
		if err := writeScenesJSON(dir, scenes); err != nil {
			return err
		}
	}

	if videoScenesFormat == "json" {
		out, err := json.MarshalIndent(scenes, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal: %w", err)
		}
		fmt.Println(string(out))
		return nil
	}

	fmt.Printf("Detected %d scene(s):\n", len(scenes))
	totalFrames := 0
	for i, s := range scenes {
		end := s.From + s.DurationInFrames
		fmt.Printf("  [%d] %-24s frames %5d-%-5d  (%4d frames)\n",
			i+1, s.Name, s.From, end, s.DurationInFrames)
		totalFrames = end
	}
	fmt.Printf("Total: %d frames across %d scene(s).\n", totalFrames, len(scenes))
	if videoScenesWrite {
		fmt.Printf("Wrote scenes.json to %s\n", dir)
	} else {
		fmt.Println()
		fmt.Println("Pass --write to persist these to scenes.json.")
	}
	return nil
}
