package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Voice renders with Gemini. Two modes:
//
//   - Live (WebSocket, audio-out): used for single-speaker monologue —
//     cheaper ($0.018/min output vs $0.03/min for TTS flash) and picks
//     up style tags like "[ancient egyptian alchemist]" baked into
//     the text prompt.
//
//   - Multi-speaker TTS (REST): used for two-voice podcasts — the Live
//     API doesn't accept multiple speakerVoiceConfigs.
//
// Both return raw 16-bit mono PCM at 24kHz, which the caller wraps in
// a RIFF/WAV header before writing to disk.

const geminiSampleRate = 24000

// Models are overridable via env so smoke tests and probes can pin a
// specific preview tag without a code edit.
func ttsModel() string {
	if v := os.Getenv("KINDSHIP_GEMINI_TTS_MODEL"); v != "" {
		return v
	}
	return "gemini-3.1-flash-tts-preview"
}

func liveModel() string {
	if v := os.Getenv("KINDSHIP_GEMINI_LIVE_MODEL"); v != "" {
		return v
	}
	return "models/gemini-3.1-flash-live-preview"
}

// SingleSpeakerLive renders `text` with Gemini Live API, voiceName
// speaking, and returns raw PCM bytes. `stylePrefix` is prepended to
// the user turn — pass an empty string to render the text bare.
//
// Style tags like "[ancient egyptian alchemist]" land in the text
// prompt itself. The underscore-prefix "Read this in your natural
// speaking voice:" prefix that the Live probe used is NOT added here
// — the caller owns phrasing.
func SingleSpeakerLive(
	ctx context.Context,
	apiKey, voiceName, text string,
) ([]byte, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("SingleSpeakerLive: apiKey is empty")
	}
	if voiceName == "" {
		return nil, fmt.Errorf("SingleSpeakerLive: voiceName is empty")
	}
	if text == "" {
		return nil, fmt.Errorf("SingleSpeakerLive: text is empty")
	}

	wsURL := fmt.Sprintf(
		"wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent?key=%s",
		url.QueryEscape(apiKey),
	)

	// WebSocket has its own handshake timeout; the outer ctx caps total
	// wall-clock. Live renders of ~3-minute monologues finish well
	// inside 2 minutes, so 5 minutes is pathologically generous.
	wsCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// The official @google/genai SDK sets these headers on the WS
	// upgrade. Without them, some Gemini Live preview models accept
	// the setup frame and then silently ignore input frames —
	// looks identical to a timeout. `x-goog-api-client` especially
	// is used server-side for feature-flag routing; missing it trips
	// a gate on 3.1-flash-live-preview. Verified by spying on the
	// SDK's wire format (see scripts/gemini-voice-eval/06-live-sdk).
	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"User-Agent":        []string{"kindship-cli/1.0 google-genai-compat"},
			"X-Goog-Api-Client": []string{"kindship-cli/1.0 google-genai-compat"},
		},
		// The Node `ws` library (used by @google/genai) negotiates
		// permessage-deflate by default; coder/websocket defaults to
		// CompressionDisabled. Some Gemini Live preview models appear
		// to require the compression extension to route audio — match
		// the SDK defaults to be safe.
		CompressionMode: websocket.CompressionNoContextTakeover,
	}
	conn, resp, err := websocket.Dial(wsCtx, wsURL, dialOpts)
	if err != nil {
		return nil, fmt.Errorf("gemini live dial: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	if os.Getenv("KINDSHIP_GEMINI_LIVE_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[live] dial ok — status=%d protocol=%q\n", resp.StatusCode, resp.Header.Get("Sec-WebSocket-Protocol"))
	}

	setup := map[string]any{
		"setup": map[string]any{
			"model": liveModel(),
			"generationConfig": map[string]any{
				"responseModalities": []string{"AUDIO"},
				"speechConfig": map[string]any{
					"voiceConfig": map[string]any{
						"prebuiltVoiceConfig": map[string]any{
							"voiceName": voiceName,
						},
					},
				},
			},
		},
	}
	if err := writeJSON(wsCtx, conn, setup); err != nil {
		return nil, fmt.Errorf("send setup: %w", err)
	}
	if os.Getenv("KINDSHIP_GEMINI_LIVE_DEBUG") != "" {
		b, _ := json.Marshal(setup)
		fmt.Fprintf(os.Stderr, "[live] sent setup — %d bytes\n", len(b))
	}

	var chunks []byte
	// Gemini 3.1-flash-live-preview silently ignores the legacy
	// `clientContent` envelope and only acts on `realtimeInput` with
	// a plain `text` field. The @google/genai SDK's sendRealtimeInput
	// serializes to the same shape (see index.mjs
	// liveSendRealtimeInputParametersToMldev). Verified against
	// scripts/gemini-voice-eval/06-live-sdk.ts — the SDK emits
	// turnComplete without needing an explicit turnComplete flag.
	turnClient := map[string]any{
		"realtimeInput": map[string]any{
			"text": text,
		},
	}

	// State machine: wait for setupComplete, send realtimeInput, then
	// accumulate audio until turnComplete. Anything else is a signal
	// to keep reading.
	debug := os.Getenv("KINDSHIP_GEMINI_LIVE_DEBUG") != ""
	for {
		typ, payload, err := conn.Read(wsCtx)
		if err != nil {
			if debug {
				fmt.Fprintf(os.Stderr, "[live] read err — %v (chunks=%d)\n", err, len(chunks))
			}
			if len(chunks) == 0 {
				return nil, fmt.Errorf("gemini live read: %w", err)
			}
			return chunks, fmt.Errorf("gemini live read (partial %d bytes): %w", len(chunks), err)
		}
		if debug {
			p := string(payload)
			if len(p) > 200 {
				p = p[:200] + "…"
			}
			fmt.Fprintf(os.Stderr, "[live] frame type=%d len=%d: %s\n", typ, len(payload), p)
		}
		// Gemini Live sends JSON envelopes as BINARY frames
		// (MessageBinary), not text — so we can't filter on
		// MessageText. Both types carry the same UTF-8 JSON body.
		// We only skip unrecognized types, which shouldn't occur.
		_ = typ

		var env struct {
			SetupComplete map[string]any `json:"setupComplete"`
			ServerContent *struct {
				ModelTurn *struct {
					Parts []struct {
						InlineData *struct {
							MimeType string `json:"mimeType"`
							Data     string `json:"data"`
						} `json:"inlineData"`
					} `json:"parts"`
				} `json:"modelTurn"`
				TurnComplete       bool `json:"turnComplete"`
				GenerationComplete bool `json:"generationComplete"`
			} `json:"serverContent"`
			ErrorDetails json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(payload, &env); err != nil {
			return nil, fmt.Errorf("parse live frame: %w", err)
		}
		if env.ErrorDetails != nil {
			return nil, fmt.Errorf("gemini live error frame: %s", string(env.ErrorDetails))
		}

		if env.SetupComplete != nil {
			if err := writeJSON(wsCtx, conn, turnClient); err != nil {
				return nil, fmt.Errorf("send realtimeInput: %w", err)
			}
			continue
		}

		if env.ServerContent != nil && env.ServerContent.ModelTurn != nil {
			for _, p := range env.ServerContent.ModelTurn.Parts {
				if p.InlineData == nil || p.InlineData.Data == "" {
					continue
				}
				raw, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
				if err != nil {
					return nil, fmt.Errorf("decode audio part: %w", err)
				}
				chunks = append(chunks, raw...)
			}
		}

		if env.ServerContent != nil && env.ServerContent.TurnComplete {
			return chunks, nil
		}
	}
}

func writeJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	buf, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return conn.Write(ctx, websocket.MessageText, buf)
}

