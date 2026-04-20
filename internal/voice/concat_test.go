package voice

import (
	"testing"
)

// sine approximates a 16-bit signed PCM sine-wave chunk of a given
// sample count with constant amplitude — used to make crossfade
// behavior observable in tests.
func flatChunk(samples int, amp int16) []byte {
	out := make([]int16, samples)
	for i := range out {
		out[i] = amp
	}
	return int16ToBytes(out)
}

func TestConcatWithCrossfade_EmptyInput(t *testing.T) {
	if got, err := ConcatWithCrossfade(nil, 24000, 0.3); err != nil || got != nil {
		t.Fatalf("empty input should return (nil,nil); got (%v, %v)", got, err)
	}
}

func TestConcatWithCrossfade_SingleChunkPassthrough(t *testing.T) {
	a := flatChunk(1000, 5000)
	got, err := ConcatWithCrossfade([][]byte{a}, 24000, 0.3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(a) {
		t.Fatalf("single chunk should pass through; got %d bytes, want %d", len(got), len(a))
	}
}

func TestConcatWithCrossfade_OddLengthRejected(t *testing.T) {
	_, err := ConcatWithCrossfade([][]byte{{0x00, 0x00, 0x00}}, 24000, 0.3)
	if err == nil {
		t.Fatalf("odd-length chunk should error")
	}
}

func TestConcatWithCrossfade_FadeDisabledWhenSecondsZero(t *testing.T) {
	// fade=0 → byte-for-byte concat, total length = sum(lengths).
	a := flatChunk(1000, 5000)
	b := flatChunk(800, 3000)
	got, err := ConcatWithCrossfade([][]byte{a, b}, 24000, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(a)+len(b) {
		t.Fatalf("fade=0 should be flat concat; got %d bytes, want %d", len(got), len(a)+len(b))
	}
}

func TestConcatWithCrossfade_OverlapShortensOutput(t *testing.T) {
	// With crossfade, the fadeSamples region from the tail of A is
	// summed with the head of B — so output length drops by
	// (fadeSamples * 2 bytes) per boundary compared to flat concat.
	a := flatChunk(24000, 8000) // 1 second at 24kHz
	b := flatChunk(24000, 4000)
	fade := 0.3
	fadeSamples := int(fade * 24000) // 7200

	got, err := ConcatWithCrossfade([][]byte{a, b}, 24000, fade)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := len(a) + len(b) - fadeSamples*2 // 2 bytes/sample
	if len(got) != expected {
		t.Fatalf("crossfade should shorten output by 2*fadeSamples bytes per boundary; got %d, want %d",
			len(got), expected)
	}
}

func TestConcatWithCrossfade_MidpointIsBlend(t *testing.T) {
	// At the exact middle of the crossfade region, output should be
	// (A + B) / 2 (aGain=bGain=0.5). With A=8000 and B=4000, midpoint
	// sample should be ~6000.
	a := flatChunk(24000, 8000)
	b := flatChunk(24000, 4000)
	fade := 0.3
	fadeSamples := int(fade * 24000)

	got, err := ConcatWithCrossfade([][]byte{a, b}, 24000, fade)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mid := bytesToInt16(got)
	// Midpoint of the fade region lives at len(a) - fadeSamples + fadeSamples/2.
	midIdx := len(mid) - (len(b) / 2) - fadeSamples/2
	// Fallback: compute directly from known geometry — the crossfade
	// starts at sample (len(a)/2 - fadeSamples) in `got` and the mid
	// is fadeSamples/2 after that.
	start := (len(a) / 2) - fadeSamples
	midIdx = start + fadeSamples/2
	v := mid[midIdx]
	if v < 5800 || v > 6200 {
		t.Fatalf("midpoint should be ~(8000+4000)/2=6000; got %d at idx %d", v, midIdx)
	}
}

func TestConcatWithCrossfade_EndpointValues(t *testing.T) {
	// The very first sample of the fade region should still be ~A's
	// value (aGain≈1, bGain≈0); the very last sample of the fade
	// region should be ~B's value (aGain≈0, bGain≈1).
	a := flatChunk(24000, 8000)
	b := flatChunk(24000, 4000)
	fade := 0.3
	fadeSamples := int(fade * 24000)
	got, _ := ConcatWithCrossfade([][]byte{a, b}, 24000, fade)
	samples := bytesToInt16(got)

	// Fade region starts at len(A)/2 - fadeSamples samples, ends at
	// len(A)/2 samples.
	fadeStart := (len(a) / 2) - fadeSamples
	first := samples[fadeStart]
	last := samples[fadeStart+fadeSamples-1]
	if first < 7800 || first > 8000 {
		t.Errorf("first fade sample should be close to A's value (8000); got %d", first)
	}
	if last < 3900 || last > 4100 {
		t.Errorf("last fade sample should be close to B's value (4000); got %d", last)
	}
}

func TestConcatWithCrossfade_ManyChunks(t *testing.T) {
	// 5 chunks with distinct amplitudes — output should exist, have
	// correct total length (sum - 4*fadeSamples overlap), and no panic.
	chunks := [][]byte{
		flatChunk(24000, 1000),
		flatChunk(24000, 2000),
		flatChunk(24000, 3000),
		flatChunk(24000, 4000),
		flatChunk(24000, 5000),
	}
	fade := 0.3
	fadeSamples := int(fade * 24000)
	got, err := ConcatWithCrossfade(chunks, 24000, fade)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	total := 0
	for _, c := range chunks {
		total += len(c)
	}
	expected := total - 4*fadeSamples*2 // 4 boundaries, 2 bytes/sample
	if len(got) != expected {
		t.Fatalf("expected %d bytes (%d chunks − 4 overlaps), got %d", expected, len(chunks), len(got))
	}
}
