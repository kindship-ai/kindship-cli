// Package llm holds Go clients for the LLM providers the kindship CLI
// talks to directly — LiteLLM (Anthropic-compatible) + Gemini.
//
// These clients do NOT go through the kindship-vercel API. The voice
// and strategy commands fetch a LiteLLM virtual key via the short,
// CF-safe secrets endpoint, then call LiteLLM on the agent container's
// own Docker network. That keeps long Opus calls off the path that
// times out at 100s (Cloudflare).
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AnthropicRequest is a subset of the Anthropic messages API body. We
// only expose what the voice + strategy commands actually need.
type AnthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []AnthropicMessage `json:"messages"`
	Thinking    *AnthropicThinking `json:"thinking,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	// OutputConfig is an Opus-4.6 parameter biasing toward longer,
	// higher-quality output. The web-side strategy generator always
	// sets `{ effort: "high" }` (see strategy-generation.server.ts),
	// and the Phase 0 probes pin the same shape. Omitting it would
	// silently downgrade CLI-generated output vs. worker-generated.
	OutputConfig *AnthropicOutputConfig `json:"output_config,omitempty"`
}

// AnthropicOutputConfig holds the `output_config` request field.
type AnthropicOutputConfig struct {
	// Effort is typically "high" or "medium". Passing an invalid
	// value surfaces as a 400 from LiteLLM/Anthropic rather than a
	// silent no-op.
	Effort string `json:"effort"`
}

// AnthropicMessage is one turn in the conversation.
type AnthropicMessage struct {
	Role    string              `json:"role"`
	Content []AnthropicContent  `json:"content"`
}

// AnthropicContent is a single content block. For user input this is
// always a text block. Assistant responses may contain thinking blocks
// before text blocks when `thinking` is enabled.
type AnthropicContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

// AnthropicThinking opts into extended thinking. `budget_tokens` caps
// the internal deliberation.
type AnthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// AnthropicUsage is the token-accounting echo Anthropic (and LiteLLM)
// include on the final message.
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// AnthropicResponse is the assembled final message.
type AnthropicResponse struct {
	ID         string             `json:"id"`
	Model      string             `json:"model"`
	Role       string             `json:"role"`
	Content    []AnthropicContent `json:"content"`
	StopReason string             `json:"stop_reason"`
	Usage      AnthropicUsage     `json:"usage"`
}

// TextOutput returns the concatenated `text` blocks. Thinking blocks
// are skipped — callers that want them can walk Content directly.
func (r *AnthropicResponse) TextOutput() string {
	var b strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// CallAnthropicStreaming POSTs to /anthropic/v1/messages?stream=true on
// the LiteLLM proxy, consumes the SSE stream, and returns the assembled
// final response.
//
// Streaming is required for Opus 4.6 + 16k thinking + 50k max_tokens
// because Anthropic's own non-stream endpoint has a ~10-minute wall-
// clock ceiling that these configs brush against. LiteLLM passes the
// SSE through.
//
// The per-request timeout is deliberately generous (10 minutes). If a
// real request stalls that long, something is wrong upstream — failing
// loudly is preferable to hanging a CLI invocation indefinitely.
func CallAnthropicStreaming(
	ctx context.Context,
	baseURL, apiKey string,
	req AnthropicRequest,
) (*AnthropicResponse, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("CallAnthropicStreaming: baseURL is empty")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("CallAnthropicStreaming: apiKey is empty")
	}

	// LiteLLM exposes two Anthropic-compatible endpoints:
	//   /v1/messages           — experimental pass-through, forwards
	//                            the client's x-api-key to Anthropic.
	//                            A LiteLLM virtual key will be rejected.
	//   /anthropic/v1/messages — router-aware, virtual key authenticates
	//                            AT LiteLLM which then uses its own
	//                            server-side ANTHROPIC_API_KEY upstream.
	// Only the second one works with our per-agent virtual keys.
	url := strings.TrimRight(baseURL, "/") + "/anthropic/v1/messages"

	body := map[string]any{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
		"stream":     true,
		"messages":   req.Messages,
	}
	if req.System != "" {
		body["system"] = req.System
	}
	if req.Thinking != nil {
		body["thinking"] = req.Thinking
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.OutputConfig != nil {
		body["output_config"] = req.OutputConfig
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, url, bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("x-api-key", apiKey)

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		preview := string(errBody)
		if len(preview) > 400 {
			preview = preview[:400]
		}
		return nil, fmt.Errorf(
			"LiteLLM %s: %d %s: %s",
			url, resp.StatusCode, resp.Status, preview,
		)
	}

	return readAnthropicStream(resp.Body)
}

// readAnthropicStream consumes Anthropic SSE events off r and returns
// the final assembled message. Event shape per
// https://docs.anthropic.com/en/api/messages-streaming:
//
//   message_start          → { message: { id, role, model, usage } }
//   content_block_start    → { index, content_block: { type, … } }
//   content_block_delta    → { index, delta: { type, text | thinking, … } }
//   content_block_stop     → { index }
//   message_delta          → { delta: { stop_reason, … }, usage }
//   message_stop
//   error                  → { error: { type, message } }  (mid-stream)
//
// We don't care about per-token deltas for final-message semantics,
// only the accumulated content and the terminating stop_reason / usage.
//
// Two ways the stream can fail that are NOT visible as HTTP status
// (because headers flushed at 200 before the failure):
//   1. EOF before message_stop — upstream hung up mid-response.
//   2. `event: error` with a JSON error payload mid-stream — LiteLLM
//      or Anthropic surfaces a failure after the response began.
// Both must produce an error, not a silently-assembled partial result.
func readAnthropicStream(r io.Reader) (*AnthropicResponse, error) {
	scanner := bufio.NewScanner(r)
	// Some LiteLLM deployments emit large SSE payloads (thinking blocks,
	// big content chunks); grow the buffer so bufio.Scanner doesn't
	// crash with ErrTooLong on a long line.
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	var out AnthropicResponse
	blocks := map[int]*AnthropicContent{}
	sawMessageStop := false

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}

		var env struct {
			Type         string          `json:"type"`
			Index        int             `json:"index"`
			Message      json.RawMessage `json:"message"`
			ContentBlock json.RawMessage `json:"content_block"`
			Delta        json.RawMessage `json:"delta"`
			Usage        *AnthropicUsage `json:"usage"`
			Error        *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &env); err != nil {
			return nil, fmt.Errorf("parse SSE event: %w: %s", err, preview(payload))
		}

		switch env.Type {
		case "message_start":
			var m struct {
				Message struct {
					ID    string         `json:"id"`
					Role  string         `json:"role"`
					Model string         `json:"model"`
					Usage AnthropicUsage `json:"usage"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(payload), &m); err != nil {
				return nil, fmt.Errorf("parse message_start: %w", err)
			}
			out.ID = m.Message.ID
			out.Role = m.Message.Role
			out.Model = m.Message.Model
			out.Usage = m.Message.Usage

		case "content_block_start":
			var cb AnthropicContent
			if err := json.Unmarshal(env.ContentBlock, &cb); err != nil {
				return nil, fmt.Errorf("parse content_block_start: %w", err)
			}
			blocks[env.Index] = &AnthropicContent{
				Type:     cb.Type,
				Text:     cb.Text,
				Thinking: cb.Thinking,
			}

		case "content_block_delta":
			var d struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
			}
			if err := json.Unmarshal(env.Delta, &d); err != nil {
				return nil, fmt.Errorf("parse content_block_delta: %w", err)
			}
			cb, ok := blocks[env.Index]
			if !ok {
				cb = &AnthropicContent{}
				blocks[env.Index] = cb
			}
			switch d.Type {
			case "text_delta":
				if cb.Type == "" {
					cb.Type = "text"
				}
				cb.Text += d.Text
			case "thinking_delta":
				if cb.Type == "" {
					cb.Type = "thinking"
				}
				cb.Thinking += d.Thinking
			}

		case "message_delta":
			var d struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage *AnthropicUsage `json:"usage"`
			}
			if err := json.Unmarshal([]byte(payload), &d); err != nil {
				return nil, fmt.Errorf("parse message_delta: %w", err)
			}
			if d.Delta.StopReason != "" {
				out.StopReason = d.Delta.StopReason
			}
			if d.Usage != nil {
				// Anthropic sends the running output-token count here.
				out.Usage.OutputTokens = d.Usage.OutputTokens
			}

		case "message_stop":
			sawMessageStop = true

		case "error":
			// Mid-stream error surfaced by Anthropic/LiteLLM after
			// HTTP 200 headers already went out. Must not be swallowed.
			if env.Error != nil {
				return nil, fmt.Errorf(
					"anthropic SSE error event: type=%q message=%q",
					env.Error.Type, env.Error.Message,
				)
			}
			return nil, fmt.Errorf("anthropic SSE error event (no payload): %s", preview(payload))

		case "content_block_stop", "ping":
			// `ping` is a keepalive; content_block_stop doesn't add
			// anything to the assembly (deltas already accumulated).
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read SSE stream: %w", err)
	}

	if !sawMessageStop {
		// Stream hung up before the terminator — don't hand back a
		// half-assembled message as if it were a final response. This
		// matches the @anthropic-ai/sdk contract: messages.stream()
		// .finalMessage() throws when the stream aborts mid-response.
		return nil, fmt.Errorf(
			"anthropic stream truncated: no message_stop event before EOF (stop_reason=%q, %d content blocks, %d output tokens)",
			out.StopReason, len(blocks), out.Usage.OutputTokens,
		)
	}

	// Ordered assembly: the server emits blocks in ascending index.
	maxIdx := -1
	for i := range blocks {
		if i > maxIdx {
			maxIdx = i
		}
	}
	for i := 0; i <= maxIdx; i++ {
		if cb := blocks[i]; cb != nil {
			out.Content = append(out.Content, *cb)
		}
	}

	if out.ID == "" && len(out.Content) == 0 {
		return nil, fmt.Errorf("empty SSE stream (no message_start, no content)")
	}
	return &out, nil
}

func preview(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
