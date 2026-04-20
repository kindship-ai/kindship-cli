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

// ChunkDialogue partitions `lines` into sub-slices where the cumulative
// estimated spoken audio of each chunk is close to (but not exceeding)
// targetSeconds. Boundaries ALWAYS fall on turn boundaries — a line is
// never split across chunks. Preserves line order.
//
// A single line longer than targetSeconds goes into its own chunk,
// even though that chunk will overshoot the target. This is fine — the
// alternative (splitting a turn) would break speaker continuity and
// reopen the drift problem we're chunking to solve.
//
// Returns an empty slice when `lines` is empty. When targetSeconds is
// <= 0, returns a single chunk containing all lines (no chunking). Used
// by cmd.runVoiceMulti to drive the chunked MultiSpeakerTTS render
// loop added by the 2026-04 voice-quality plan.
func ChunkDialogue(lines []PodcastLine, targetSeconds int) [][]PodcastLine {
	if len(lines) == 0 {
		return nil
	}
	if targetSeconds <= 0 {
		out := make([]PodcastLine, len(lines))
		copy(out, lines)
		return [][]PodcastLine{out}
	}

	chunks := [][]PodcastLine{{}}
	currentSeconds := 0.0
	cursor := 0

	for _, line := range lines {
		lineSeconds := EstimateLineSeconds(line)
		// If adding this line would overshoot AND the current chunk
		// already has at least one line, close the current chunk and
		// start a new one. Single oversized lines still land alone.
		if currentSeconds > 0 && currentSeconds+lineSeconds > float64(targetSeconds) {
			cursor++
			chunks = append(chunks, []PodcastLine{})
			currentSeconds = 0
		}
		chunks[cursor] = append(chunks[cursor], line)
		currentSeconds += lineSeconds
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
