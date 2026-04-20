package llm

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestSingleSpeakerLive_RealAPI is a go-build-tag-less integration
// test: skips unless $KINDSHIP_GEMINI_API_KEY is set. That matches
// the pattern the rest of the CLI uses — tests never implicitly
// burn API budget; only when the operator explicitly opts in.
//
// Run (from kindship-cli-repo/):
//
//	KINDSHIP_GEMINI_API_KEY=... go test -v -run TestSingleSpeakerLive_RealAPI ./internal/llm/
func TestSingleSpeakerLive_RealAPI(t *testing.T) {
	apiKey := os.Getenv("KINDSHIP_GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("KINDSHIP_GEMINI_API_KEY not set — skipping live-API integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pcm, err := SingleSpeakerLive(
		ctx, apiKey, "Kore",
		"Read this in your natural speaking voice: The quiet moment between the question and the answer is where the craft lives.",
	)
	if err != nil {
		t.Fatalf("SingleSpeakerLive error: %v", err)
	}
	// Probe 06 produced ~314KB of PCM for this exact line; anything
	// much smaller than 50KB means we got a silent close / truncated
	// stream and shouldn't count as a pass.
	if len(pcm) < 50_000 {
		t.Fatalf("expected > 50KB PCM, got %d bytes (turnComplete without audio?)", len(pcm))
	}
	t.Logf("ok — %d bytes of PCM from Gemini Live", len(pcm))
}
