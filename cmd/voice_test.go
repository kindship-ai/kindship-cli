package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kindship-ai/kindship-cli/internal/api"
)

func TestDeriveSlug(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		check func(t *testing.T, got string)
	}{
		{
			name: "simple english text",
			text: "The quiet moment between the question and the answer",
			check: func(t *testing.T, got string) {
				if got != "the-quiet-moment-between-the-question-an" {
					t.Errorf("unexpected slug: %q", got)
				}
			},
		},
		{
			name: "strips trailing dash after truncation",
			text: "Exactly forty one chars including-a-trailer-----",
			check: func(t *testing.T, got string) {
				if strings.HasSuffix(got, "-") {
					t.Errorf("slug %q should not end in dash", got)
				}
			},
		},
		{
			name: "lowercases and strips punctuation",
			text: "Hello, World! This is a TEST.",
			check: func(t *testing.T, got string) {
				if strings.ContainsAny(got, "!.,") {
					t.Errorf("slug %q should not contain punctuation", got)
				}
				for _, r := range got {
					if r >= 'A' && r <= 'Z' {
						t.Errorf("slug %q should be lowercase", got)
					}
				}
			},
		},
		{
			name: "non-ascii text falls back to timestamp",
			text: "こんにちは",
			check: func(t *testing.T, got string) {
				if !strings.HasPrefix(got, "voice-") {
					t.Errorf("expected timestamp fallback, got %q", got)
				}
			},
		},
		{
			name: "empty string falls back to timestamp",
			text: "",
			check: func(t *testing.T, got string) {
				if !strings.HasPrefix(got, "voice-") {
					t.Errorf("expected timestamp fallback, got %q", got)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveSlug(tc.text)
			tc.check(t, got)
		})
	}
}

func TestDeriveSlugPassesValidateSlug(t *testing.T) {
	// Anything deriveSlug produces must be a valid slug the server will
	// accept; otherwise auto-naming breaks when --slug is omitted.
	samples := []string{
		"The quiet moment between the question and the answer",
		"A",
		"Hello, World!",
		"Numbers 12345 and letters mixed",
		strings.Repeat("a", 200),
	}
	for _, s := range samples {
		slug := deriveSlug(s)
		if err := validateSlug(slug); err != nil {
			t.Errorf("deriveSlug(%q) = %q failed validateSlug: %v", s, slug, err)
		}
	}
}

