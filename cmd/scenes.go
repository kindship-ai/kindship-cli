package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// extractScenesFromSrc scans src/Composition.tsx (and falls back to
// other src/*.tsx files) for scene definitions. Two strategies, in
// order:
//
//  1. const sceneTimeline = [{ label/name/title: "...", duration: N }, ...]
//     This is the cleanest pattern — explicitly named scenes with
//     declared durations. Thoth's compositions follow it.
//  2. <Sequence from={N} durationInFrames={M}> with NUMERIC LITERAL
//     props. Computed values like {scene[0].duration} are skipped
//     (we can't statically resolve them).
//
// Returns the detected scenes with cumulative `from` offsets computed,
// or nil if neither strategy yields anything. Caller falls back to
// evenly-spaced frame picks in that case.
func extractScenesFromSrc(dir string) ([]sceneMeta, error) {
	candidates := []string{
		filepath.Join(dir, "src", "Composition.tsx"),
	}
	// Glob for other src/*.tsx as fallback (some compositions split
	// across files; Composition.tsx is convention but not enforced).
	moreFiles, _ := filepath.Glob(filepath.Join(dir, "src", "*.tsx"))
	for _, f := range moreFiles {
		if !contains(candidates, f) {
			candidates = append(candidates, f)
		}
	}

	for _, path := range candidates {
		bs, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		src := string(bs)

		if scenes := parseSceneTimeline(src); len(scenes) > 0 {
			return scenes, nil
		}
		if scenes := parseLiteralSequences(src); len(scenes) > 0 {
			return scenes, nil
		}
	}
	return nil, nil
}

// parseSceneTimeline detects the convention pattern:
//
//	const sceneTimeline = [
//	  { label: "Threshold", duration: 240 },
//	  { name:  "Landscape", duration: 240 },
//	  ...
//	] as const;
//
// Tolerates: name/label/title for the human label; duration or
// durationInFrames for the duration; trailing commas; mixed quoting.
// Computes `from` cumulatively from durations.
func parseSceneTimeline(src string) []sceneMeta {
	// Match the array literal — non-greedy so we don't gobble past the
	// first closing bracket. Variable name doesn't have to be literally
	// "sceneTimeline" — anything ending in "Timeline" or "Scenes" is fair
	// game (sceneTimeline, SCENE_TIMELINE, scenes, sceneScenes, etc.).
	arrRe := regexp.MustCompile(`(?si)const\s+\w*scene\w*\s*(?::[^=]+)?=\s*\[(.+?)\]\s*(?:as\s+const)?\s*;`)
	match := arrRe.FindStringSubmatch(src)
	if len(match) < 2 {
		return nil
	}
	body := match[1]

	// Each entry is `{ ... }`. Walk balanced braces — regex alone can't
	// reliably split nested objects, but scene timelines are flat so a
	// brace-balanced scan works here.
	entries := splitBalancedObjects(body)
	scenes := make([]sceneMeta, 0, len(entries))
	cumulativeFrom := 0
	for i, entry := range entries {
		duration := extractNumericField(entry, []string{"durationInFrames", "duration"})
		if duration <= 0 {
			// Skip entries we can't resolve — but keep going. Returning
			// partial scenes is better than refusing to extract anything
			// when one entry has a computed duration.
			continue
		}
		name := extractStringField(entry, []string{"name", "label", "title"})
		if name == "" {
			name = fmt.Sprintf("scene-%d", i)
		}
		scenes = append(scenes, sceneMeta{
			Name:             name,
			From:             cumulativeFrom,
			DurationInFrames: duration,
		})
		cumulativeFrom += duration
	}
	return scenes
}

