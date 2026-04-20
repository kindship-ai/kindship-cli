package voice

import (
	"strings"
	"testing"
)

func line(speaker, text string) PodcastLine {
	return PodcastLine{Speaker: speaker, Text: text}
}

func TestChunkDialogue_Empty(t *testing.T) {
	if got := ChunkDialogue(nil, 90); got != nil {
		t.Fatalf("empty input → nil; got %v", got)
	}
}

func TestChunkDialogue_TargetZeroIsNoOp(t *testing.T) {
	in := []PodcastLine{line("A", "hello world"), line("B", "hi back")}
	got := ChunkDialogue(in, 0)
	if len(got) != 1 {
		t.Fatalf("target=0 → single chunk; got %d chunks", len(got))
	}
	if len(got[0]) != 2 {
		t.Fatalf("target=0 → all lines in one chunk; got %d lines", len(got[0]))
	}
}

func TestChunkDialogue_SplitsOnTurnBoundary(t *testing.T) {
	// 30-word lines ≈ 12s each at 150 wpm. With target=30s, first two
	// lines fit (~24s), third line would push us to ~36s → new chunk.
	thirty := strings.TrimSpace(strings.Repeat("word ", 30))
	in := []PodcastLine{
		line("A", thirty),
		line("B", thirty),
		line("A", thirty),
		line("B", thirty),
	}
	got := ChunkDialogue(in, 30)
	if len(got) != 2 {
		t.Fatalf("expected 2 chunks; got %d (summary=%s)", len(got), SplitAnnouncement(got))
	}
	// Boundary must be on a turn boundary → chunk 1 ends after line 2,
	// chunk 2 starts with line 3.
	if got[0][len(got[0])-1].Text != thirty || got[1][0].Text != thirty {
		t.Fatalf("chunk boundary landed mid-content: %+v", got)
	}
	if len(got[0])+len(got[1]) != 4 {
		t.Fatalf("lines lost across split: got %d+%d, want 4", len(got[0]), len(got[1]))
	}
}

func TestChunkDialogue_OversizedLineStandsAlone(t *testing.T) {
	// A 300-word turn (~120s) exceeds the 90s target. It must still
	// land alone in its own chunk; splitting a turn is never OK.
	huge := strings.TrimSpace(strings.Repeat("word ", 300))
	small := "yeah"
	in := []PodcastLine{line("A", small), line("B", huge), line("A", small)}
	got := ChunkDialogue(in, 90)
	// [small] [huge] [small] OR [small, huge] → depends on small's
	// ~0.6s floor vs huge's 120s. small + huge would be 120.6s, way
	// over — so we expect the accumulator to flush `small` into its
	// own chunk before placing `huge`.
	if len(got) < 2 {
		t.Fatalf("oversized line should start a new chunk; got %d chunks: %s", len(got), SplitAnnouncement(got))
	}
	// Verify no line was lost or split.
	count := 0
	for _, c := range got {
		count += len(c)
	}
	if count != 3 {
		t.Fatalf("lines lost or duplicated: %d vs 3", count)
	}
}

func TestEstimateLineSeconds_BackchannelFloor(t *testing.T) {
	if got := EstimateLineSeconds(line("A", "mm")); got < 0.6 {
		t.Fatalf("single-word backchannel should floor at 0.6s; got %.2f", got)
	}
	if got := EstimateLineSeconds(line("A", "")); got != 0 {
		t.Fatalf("empty line → 0s; got %.2f", got)
	}
}

func TestEstimateLineSeconds_150wpm(t *testing.T) {
	// 150 words → 60 seconds at 150 wpm exactly.
	text := strings.TrimSpace(strings.Repeat("word ", 150))
	got := EstimateLineSeconds(line("A", text))
	if got < 59 || got > 61 {
		t.Fatalf("150 words should estimate ~60s at 150 wpm; got %.2f", got)
	}
}
