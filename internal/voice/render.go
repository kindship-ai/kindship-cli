package voice

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SpeakerProfile is the shape passed as `{{speaker_profile}}` (or
// `{{speaker_1_profile}}`, `{{speaker_2_profile}}`) to the Opus
// ideate/author prompts. Mirrors the old server-side shape so the
// prompts can be reused verbatim.
type SpeakerProfile struct {
	Name             string
	Role             string
	VoiceID          string
	BehavioralClause string
	Personality      string
}

// RenderSpeakerProfile formats a profile as the multi-line string the
// prompts expect to see inlined where `{{speaker_profile}}` appears.
func RenderSpeakerProfile(p SpeakerProfile) string {
	return strings.Join([]string{
		"name: " + p.Name,
		"role: " + p.Role,
		"voice_id: " + p.VoiceID,
		"behavioral_clause: " + p.BehavioralClause,
		"personality:",
		p.Personality,
	}, "\n")
}

// MonologueBeat is one line from the author-pass JSON.
type MonologueBeat struct {
	Text            string  `json:"text"`
	PerformanceHint *string `json:"performance_hint"`
}

// MonologueScript is the author-pass JSON output for a single-speaker
// monologue.
type MonologueScript struct {
	Scratchpad string          `json:"scratchpad"`
	Title      string          `json:"title"`
	OpeningCue string          `json:"opening_cue"`
	Beats      []MonologueBeat `json:"beats"`
}

// PodcastLine is one dialogue turn.
type PodcastLine struct {
	Speaker         string  `json:"speaker"`
	Text            string  `json:"text"`
	PerformanceHint *string `json:"performance_hint"`
}

// PodcastScript is the author-pass JSON output for a two-speaker
// podcast.
type PodcastScript struct {
	Scratchpad   string        `json:"scratchpad"`
	Title        string        `json:"title"`
	ColdOpenNote string        `json:"cold_open_note"`
	Dialogue     []PodcastLine `json:"dialogue"`
}

// ParseMonologueScript unmarshals the author-pass JSON output. Opus
// occasionally wraps its JSON in code fences despite the prompt's
// explicit no-fence rule, so this is lenient.
func ParseMonologueScript(raw string) (*MonologueScript, error) {
	body := stripCodeFences(raw)
	var out MonologueScript
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return nil, fmt.Errorf("parse monologue script: %w: %s", err, preview(body))
	}
	if len(out.Beats) == 0 {
		return nil, fmt.Errorf("monologue script has no beats: %s", preview(body))
	}
	return &out, nil
}

// ParsePodcastScript unmarshals the author-pass JSON output for a
// two-speaker podcast.
func ParsePodcastScript(raw string) (*PodcastScript, error) {
	body := stripCodeFences(raw)
	var out PodcastScript
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return nil, fmt.Errorf("parse podcast script: %w: %s", err, preview(body))
	}
	if len(out.Dialogue) == 0 {
		return nil, fmt.Errorf("podcast script has no dialogue: %s", preview(body))
	}
	return &out, nil
}

// Default scene / directorial-style prose — matches what the deleted
// /api/cli/voice/* routes used, so audio quality is continuous across
// the restructure.
const (
	defaultMonologueScene = "Close mic, single speaker, unhurried. Room tone audible — presence, not performance."
	defaultPodcastScene   = "A small sound-treated room, late afternoon, two microphones between them. Conversational, not broadcast-grade."
	defaultDirectorStyle  = "Conversational and specific. The opposite of 'radio announcer'. Vocal warmth present but subtle. No bombast. Information lands because it's interesting, not because the delivery underlined it."
)

// RenderExactPrompt is what `kindship voice exact` sends to Gemini
// Live: a persona tag followed by the verbatim text. The bracketed
// `[voice, clause]` is load-bearing — the model only commits to the
// persona when both parts are present.
func RenderExactPrompt(voice, style, text string) string {
	return fmt.Sprintf("[%s, %s] %s", voice, style, text)
}