// parseLiteralSequences finds <Sequence from={N} durationInFrames={M}>
// with NUMERIC LITERAL values only. Sequences with computed/identifier
// props (`from={offset}`) get skipped — caller falls back to
// evenly-spaced if no usable Sequences are found.
func parseLiteralSequences(src string) []sceneMeta {
	// Match <Sequence ...> where the props block contains the two
	// required attrs in either order. Capture both numerics. Allow
	// arbitrary attribute order + extra attrs (className, layout, etc.).
	re := regexp.MustCompile(`<Sequence\s+([^>]+?)>`)
	scenes := []sceneMeta{}
	for i, m := range re.FindAllStringSubmatch(src, -1) {
		if len(m) < 2 {
			continue
		}
		props := m[1]
		from, fromOK := extractJSXNumericProp(props, "from")
		dur, durOK := extractJSXNumericProp(props, "durationInFrames")
		if !fromOK || !durOK {
			continue
		}
		name := extractJSXStringProp(props, "name")
		if name == "" {
			name = fmt.Sprintf("sequence-%d", i)
		}
		scenes = append(scenes, sceneMeta{
			Name:             name,
			From:             from,
			DurationInFrames: dur,
		})
	}
	return scenes
}

// splitBalancedObjects walks a string of `{...}, {...}, {...}` and
// returns the inner-bracketed substrings. Brace-balanced so nested
// objects don't break the split. Quotes are tracked so braces inside
// strings don't count.
func splitBalancedObjects(body string) []string {
	out := []string{}
	depth := 0
	start := -1
	inString := byte(0) // ', ", or `
	escape := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		if escape {
			escape = false
			continue
		}
		if inString != 0 {
			if c == '\\' {
				escape = true
				continue
			}
			if c == inString {
				inString = 0
			}
			continue
		}
		if c == '"' || c == '\'' || c == '`' {
			inString = c
			continue
		}
		if c == '{' {
			if depth == 0 {
				start = i + 1
			}
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 && start >= 0 {
				out = append(out, body[start:i])
				start = -1
			}
		}
	}
	return out
}

// extractNumericField finds `<key>: <number>` inside an object body.
// Tries each key name in order. Returns 0 if none match.
func extractNumericField(body string, keys []string) int {
	for _, key := range keys {
		re := regexp.MustCompile(`(?:^|[\s,{])` + regexp.QuoteMeta(key) + `\s*:\s*(\d+)\b`)
		m := re.FindStringSubmatch(body)
		if len(m) >= 2 {
			n, err := strconv.Atoi(m[1])
			if err == nil {
				return n
			}
		}
	}
	return 0
}

// extractStringField finds `<key>: "<value>"` inside an object body.
// Tries each key name in order. Returns "" if none match.
func extractStringField(body string, keys []string) string {
	for _, key := range keys {
		re := regexp.MustCompile(`(?:^|[\s,{])` + regexp.QuoteMeta(key) + `\s*:\s*["'\x60]([^"'\x60]+)["'\x60]`)
		m := re.FindStringSubmatch(body)
		if len(m) >= 2 {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// extractJSXNumericProp finds `<name>={<number>}` in a JSX prop list.
// Numeric literals only — any non-digit content (identifiers, calls,
// ternaries) returns false.
func extractJSXNumericProp(props, name string) (int, bool) {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*=\s*\{\s*(\d+)\s*\}`)
	m := re.FindStringSubmatch(props)
	if len(m) >= 2 {
		n, err := strconv.Atoi(m[1])
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

// extractJSXStringProp finds `<name>="value"` (or `={'value'}`) in a
// JSX prop list. Used for optional `name=` on Sequence.
func extractJSXStringProp(props, name string) string {
	// double-quoted attribute
	re1 := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*=\s*"([^"]+)"`)
	if m := re1.FindStringSubmatch(props); len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	// braced string literal
	re2 := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*=\s*\{\s*["'\x60]([^"'\x60]+)["'\x60]\s*\}`)
	if m := re2.FindStringSubmatch(props); len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// writeScenesJSON serializes scenes to <dir>/scenes.json. Idempotent —
// overwrites any existing file. Caller decides whether to write.
func writeScenesJSON(dir string, scenes []sceneMeta) error {
	if len(scenes) == 0 {
		return fmt.Errorf("refusing to write empty scenes.json")
	}
	out, err := json.MarshalIndent(scenes, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal scenes: %w", err)
	}
	out = append(out, '\n')
	path := filepath.Join(dir, "scenes.json")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
