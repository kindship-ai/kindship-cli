package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGeminiServer stands in for generativelanguage.googleapis.com for
// UnderstandAudio tests. Captures the last request's body + query so
// tests can assert what the client actually sent — schema forwarding,
// base64 audio, the right model in the URL path, etc.
type fakeGeminiServer struct {
	*httptest.Server
	lastMethod string
	lastPath   string
	lastQuery  string
	lastBody   []byte
}

func newFakeGeminiServer(
	t *testing.T,
	respond func(w http.ResponseWriter, r *http.Request, body []byte),
) *fakeGeminiServer {
	t.Helper()
	fg := &fakeGeminiServer{}
	fg.Server = httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			fg.lastMethod = r.Method
			fg.lastPath = r.URL.Path
			fg.lastQuery = r.URL.RawQuery
			fg.lastBody = body
			respond(w, r, body)
		}),
	)
	t.Cleanup(fg.Close)
	t.Setenv("KINDSHIP_GEMINI_REST_BASE_URL", fg.URL)
	return fg
}

// TestUnderstandAudio_PlainText — no schema; model returns prose.
func TestUnderstandAudio_PlainText(t *testing.T) {
	fg := newFakeGeminiServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		resp := map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{
							map[string]any{"text": "The voice describes a quiet moment between question and answer."},
						},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	got, err := UnderstandAudio(
		context.Background(),
		"sk-fake", "gemini-2.5-flash",
		[]byte("FAKEWAV"), "audio/wav",
		"Describe this audio briefly.",
		nil,
	)
	if err != nil {
		t.Fatalf("UnderstandAudio: %v", err)
	}
	if !strings.Contains(got, "quiet moment") {
		t.Errorf("response text not plumbed through: %q", got)
	}

	// Request shape sanity.
	if fg.lastMethod != http.MethodPost {
		t.Errorf("method = %q", fg.lastMethod)
	}
	if !strings.Contains(fg.lastPath, "gemini-2.5-flash:generateContent") {
		t.Errorf("path did not include model:generateContent, got %q", fg.lastPath)
	}
	if !strings.Contains(fg.lastQuery, "key=sk-fake") {
		t.Errorf("api key not in query string: %q", fg.lastQuery)
	}

	// Body should have audio inline (base64'd) and prompt text, and
	// should NOT include a generationConfig when no schema was passed.
	var body map[string]any
	if err := json.Unmarshal(fg.lastBody, &body); err != nil {
		t.Fatalf("parse request body: %v", err)
	}
	if _, ok := body["generationConfig"]; ok {
		t.Errorf("generationConfig should be absent when responseSchema is nil")
	}
	contents, ok := body["contents"].([]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("contents shape wrong: %#v", body["contents"])
	}
	parts := contents[0].(map[string]any)["parts"].([]any)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts (prompt + inline audio), got %d", len(parts))
	}
	if _, ok := parts[0].(map[string]any)["text"]; !ok {
		t.Errorf("first part should be text")
	}
	inline, ok := parts[1].(map[string]any)["inlineData"].(map[string]any)
	if !ok {
		t.Errorf("second part should be inlineData")
	}
	if inline["mimeType"] != "audio/wav" {
		t.Errorf("mimeType not plumbed: %v", inline["mimeType"])
	}
	if inline["data"] == "" {
		t.Errorf("base64 audio data empty")
	}
}

// TestUnderstandAudio_StructuredOutput — schema set; responseSchema +
// responseMimeType propagate into the request's generationConfig.
func TestUnderstandAudio_StructuredOutput(t *testing.T) {
	fg := newFakeGeminiServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		resp := map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{
							map[string]any{"text": `{"sentences":[{"start_ms":100,"end_ms":1800,"text":"Hi."}]}`},
						},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	schema := map[string]any{
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
				},
			},
		},
	}

	got, err := UnderstandAudio(
		context.Background(),
		"sk-fake", "gemini-2.5-pro",
		[]byte("FAKEWAV"), "audio/wav",
		"Transcribe with sentence-level timestamps.",
		schema,
	)
	if err != nil {
		t.Fatalf("UnderstandAudio: %v", err)
	}
	var payload struct {
		Sentences []struct {
			StartMS int    `json:"start_ms"`
			EndMS   int    `json:"end_ms"`
			Text    string `json:"text"`
		} `json:"sentences"`
	}
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("response not parseable JSON: %v: %q", err, got)
	}
	if len(payload.Sentences) != 1 || payload.Sentences[0].StartMS != 100 {
		t.Errorf("structured payload wrong: %+v", payload)
	}

	// Schema should flow through into the request body as
	// generationConfig.responseSchema + responseMimeType.
	var body map[string]any
	if err := json.Unmarshal(fg.lastBody, &body); err != nil {
		t.Fatalf("parse request body: %v", err)
	}
	gc, ok := body["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig missing when responseSchema was set: %v", body)
	}
	if gc["responseMimeType"] != "application/json" {
		t.Errorf("responseMimeType not set: %v", gc["responseMimeType"])
	}

	// Deep-equal the forwarded schema against the input. Catches a
	// regression that mutates, drops, or re-nests schema fields
	// somewhere in the marshal path — the "generationConfig has A
	// responseSchema key" check alone would miss that. Both sides
	// go through JSON round-trip so numeric types, map ordering, and
	// nil/empty distinctions normalize the same way.
	expected, _ := json.Marshal(schema)
	got2, _ := json.Marshal(gc["responseSchema"])
	if string(expected) != string(got2) {
		t.Errorf("responseSchema not round-tripped verbatim\n  want: %s\n  got:  %s", expected, got2)
	}
}

// TestUnderstandAudio_ErrorSurfacesWithPreview — 4xx / 5xx responses
// must include status + a body preview so the caller can debug.
func TestUnderstandAudio_ErrorSurfacesWithPreview(t *testing.T) {
	newFakeGeminiServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad audio mime"}}`))
	})

	_, err := UnderstandAudio(
		context.Background(),
		"sk-fake", "gemini-2.5-flash",
		[]byte("FAKEWAV"), "audio/wav",
		"x",
		nil,
	)
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should include status: %v", err)
	}
	if !strings.Contains(err.Error(), "bad audio mime") {
		t.Errorf("error should include body preview: %v", err)
	}
}

// TestUnderstandAudio_ValidatesInputs — empty required args error before
// any HTTP round-trip.
func TestUnderstandAudio_ValidatesInputs(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name                                             string
		apiKey, model, audioMime, prompt string
		audio                                            []byte
	}{
		{"no apiKey", "", "m", "audio/wav", "p", []byte("a")},
		{"no model", "k", "", "audio/wav", "p", []byte("a")},
		{"no audioMime", "k", "m", "", "p", []byte("a")},
		{"no prompt", "k", "m", "audio/wav", "", []byte("a")},
		{"empty audio", "k", "m", "audio/wav", "p", []byte{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := UnderstandAudio(
				ctx, tc.apiKey, tc.model,
				tc.audio, tc.audioMime,
				tc.prompt, nil,
			)
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