func TestTruncateVoicePreview(t *testing.T) {
	if got := truncateVoicePreview("short", 40); got != "short" {
		t.Errorf("expected passthrough, got %q", got)
	}
	long := strings.Repeat("x", 80)
	got := truncateVoicePreview(long, 40)
	if len(got) <= 40 {
		t.Errorf("truncated result should be >40 chars (got %d) because of ellipsis", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected trailing ellipsis, got %q", got)
	}
}

// TestVoiceExactBatchFileParse pins the exact shape the batch driver file
// must have: {"texts":[...]}. If someone later loosens this without
// updating the CLI, the parse-and-loop path will break silently.
func TestVoiceExactBatchFileParse(t *testing.T) {
	good := []byte(`{"texts":["chapter one","chapter two","chapter three"]}`)
	var batch api.VoiceExactBatchFile
	if err := json.Unmarshal(good, &batch); err != nil {
		t.Fatalf("valid batch file should parse: %v", err)
	}
	if len(batch.Texts) != 3 {
		t.Errorf("expected 3 texts, got %d", len(batch.Texts))
	}
	if batch.Texts[1] != "chapter two" {
		t.Errorf("ordering lost: got %q for index 1", batch.Texts[1])
	}

	bad := []byte(`["chapter one","chapter two"]`) // array, not wrapped object
	var batch2 api.VoiceExactBatchFile
	if err := json.Unmarshal(bad, &batch2); err == nil {
		t.Errorf("bare array should fail to unmarshal into VoiceExactBatchFile")
	}
}

// TestVoiceExactManifestEntry pins the filename convention and shape so a
// regression would surface as a failing test instead of a silently-broken
// directory layout.
func TestVoiceExactManifestEntry(t *testing.T) {
	entry := api.VoiceExactManifestEntry{
		Index: 1,
		Text:  "Chapter one: the beginning",
		File:  "01-chapter-one-the-beginning.wav",
		Bytes: 123456,
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	s := string(raw)
	for _, want := range []string{`"index":1`, `"text":`, `"file":`, `"bytes":123456`} {
		if !strings.Contains(s, want) {
			t.Errorf("manifest entry JSON missing %q: %s", want, s)
		}
	}
}

// TestWriteMetaSidecar exercises the podcast .meta.json writer end-to-end
// to catch regressions in the critical path that the codex review flagged
// (the sidecar is required — a silent failure must not be possible).
func TestWriteMetaSidecar(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ep.meta.json")
	meta := map[string]any{
		"slug":           "a-short-one",
		"title":          "A short one",
		"cold_open_note": "mid-reaction, no formal intro",
		"line_count":     "42",
	}
	if err := writeMetaSidecar(p, meta); err != nil {
		t.Fatalf("writeMetaSidecar failed: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("sidecar not readable: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatalf("sidecar JSON invalid: %v", err)
	}
	if round["title"] != "A short one" {
		t.Errorf("title round-trip lost: got %v", round["title"])
	}
	if round["cold_open_note"] != "mid-reaction, no formal intro" {
		t.Errorf("cold_open_note round-trip lost: got %v", round["cold_open_note"])
	}
}

// TestWriteMetaSidecarFailsOnUnwritablePath proves the writer surfaces
// errors — it does NOT swallow them. If someone re-introduces the silent-
// failure bug, this test fails.
func TestWriteMetaSidecarFailsOnUnwritablePath(t *testing.T) {
	// Writing under a non-existent parent directory (we don't MkdirAll
	// inside writeMetaSidecar) must fail.
	err := writeMetaSidecar("/does/not/exist/ep.meta.json", map[string]any{"a": 1})
	if err == nil {
		t.Errorf("expected error writing to non-existent parent dir, got nil")
	}
}

// Tag-pass prose-preservation validator — these tests guard the
// bracket-whitelist invariant we committed to in Phase C. Changes to
// the audio-tag vocabulary in monologue-tag-system.md MUST be mirrored
// in audioTagPattern, and these tests will fail first.
func TestNormalizeForProseCheck_StripsKnownTags(t *testing.T) {
	in := "[pause] Welcome. [breath] I am here. [softly] Listen. [short pause] Begin."
	want := "Welcome. I am here. Listen. Begin."
	if got := normalizeForProseCheck(in); got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestNormalizeForProseCheck_PreservesNonAudioBrackets(t *testing.T) {
	// Authored prose may contain [sic] or [redacted] — those are content,
	// not audio tags, and must survive the normalize pass so a tagged
	// beat that preserves them compares equal to the authored version.
	in := "She said [sic] he did it [redacted]."
	want := "She said [sic] he did it [redacted]."
	if got := normalizeForProseCheck(in); got != want {
		t.Errorf("non-audio brackets should survive; expected %q, got %q", want, got)
	}
}

func TestNormalizeForProseCheck_CollapsesWhitespace(t *testing.T) {
	in := "  Welcome.   [pause]   I   am   here.  "
	want := "Welcome. I am here."
	if got := normalizeForProseCheck(in); got != want {
		t.Errorf("expected whitespace collapse %q, got %q", want, got)
	}
}

func TestNormalizeForProseCheck_ValidatorAgreementOnTaggedVsAuthored(t *testing.T) {
	// The critical invariant: tagging should not change the prose — a
	// tagged beat normalizes to the same string as its authored source.
	authored := "Welcome. I am Thoth. Enter slowly."
	tagged := "[softly] Welcome. [pause] I am Thoth. [breath] Enter slowly. [pause]"
	if normalizeForProseCheck(tagged) != normalizeForProseCheck(authored) {
		t.Errorf("tagged should normalize equal to authored\n  tagged → %q\n  authored → %q",
			normalizeForProseCheck(tagged), normalizeForProseCheck(authored))
	}
}

func TestNormalizeForProseCheck_DetectsProseRewrite(t *testing.T) {
	// A rewrite — even a subtle word change — must surface as a mismatch.
	authored := "Welcome. I am Thoth."
	tagged := "[softly] Greetings. [pause] I am Thoth."
	if normalizeForProseCheck(tagged) == normalizeForProseCheck(authored) {
		t.Error("validator failed to detect prose rewrite (authored 'Welcome' → tagged 'Greetings')")
	}
}

func TestCapitalize(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"narrator":  "Narrator",
		"companion": "Companion",
		"a":         "A",
	}
	for in, want := range cases {
		if got := capitalize(in); got != want {
			t.Errorf("capitalize(%q) = %q, want %q", in, got, want)
		}
	}
}
