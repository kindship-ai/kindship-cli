package voice

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// chunkWPM is the spoken-word-per-minute estimate used to size chunks.
// 150 wpm sits at the middle of natural podcast conversation (120-180)
// and is slower than broadcast (180-200). Slightly under-estimating
// means chunks render at or below the target seconds — a cheap safety
// margin against Gemini 3.1 Flash TTS preview drift.
const chunkWPM = 150

// DefaultChunkTargetSeconds is the default target length of a single
// MultiSpeakerTTS chunk. Empirically (2026-04 audition): 90s produced
// too many audible boundaries even with crossfade; 180s reduced
// boundary count but the chunks themselves drifted too far into
// Gemini's long-horizon zone. 100s is the audition sweet spot —
// enough chunks to keep per-chunk drift tight, few enough that the
// crossfade-smoothed boundaries stay unobtrusive. Tune via
// KINDSHIP_VOICE_CHUNK_SECONDS if you want to experiment (0 or
// negative disables chunking — single-call render).
const DefaultChunkTargetSeconds = 100

// ChunkTargetSeconds returns the currently-configured target chunk
// length. Callers pass this into ChunkDialogue. Env var
// KINDSHIP_VOICE_CHUNK_SECONDS overrides the compiled default so
// operators can tune without a CLI release.
func ChunkTargetSeconds() int {
	if v := os.Getenv("KINDSHIP_VOICE_CHUNK_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return DefaultChunkTargetSeconds
}

// ChunkDialogue partitions `lines` into equal-length turn-aligned
// sub-slices. Two-pass algorithm:
//
//  1. Determine chunk count N via greedy fill against targetSeconds as
//     a ceiling (the original behavior).
//  2. If N > 1, re-partition greedily against totalSeconds/N as the
//     target — every chunk lands close to 1/N of the episode, which
//     eliminates tiny tail chunks (where Gemini 3.1 Flash TTS multi-
//     speaker loses voice-identity conditioning and can collapse both
//     roles onto one voice profile — observed on Ripple's v2 run,
//     2026-04-21, last chunk 51s/10 lines rendered with a single voice).
//
// Boundaries ALWAYS fall on turn boundaries — a line is never split
// across chunks. Preserves line order. A single line longer than the
// target lands alone in its own chunk (oversized-line invariant).
//
// Returns nil when `lines` is empty. When targetSeconds <= 0, returns
// a single chunk containing all lines (no chunking). Used by
// cmd.runVoiceMulti to drive the chunked MultiSpeakerTTS render loop.
func ChunkDialogue(lines []PodcastLine, targetSeconds int) [][]PodcastLine {
	if len(lines) == 0 {
		return nil
	}
	if targetSeconds <= 0 {
		out := make([]PodcastLine, len(lines))
		copy(out, lines)
		return [][]PodcastLine{out}
	}

	// Pass 1: ceiling-based greedy chunking to determine N.
	first := greedyChunk(lines, float64(targetSeconds))
	if len(first) <= 1 {
		return first
	}

	// Pass 2: threshold-based equal-length chunking. Threshold is
	// superior to greedy-forward with target=total/N for balancing
	// chunk sizes because greedy accumulates slack in the tail (each
	// chunk under-fills by up to one turn's length, and those underfills
	// sum into a tiny last chunk — observed on Ripple v3 as a 47s
	// tail amid 65-84s peers, even after the equal-target pass).
	//
	// Threshold approach: compute N-1 cumulative boundary targets at
	// k*(total/N) for k=1..N-1. Walk the lines accumulating; close the
	// current chunk as soon as cumulative ≥ next boundary. The final
	// chunk naturally gets its share (~total/N) because previous
	// boundaries don't steal from it.
	total := 0.0
	for _, line := range lines {
		total += EstimateLineSeconds(line)
	}
	return thresholdChunk(lines, total, len(first))
}

// greedyChunk is a turn-aligned forward-fill partitioner: fills each
// chunk to ≤ target seconds, starts a new chunk when adding the next
// line would overshoot. Used for ChunkDialogue's pass-1 (count chunks).
// Single oversized lines land alone (overshoot > split-turn).
func greedyChunk(lines []PodcastLine, target float64) [][]PodcastLine {
	chunks := [][]PodcastLine{{}}
	currentSeconds := 0.0
	cursor := 0

	for _, line := range lines {
		lineSeconds := EstimateLineSeconds(line)
		if currentSeconds > 0 && currentSeconds+lineSeconds > target {
			cursor++
			chunks = append(chunks, []PodcastLine{})
			currentSeconds = 0
		}
		chunks[cursor] = append(chunks[cursor], line)
		currentSeconds += lineSeconds
	}
	return chunks
}

// thresholdChunk partitions `lines` into up to `n` turn-aligned
// chunks of approximately total/n seconds each. Closes the current
// chunk as soon as cumulative time crosses the next k*total/n boundary.
// Distribution is tight: each chunk's size lies within ±max-turn-length
// of total/n, and the last chunk gets its natural share.
//
// Oversized-line invariant preserved: if a line's own estimate exceeds
// the per-chunk target, any content accumulated in the current chunk
// is flushed before the oversized line, so the oversized line lands
// alone in its own chunk (matching greedyChunk's behavior).
//
// May produce fewer than n chunks when one or more oversized lines
// consume disproportionate budget — this is acceptable (strictly
// better than starvation). Assumes n ≥ 1 and total > 0.
func thresholdChunk(lines []PodcastLine, total float64, n int) [][]PodcastLine {
	if n <= 1 {
		out := make([]PodcastLine, len(lines))
		copy(out, lines)
		return [][]PodcastLine{out}
	}
	target := total / float64(n)
	chunks := make([][]PodcastLine, 0, n)
	current := []PodcastLine{}
	cumulative := 0.0

	for _, line := range lines {
		lineSeconds := EstimateLineSeconds(line)
		// Oversized line arriving mid-chunk: flush current first so the
		// oversized line lands alone.
		if lineSeconds > target && len(current) > 0 {
			chunks = append(chunks, current)
			current = nil
		}
		current = append(current, line)
		cumulative += lineSeconds
		// Close current if cumulative crossed the next k*target boundary
		// AND we haven't already emitted n-1 chunks (final chunk gets
		// whatever remains).
		if len(chunks) < n-1 && cumulative >= float64(len(chunks)+1)*target {
			chunks = append(chunks, current)
			current = nil
		}
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

// EstimateLineSeconds converts a PodcastLine's word count into an
// estimated spoken-audio duration using chunkWPM. Performance hints
// and short acknowledgements ("mm", "right") round up to a small floor
// so backchannels don't count as zero.
func EstimateLineSeconds(line PodcastLine) float64 {
	words := countWords(line.Text)
	if words == 0 {
		return 0
	}
	seconds := float64(words) * 60.0 / float64(chunkWPM)
	// Backchannels like "mm" or "right" still take ~0.6s of audio —
	// don't let ≤1-word lines estimate as 0.4s (which they would at
	// 150 wpm exact) and cause chunk boundaries to think they're free.
	const backchannelFloorSeconds = 0.6
	if seconds < backchannelFloorSeconds {
		return backchannelFloorSeconds
	}
	return seconds
}

func countWords(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	return len(strings.Fields(s))
}

// SplitAnnouncement is a sanity-check helper callers can use to log
// the chunking result before rendering. Returns a human-readable
// summary like "3 chunks: [12 lines, ~85.3s] [14 lines, ~89.1s] [7 lines, ~42.7s]".
func SplitAnnouncement(chunks [][]PodcastLine) string {
	if len(chunks) == 0 {
		return "0 chunks"
	}
	var parts []string
	for _, chunk := range chunks {
		total := 0.0
		for _, line := range chunk {
			total += EstimateLineSeconds(line)
		}
		parts = append(parts, fmt.Sprintf("[%d lines, ~%.1fs]", len(chunk), total))
	}
	return fmt.Sprintf("%d chunks: %s", len(chunks), strings.Join(parts, " "))
}