// RenderMonologueGeminiPrompt builds the full Gemini TTS prompt
// (profile + scene + director notes + persona tag + transcript) for
// a single-speaker monologue render pass.
//
// Audio tags ([pause], [breath], [softly], etc.) are inserted by the
// Opus tag pass upstream — this function does NOT ask Gemini to
// improvise tags. The transcript is used as-is; if the tag pass ran,
// tags are already inline in beat.Text.
func RenderMonologueGeminiPrompt(
	script *MonologueScript,
	profile SpeakerProfile,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# AUDIO PROFILE: Speaker\n%s\n\n", RenderSpeakerProfile(profile))
	fmt.Fprintf(&b, "## THE SCENE\n%s\n\n", defaultMonologueScene)
	fmt.Fprintf(&b, "### DIRECTOR'S NOTES\n\nStyle: %s\n\n", defaultDirectorStyle)
	b.WriteString("Pace: Natural pace. Let phrases breathe. Pauses are real pauses, not polite gaps. When a beat ends with \"…\" or \"—\", honor it in the timing.\n\n")
	b.WriteString("Dynamics: Low. Close-mic, intimate, not projected. No shouting. Honor inline audio tags ([pause], [breath], [softly], etc.) verbatim.\n\n")
	b.WriteString("### PERFORMANCE GUIDANCE\n\nFor each beat, follow the text as written. Where a performance_hint is provided, let it shape delivery. Where no hint is provided, deliver naturally based on context and the speaker's personality.\n\n")
	b.WriteString("#### TRANSCRIPT\n\n")
	fmt.Fprintf(&b, "[%s, %s]\n\n", profile.VoiceID, profile.BehavioralClause)
	for _, beat := range script.Beats {
		if beat.PerformanceHint != nil && *beat.PerformanceHint != "" {
			fmt.Fprintf(&b, "[%s] %s\n", *beat.PerformanceHint, beat.Text)
		} else {
			fmt.Fprintf(&b, "%s\n", beat.Text)
		}
	}
	return b.String()
}

// RenderPodcastGeminiPrompt builds the TTS multi-speaker prompt for
// a two-voice podcast: dual profiles, scene, director notes, plus
// transcript lines prefixed with `Speaker: [hint] text`.
func RenderPodcastGeminiPrompt(
	script *PodcastScript,
	speaker1, speaker2 SpeakerProfile,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# AUDIO PROFILE: Speaker 1\n%s\n\n", RenderSpeakerProfile(speaker1))
	fmt.Fprintf(&b, "# AUDIO PROFILE: Speaker 2\n%s\n\n", RenderSpeakerProfile(speaker2))
	fmt.Fprintf(&b, "## THE SCENE\n%s\n\n", defaultPodcastScene)
	fmt.Fprintf(&b, "### DIRECTOR'S NOTES\n\nStyle: %s\n\n", defaultDirectorStyle)
	b.WriteString("Pace: Natural conversational pace. Slightly faster than broadcast — the rhythm of actual talk. Interruptions are real interruptions, not polite handoffs. When a line ends with \"…\" or \"—\", honor that in the timing.\n\n")
	b.WriteString("Dynamics: Low. This is close-mic, intimate, not projected. Whispers are rare but permitted for emphasis. Shouting never.\n\n")
	b.WriteString("### PERFORMANCE GUIDANCE\n\nFor each line, follow the text as written. Where a performance_hint is provided, let it shape delivery. Where no hint is provided, deliver naturally based on context and the speaker's personality.\n\n")
	b.WriteString("Inject the following where they fit organically, even if not written:\n- Brief [laughs] or [small laugh] when the line genuinely warrants it\n- [thinking] or [searching for a word] before lines where a speaker visibly hesitates\n- [short pause] between topic shifts\n\n")
	b.WriteString("Do not over-inject. If tags appear on more than a quarter of lines, you're overdoing it.\n\n")
	b.WriteString("#### TRANSCRIPT\n\n")
	for _, line := range script.Dialogue {
		if line.PerformanceHint != nil && *line.PerformanceHint != "" {
			fmt.Fprintf(&b, "%s: [%s] %s\n", line.Speaker, *line.PerformanceHint, line.Text)
		} else {
			fmt.Fprintf(&b, "%s: %s\n", line.Speaker, line.Text)
		}
	}
	return b.String()
}

// stripCodeFences removes ```json ... ``` wrappers that Opus
// sometimes adds despite prompts forbidding them.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Drop the first fence + optional language tag.
		if nl := strings.IndexByte(s, '\n'); nl != -1 {
			s = s[nl+1:]
		}
	}
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSpace(s[:len(s)-3])
	}
	return s
}

func preview(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
