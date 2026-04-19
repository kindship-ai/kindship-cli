package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// streamedEvents spits a canned Anthropic SSE stream at the client and
// flushes between frames so the scanner-side code has to behave like
// it's receiving data incrementally, not one fat payload.
type streamedEvent struct {
	event string
	data  any
}

func writeSSEStream(t *testing.T, w http.ResponseWriter, events []streamedEvent) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, ok := w.(http.Flusher)
	if !ok {
		t.Fatalf("ResponseWriter does not support flushing")
	}
	for _, e := range events {
		payload, err := json.Marshal(e.data)
		if err != nil {
			t.Fatalf("marshal SSE event: %v", err)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.event, payload)
		flusher.Flush()
	}
}

func TestCallAnthropicStreaming_AssemblesTextAndThinking(t *testing.T) {
	var (
		gotAuthHeader string
		gotPath       string
		gotStream     bool
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("x-api-key")
		gotPath = r.URL.Path

		var body struct {
			Stream   bool   `json:"stream"`
			Model    string `json:"model"`
			Messages []any  `json:"messages"`
		}
		dec := json.NewDecoder(bufio.NewReader(r.Body))
		if err := dec.Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotStream = body.Stream

		writeSSEStream(t, w, []streamedEvent{
			{event: "message_start", data: map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id":    "msg_fake_1",
					"role":  "assistant",
					"model": "claude-opus-4-6",
					"usage": map[string]any{"input_tokens": 42, "output_tokens": 0},
				},
			}},
			// Thinking block arrives before text — matches production
			// shape when `thinking` is enabled (probe 4 validated this).
			{event: "content_block_start", data: map[string]any{
				"type":          "content_block_start",
				"index":         0,
				"content_block": map[string]any{"type": "thinking", "thinking": ""},
			}},
			{event: "content_block_delta", data: map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{"type": "thinking_delta", "thinking": "Let me "},
			}},
			{event: "content_block_delta", data: map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{"type": "thinking_delta", "thinking": "think about this."},
			}},
			{event: "content_block_stop", data: map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			}},
			{event: "content_block_start", data: map[string]any{
				"type":          "content_block_start",
				"index":         1,
				"content_block": map[string]any{"type": "text", "text": ""},
			}},
			{event: "content_block_delta", data: map[string]any{
				"type":  "content_block_delta",
				"index": 1,
				"delta": map[string]any{"type": "text_delta", "text": "Hello, "},
			}},
			{event: "content_block_delta", data: map[string]any{
				"type":  "content_block_delta",
				"index": 1,
				"delta": map[string]any{"type": "text_delta", "text": "world."},
			}},
			{event: "content_block_stop", data: map[string]any{
				"type":  "content_block_stop",
				"index": 1,
			}},
			{event: "message_delta", data: map[string]any{
				"type":  "message_delta",
				"delta": map[string]any{"stop_reason": "end_turn"},
				"usage": map[string]any{"output_tokens": 7},
			}},
			{event: "message_stop", data: map[string]any{"type": "message_stop"}},
		})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := CallAnthropicStreaming(ctx, srv.URL, "sk-test-1234", AnthropicRequest{
		Model:     "claude-opus-4-6",
		MaxTokens: 512,
		Messages: []AnthropicMessage{
			{Role: "user", Content: []AnthropicContent{{Type: "text", Text: "hi"}}},
		},
		Thinking: &AnthropicThinking{Type: "enabled", BudgetTokens: 1024},
	})
	if err != nil {
		t.Fatalf("CallAnthropicStreaming error: %v", err)
	}

	if !gotStream {
		t.Errorf("expected stream=true in request body")
	}
	if gotAuthHeader != "sk-test-1234" {
		t.Errorf("x-api-key not forwarded, got %q", gotAuthHeader)
	}
	if gotPath != "/anthropic/v1/messages" {
		t.Errorf("wrong path — must hit router-aware endpoint, got %q", gotPath)
	}

	if got := resp.TextOutput(); got != "Hello, world." {
		t.Errorf("TextOutput = %q, want %q", got, "Hello, world.")
	}

	// Assert thinking block appears before text block (probe-4 invariant).
	if len(resp.Content) < 2 {
		t.Fatalf("expected >= 2 content blocks, got %d", len(resp.Content))
	}
	if resp.Content[0].Type != "thinking" {
		t.Errorf("first block should be thinking, got %q", resp.Content[0].Type)
	}
	if resp.Content[0].Thinking != "Let me think about this." {
		t.Errorf("thinking accumulation wrong: %q", resp.Content[0].Thinking)
	}
	if resp.Content[1].Type != "text" {
		t.Errorf("second block should be text, got %q", resp.Content[1].Type)
	}

	if resp.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q", resp.StopReason)
	}
	if resp.Usage.InputTokens != 42 || resp.Usage.OutputTokens != 7 {
		t.Errorf("usage wrong: %+v", resp.Usage)
	}
}

