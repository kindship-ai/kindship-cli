package voice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
)

// LoudnormTarget captures the EBU R128 loudness target applied to each
// chunk before concatenation. Values chosen to match typical podcast
// mastering:
//
//   - Integrated loudness (I) at -16 LUFS — the standard for streaming
//     audio; loud enough to be intelligible on phone speakers, quiet
//     enough to preserve dynamic range.
//   - True peak (TP) at -1.5 dBTP — safety margin against inter-sample
//     peaks on codec conversion.
//   - Loudness range (LRA) at 11 LU — allows natural conversational
//     dynamics without compressing expressiveness out.
type LoudnormTarget struct {
	IntegratedLUFS float64
	TruePeakDBTP   float64
	LoudnessRange  float64
}

// DefaultLoudnormTarget matches the EBU R128 podcast-mastering defaults
// used by the chunked render loop in cmd.runVoiceMulti. Change here if
// we pin a different target globally.
var DefaultLoudnormTarget = LoudnormTarget{
	IntegratedLUFS: -16,
	TruePeakDBTP:   -1.5,
	LoudnessRange:  11,
}

// ErrFFmpegMissing surfaces when the local environment lacks ffmpeg on
// PATH — on agent containers this is a Dockerfile regression; locally
// it means `brew install ffmpeg` is the fix.
var ErrFFmpegMissing = errors.New("ffmpeg binary not found on PATH — agent containers should install it via infra/agent-container/Dockerfile; run `brew install ffmpeg` for local dev")

// NormalizePCM pipes raw 16-bit signed little-endian mono PCM through
// ffmpeg's `loudnorm` filter at the given target. Input and output are
// both PCM at sampleRate Hz — no WAV header is added. Used per-chunk
// before concatenation so boundary loudness mismatches don't surface
// as audible seams in the final WAV.
//
// ffmpeg invocation pattern:
//
//	ffmpeg -hide_banner -loglevel error \
//	  -f s16le -ar <rate> -ac 1 -i - \
//	  -af "loudnorm=I=<I>:TP=<TP>:LRA=<LRA>" \
//	  -f s16le -ar <rate> -ac 1 -
//
// The single-pass loudnorm is not as accurate as the two-pass form
// ffmpeg's docs recommend for file-based content, but within a short
// (~90s) chunk the single-pass numbers are stable enough and it's much
// cheaper. If the chunks ever grow past ~3 minutes, revisit.
func NormalizePCM(ctx context.Context, pcm []byte, sampleRate int, target LoudnormTarget) ([]byte, error) {
	if len(pcm) == 0 {
		return nil, fmt.Errorf("NormalizePCM: pcm is empty")
	}
	if sampleRate <= 0 {
		return nil, fmt.Errorf("NormalizePCM: sampleRate must be > 0, got %d", sampleRate)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, ErrFFmpegMissing
	}

	filter := fmt.Sprintf("loudnorm=I=%s:TP=%s:LRA=%s",
		trimFloat(target.IntegratedLUFS),
		trimFloat(target.TruePeakDBTP),
		trimFloat(target.LoudnessRange),
	)

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-f", "s16le", "-ar", strconv.Itoa(sampleRate), "-ac", "1", "-i", "-",
		"-af", filter,
		"-f", "s16le", "-ar", strconv.Itoa(sampleRate), "-ac", "1", "-",
	)
	cmd.Stdin = bytes.NewReader(pcm)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg loudnorm failed: %w (stderr: %s)", err, truncate(stderr.String(), 400))
	}
	if stdout.Len() == 0 {
		return nil, fmt.Errorf("ffmpeg loudnorm produced no output (stderr: %s)", truncate(stderr.String(), 400))
	}
	return stdout.Bytes(), nil
}

// trimFloat formats a float without a trailing ".0" for cleaner ffmpeg
// filter strings ("-16" reads better in stderr logs than "-16.0").
func trimFloat(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
