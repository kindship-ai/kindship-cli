package voice

import (
	"encoding/binary"
	"fmt"
	"math"
)

// DefaultCrossfadeSeconds is the crossfade duration applied at chunk
// boundaries when concatenating normalized PCM. 300ms is long enough
// to mask the per-chunk voice-interpretation variance we see at
// boundaries (where Gemini may pick a slightly different prosody for
// the same voice between separate calls) but short enough that no
// perceptible beat of speech is lost to the fade. Shorter fades
// (≤150ms) still leave audible transitions; longer (≥500ms) start
// swallowing syllables.
const DefaultCrossfadeSeconds = 0.3

// ConcatWithCrossfade joins N chunks of raw 16-bit signed little-endian
// mono PCM with an equal-power linear crossfade at every chunk boundary.
// The last fadeSamples of chunk i and the first fadeSamples of chunk i+1
// are ramped out / ramped in respectively and summed sample-wise,
// replacing the overlapping regions. Chunks shorter than 2 * fadeSamples
// are appended without a fade (degenerate case).
//
// Used by cmd.runVoiceMulti to smooth the audible seams between
// per-chunk MultiSpeakerTTS calls. Equal-power (linear-gain) fade is
// a good compromise for dialogue — log-amplitude crossfades sound
// more natural for music but over-damp quick consonant transitions
// for speech.
func ConcatWithCrossfade(chunks [][]byte, sampleRate int, fadeSeconds float64) ([]byte, error) {
	if len(chunks) == 0 {
		return nil, nil
	}
	if sampleRate <= 0 {
		return nil, fmt.Errorf("ConcatWithCrossfade: sampleRate must be > 0, got %d", sampleRate)
	}
	for i, c := range chunks {
		if len(c)%2 != 0 {
			return nil, fmt.Errorf("ConcatWithCrossfade: chunk %d has odd byte length %d; 16-bit PCM requires even", i, len(c))
		}
	}
	if len(chunks) == 1 {
		out := make([]byte, len(chunks[0]))
		copy(out, chunks[0])
		return out, nil
	}

	fadeSamples := int(fadeSeconds * float64(sampleRate))
	if fadeSamples <= 0 {
		return flatConcat(chunks), nil
	}

	// Convert each chunk from bytes → int16 slice for cheaper arithmetic.
	intChunks := make([][]int16, len(chunks))
	for i, c := range chunks {
		intChunks[i] = bytesToInt16(c)
	}

	// Start with the first chunk as-is; progressively fold in each
	// subsequent chunk with crossfade. Using int16 accumulation avoids
	// an N-pass byte round-trip.
	out := make([]int16, 0, totalSamples(intChunks))
	out = append(out, intChunks[0]...)

	for i := 1; i < len(intChunks); i++ {
		next := intChunks[i]
		effectiveFade := fadeSamples
		if len(out) < 2*effectiveFade || len(next) < 2*effectiveFade {
			// Not enough room on either side for a full fade; skip the
			// fade to avoid clipping audible content. This only fires
			// on degenerate inputs (chunks shorter than 0.6s); normal
			// ~180s chunks have plenty of room.
			out = append(out, next...)
			continue
		}

		tailStart := len(out) - effectiveFade
		// Crossfade the overlapping region: linear ramp-out on tail,
		// linear ramp-in on next's head, sum sample-wise.
		for s := 0; s < effectiveFade; s++ {
			t := float64(s) / float64(effectiveFade-1) // 0.0 → 1.0 linearly
			aGain := 1.0 - t
			bGain := t
			a := float64(out[tailStart+s]) * aGain
			b := float64(next[s]) * bGain
			mixed := a + b
			// Clamp to int16 range in case two near-peak samples sum.
			if mixed > math.MaxInt16 {
				mixed = math.MaxInt16
			}
			if mixed < math.MinInt16 {
				mixed = math.MinInt16
			}
			out[tailStart+s] = int16(mixed)
		}
		// Append the rest of next (skipping its head which was consumed
		// by the crossfade).
		out = append(out, next[effectiveFade:]...)
	}

	return int16ToBytes(out), nil
}

// flatConcat joins chunks byte-for-byte with no crossfade — used when
// fadeSeconds <= 0 and as the single-chunk fast path. Separate function
// mostly for test clarity.
func flatConcat(chunks [][]byte) []byte {
	total := 0
	for _, c := range chunks {
		total += len(c)
	}
	out := make([]byte, 0, total)
	for _, c := range chunks {
		out = append(out, c...)
	}
	return out
}

func totalSamples(chunks [][]int16) int {
	n := 0
	for _, c := range chunks {
		n += len(c)
	}
	return n
}

func bytesToInt16(b []byte) []int16 {
	n := len(b) / 2
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = int16(binary.LittleEndian.Uint16(b[i*2 : i*2+2]))
	}
	return out
}

func int16ToBytes(s []int16) []byte {
	out := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(out[i*2:i*2+2], uint16(v))
	}
	return out
}