func TestCallAnthropicStreaming_BubblesUpHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": "invalid x-api-key"}`))
	}))
	defer srv.Close()

	_, err := CallAnthropicStreaming(context.Background(), srv.URL, "sk-bad", AnthropicRequest{
		Model:     "claude-opus-4-6",
		MaxTokens: 16,
		Messages: []AnthropicMessage{
			{Role: "user", Content: []AnthropicContent{{Type: "text", Text: "hi"}}},
		},
	})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401, got: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Errorf("error should include response body preview, got: %v", err)
	}
}

func TestCallAnthropicStreaming_TruncatedStreamErrors(t *testing.T) {
	// Server starts the stream, emits message_start + some content,
	// then hangs up BEFORE message_stop. The client must treat this
	// as a failure — returning a partial assembly would let a
	// truncated script silently ship to Gemini.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSEStream(t, w, []streamedEvent{
			{event: "message_start", data: map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_trunc", "role": "assistant", "model": "claude-opus-4-6",
					"usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
				},
			}},
			{event: "content_block_start", data: map[string]any{
				"type": "content_block_start", "index": 0,
				"content_block": map[string]any{"type": "text", "text": ""},
			}},
			{event: "content_block_delta", data: map[string]any{
				"type": "content_block_delta", "index": 0,
				"delta": map[string]any{"type": "text_delta", "text": "half a "},
			}},
			// no message_stop — connection just ends
		})
	}))
	defer srv.Close()

	_, err := CallAnthropicStreaming(context.Background(), srv.URL, "sk-x", AnthropicRequest{
		Model: "claude-opus-4-6", MaxTokens: 16,
		Messages: []AnthropicMessage{
			{Role: "user", Content: []AnthropicContent{{Type: "text", Text: "hi"}}},
		},
	})
	if err == nil {
		t.Fatal("expected truncation error")
	}
	if !strings.Contains(err.Error(), "truncated") &&
		!strings.Contains(err.Error(), "message_stop") {
		t.Errorf("error should mention truncation, got: %v", err)
	}
}

func TestCallAnthropicStreaming_MidStreamErrorEventErrors(t *testing.T) {
	// HTTP 200 already went out, then LiteLLM/Anthropic surfaces an
	// error mid-stream. Must bubble up — not be swallowed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSEStream(t, w, []streamedEvent{
			{event: "message_start", data: map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_err", "role": "assistant", "model": "claude-opus-4-6",
					"usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
				},
			}},
			{event: "error", data: map[string]any{
				"type": "error",
				"error": map[string]any{
					"type":    "overloaded_error",
					"message": "server briefly overloaded",
				},
			}},
		})
	}))
	defer srv.Close()

	_, err := CallAnthropicStreaming(context.Background(), srv.URL, "sk-x", AnthropicRequest{
		Model: "claude-opus-4-6", MaxTokens: 16,
		Messages: []AnthropicMessage{
			{Role: "user", Content: []AnthropicContent{{Type: "text", Text: "hi"}}},
		},
	})
	if err == nil {
		t.Fatal("expected mid-stream error to bubble up")
	}
	if !strings.Contains(err.Error(), "overloaded") {
		t.Errorf("error should surface server-side message, got: %v", err)
	}
}

func TestCallAnthropicStreaming_ValidatesInputs(t *testing.T) {
	ctx := context.Background()
	base := AnthropicRequest{
		Model: "claude-opus-4-6", MaxTokens: 16,
		Messages: []AnthropicMessage{
			{Role: "user", Content: []AnthropicContent{{Type: "text", Text: "hi"}}},
		},
	}
	if _, err := CallAnthropicStreaming(ctx, "", "k", base); err == nil {
		t.Error("expected error on empty baseURL")
	}
	if _, err := CallAnthropicStreaming(ctx, "http://x", "", base); err == nil {
		t.Error("expected error on empty apiKey")
	}
}