// Speaker is one voice in a multi-speaker TTS generation.
type Speaker struct {
	// Role label that must appear in the `text` prompt as `Role: line`.
	// Example: `Speaker: "Host"` and text `Host: Welcome back. …`.
	Speaker string
	// Prebuilt Gemini voice name, e.g. "Kore", "Puck".
	VoiceName string
}

// MultiSpeakerTTS renders a two-voice dialog via the REST TTS endpoint.
// `text` must use "<Speaker>: <line>" lines matching `speakers[].Speaker`.
// Returns raw PCM bytes at 24kHz mono.
func MultiSpeakerTTS(
	ctx context.Context,
	apiKey, text string,
	speakers []Speaker,
) ([]byte, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("MultiSpeakerTTS: apiKey is empty")
	}
	if text == "" {
		return nil, fmt.Errorf("MultiSpeakerTTS: text is empty")
	}
	if len(speakers) < 2 {
		return nil, fmt.Errorf("MultiSpeakerTTS: needs >= 2 speakers, got %d", len(speakers))
	}

	voiceConfigs := make([]map[string]any, 0, len(speakers))
	for _, s := range speakers {
		if s.Speaker == "" || s.VoiceName == "" {
			return nil, fmt.Errorf("MultiSpeakerTTS: speaker %+v has empty field", s)
		}
		voiceConfigs = append(voiceConfigs, map[string]any{
			"speaker": s.Speaker,
			"voiceConfig": map[string]any{
				"prebuiltVoiceConfig": map[string]any{
					"voiceName": s.VoiceName,
				},
			},
		})
	}

	body := map[string]any{
		"contents": []any{map[string]any{
			"parts": []any{map[string]any{"text": text}},
		}},
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig": map[string]any{
				"multiSpeakerVoiceConfig": map[string]any{
					"speakerVoiceConfigs": voiceConfigs,
				},
			},
		},
	}

	endpoint := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		url.PathEscape(ttsModel()),
		url.QueryEscape(apiKey),
	)
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", strings.TrimSuffix(endpoint, "?key="+url.QueryEscape(apiKey)), err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		p := string(respBody)
		if len(p) > 400 {
			p = p[:400]
		}
		return nil, fmt.Errorf("gemini TTS %d: %s", resp.StatusCode, p)
	}

	var parsed struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData *struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	var audio []byte
	for _, cand := range parsed.Candidates {
		for _, part := range cand.Content.Parts {
			if part.InlineData == nil || part.InlineData.Data == "" {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
			if err != nil {
				return nil, fmt.Errorf("decode audio data: %w", err)
			}
			audio = append(audio, raw...)
		}
	}
	if len(audio) == 0 {
		return nil, fmt.Errorf("gemini TTS returned no audio: %s", preview(string(respBody)))
	}
	return audio, nil
}

// PCMToWAV wraps 16-bit mono PCM at the Gemini native 24kHz in a
// minimal RIFF/WAV header so output files play in any media app.
// Pass sampleRate=0 to use the default (24000).
func PCMToWAV(pcm []byte, sampleRate int) []byte {
	if sampleRate <= 0 {
		sampleRate = geminiSampleRate
	}
	var (
		channels      uint16 = 1
		bitsPerSample uint16 = 16
	)
	byteRate := uint32(sampleRate) * uint32(channels) * uint32(bitsPerSample) / 8
	blockAlign := channels * bitsPerSample / 8

	buf := bytes.NewBuffer(make([]byte, 0, 44+len(pcm)))
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(36+len(pcm)))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, channels)
	_ = binary.Write(buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(buf, binary.LittleEndian, byteRate)
	_ = binary.Write(buf, binary.LittleEndian, blockAlign)
	_ = binary.Write(buf, binary.LittleEndian, bitsPerSample)
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, uint32(len(pcm)))
	buf.Write(pcm)
	return buf.Bytes()
}
