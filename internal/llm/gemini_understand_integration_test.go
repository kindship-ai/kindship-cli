package llm

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestUnderstandAudio_RealAPI — opt-in integration test.
//
// Skipped unless $KINDSHIP_GEMINI_API_KEY is set, mirroring the
// gemini_live_integration_test.go pattern. Also needs a local WAV
// file — default path is /tmp/replicate-align-probe/02-swedish-
// short.wav (produced by apps/web/scripts/replicate-align-probe.ts
// during Phase 0 of the plan) so the test reuses existing fixture
// rather than shipping one in the repo. Override via
// $KINDSHIP_TEST_WAV_PATH.
//
// Validates the round-trip shape, not alignment accuracy —
// accuracy is owned by the end-to-end smoke elsewhere in the plan.
// Here we assert: request succeeds, response parses against the
// sentence schema, sentences are monotonic, last-end is within
// audio duration + generous slack.
func TestUnderstandAudio_RealAPI(t *testing.T) {
	apiKey := os.Getenv("KINDSHIP_GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("KINDSHIP_GEMINI_API_KEY not set — skipping real-API test")
	}
	wavPath := os.Getenv("KINDSHIP_TEST_WAV_PATH")
	if wavPath == "" {
		wavPath = "/tmp/replicate-align-probe/02-swedish-short.wav"
	}
	wav, err := os.ReadFile(wavPath)
	if err != nil {
		t.Skipf("no fixture WAV at %s (%v) — skipping; run Phase 0 probe or set KINDSHIP_TEST_WAV_PATH", wavPath, err)
	}

	sentenceSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"sentences": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"start_ms": map[string]any{"type": "integer"},
						"end_ms":   map[string]any{"type": "integer"},
						"text":     map[string]any{"type": "string"},
					},
					"required": []string{"start_ms", "end_ms", "text"},
				},
			},
		},
		"required": []string{"sentences"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	prompt := "Transcribe the audio into a JSON object matching the schema. For each sentence, provide the exact millisecond start and end offsets it spans in the audio, plus the sentence text. Include every sentence from the beginning to the end of the audio."

	got, err := UnderstandAudio(
		ctx, apiKey, "gemini-2.5-flash",
		wav, "audio/wav",
		prompt, sentenceSchema,
	)
	if err != nil {
		t.Fatalf("UnderstandAudio error: %v", err)
	}

	var payload struct {
		Sentences []struct {
			StartMS int    `json:"start_ms"`
			EndMS   int    `json:"end_ms"`
			Text    string `json:"text"`
		} `json:"sentences"`
	}
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("response not parseable against sentence schema: %v\n%q", err, got)
	}
	if len(payload.Sentences) == 0 {
		t.Fatal("expected at least one sentence; got empty array")
	}
	for i, s := range payload.Sentences {
		if s.EndMS < s.StartMS {
			t.Errorf("sentence %d end_ms (%d) before start_ms (%d)", i, s.EndMS, s.StartMS)
		}
		if s.Text == "" {
			t.Errorf("sentence %d text empty", i)
		}
		if i > 0 && s.StartMS < payload.Sentences[i-1].StartMS {
			t.Errorf("sentence %d non-monotonic: starts at %d after %d", i, s.StartMS, payload.Sentences[i-1].StartMS)
		}
	}

	audioMs := int64(len(wav)-44) * 1000 / (24000 * 2) // 24kHz mono 16-bit
	lastEnd := int64(payload.Sentences[len(payload.Sentences)-1].EndMS)
	if lastEnd > audioMs+2000 {
		t.Errorf("last sentence end %d ms beyond audio duration %d ms (slack 2s)", lastEnd, audioMs)
	}
	t.Logf("ok — %d sentences, last end %d ms vs audio ~%d ms", len(payload.Sentences), lastEnd, audioMs)
}
