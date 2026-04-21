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
	// Each line carries a distinct marker word so we can assert the
	// exact boundary point, not just "a boundary exists".
	makeText := func(marker string) string {
		return marker + " " + strings.TrimSpace(strings.Repeat("filler ", 29))
	}
	in := []PodcastLine{
		line("A", makeText("one")),
		line("B", makeText("two")),
		line("A", makeText("three")),
		line("B", makeText("four")),
	}
	got := ChunkDialogue(in, 30)
	if len(got) != 2 {
		t.Fatalf("expected 2 chunks; got %d (summary=%s)", len(got), SplitAnnouncement(got))
	}
	if len(got[0]) != 2 || len(got[1]) != 2 {
		t.Fatalf("expected 2+2 split on turn boundary after line 2; got %d+%d", len(got[0]), len(got[1]))
	}
	if !strings.HasPrefix(got[0][0].Text, "one") || !strings.HasPrefix(got[0][1].Text, "two") {
		t.Fatalf("chunk 0 should contain lines one+two; got %v", []string{got[0][0].Text[:3], got[0][1].Text[:3]})
	}
	if !strings.HasPrefix(got[1][0].Text, "three") || !strings.HasPrefix(got[1][1].Text, "four") {
		t.Fatalf("chunk 1 should contain lines three+four; got %v", []string{got[1][0].Text[:5], got[1][1].Text[:4]})
	}
}

func TestChunkDialogue_OversizedLineStandsAlone(t *testing.T) {
	// A 300-word turn (~120s) exceeds the 90s target. It must be in its
	// own singleton chunk; bundling it with a neighbouring line (even a
	// short backchannel) would push that chunk past target and defeat
	// the anti-drift purpose of chunking.
	huge := strings.TrimSpace(strings.Repeat("word ", 300))
	small := "yeah"
	in := []PodcastLine{line("A", small), line("B", huge), line("A", small)}
	got := ChunkDialogue(in, 90)

	// Locate the huge line's chunk and assert it stands alone.
	var hugeChunkIdx = -1
	for i, c := range got {
		for _, l := range c {
			if l.Text == huge {
				hugeChunkIdx = i
				break
			}
		}
		if hugeChunkIdx >= 0 {
			break
		}
	}
	if hugeChunkIdx < 0 {
		t.Fatalf("huge line not found in any chunk: %s", SplitAnnouncement(got))
	}
	if len(got[hugeChunkIdx]) != 1 {
		t.Fatalf("huge oversized line must be alone in its chunk; got %d lines in chunk %d: %s",
			len(got[hugeChunkIdx]), hugeChunkIdx, SplitAnnouncement(got))
	}

	// Sanity: total line count preserved (no splits, no drops).
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

func TestChunkDialogue_EqualLengthRebalancing(t *testing.T) {
	// Simulate a script where greedy-fill at target 100s would produce
	// one tiny tail chunk. With equal-length rebalancing, the tail
	// should be ~1/N of the total rather than whatever's left over.
	//
	// 14 turns × 30 words each = 420 words ≈ 168s at 150 wpm.
	// Pass 1 at target=100: fills 3 chunks of ~100s until the content
	// runs out → last chunk much smaller. Pass 2 with target=168/N
	// lands N chunks of ~equal length.
	thirtyWords := strings.TrimSpace(strings.Repeat("word ", 30))
	in := make([]PodcastLine, 14)
	for i := range in {
		in[i] = PodcastLine{Speaker: []string{"A", "B"}[i%2], Text: thirtyWords}
	}

	got := ChunkDialogue(in, 100)
	if len(got) < 2 {
		t.Fatalf("expected >= 2 chunks; got %d", len(got))
	}

	// Measure per-chunk estimated duration.
	durations := make([]float64, len(got))
	for i, chunk := range got {
		for _, line := range chunk {
			durations[i] += EstimateLineSeconds(line)
		}
	}
	minDur, maxDur := durations[0], durations[0]
	for _, d := range durations {
		if d < minDur {
			minDur = d
		}
		if d > maxDur {
			maxDur = d
		}
	}
	// Equal-length rebalancing should keep max:min ratio close to 1.
	// Without rebalancing, a tail chunk could be 20% of the target
	// while others are at target — ratio ≥ 5.
	if maxDur/minDur > 1.5 {
		t.Errorf("equal-length rebalancing failed: max/min=%.2f (durations=%v)", maxDur/minDur, durations)
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
