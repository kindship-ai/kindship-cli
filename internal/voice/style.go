package voice

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"
)

// StyleVoice is one filled-in voice entry from STYLE.md's Sound
// section. The CLI uses it to feed the Opus ideate/author prompts
// (`speaker_profile` placeholder) and to pick the Gemini voice +
// behavioral clause for the render pass.
type StyleVoice struct {
	// Gemini voice name from the 30-voice roster, e.g. "Kore".
	Voice string
	// Behavioral clause — how this voice should deliver, e.g.
	// "low-drama, steady, slightly dry".
	BehavioralClause string
	// Free-form personality description from the rest of the bullet.
	Personality string
}

// StyleSound is the parsed result of `## Sound` in STYLE.md. Both
// narrator and companion may be nil when the agent hasn't filled them
// in yet — callers should require CLI flag overrides in that case.
type StyleSound struct {
	Narrator  *StyleVoice
	Companion *StyleVoice
}

// bracketPattern matches `[VoiceId, behavioral clause]` in a bullet.
// Voice-id grammar is alphanumeric (starts with a letter) to reject
// obvious typos without being so strict we reject future additions;
// IsValidGeminiVoice does the final check.
var bracketPattern = regexp.MustCompile(`\[\s*([A-Za-z][A-Za-z0-9]*)\s*,\s*([^\]]+?)\s*\]`)

// soundHeaderPattern matches `## Sound` and captures everything up
// to the next `## ` heading or end-of-file.
var soundHeaderPattern = regexp.MustCompile(`(?m)^##\s+Sound\s*\n`)

// bulletLinePattern matches a line that starts a list item:
//
//	- **Heading** — body
//
// RE2 lacks lookaheads, so we accept the loose shape here and leave
// the inner `**...**` scanning to the parseVoiceBullet helper —
// negative-lookahead gymnastics aren't worth it when we can just
// substring-search the first line of each bullet.
var bulletLinePattern = regexp.MustCompile(`^\s*(?:[-*+]|\d+\.)\s+\*\*`)

// ParseStyleMdSound extracts narrator + companion voices from an
// agent's STYLE.md. Unfilled / missing bullets return nil for that
// field — caller decides whether to require a CLI override.
//
// Expected bullet shape:
//
//	- **Narrator voice (monologue)** — `[VoiceId, behavioral clause]`. Personality …
//	- **Companion voice (podcast)** — `[VoiceId, behavioral clause]`. Personality …
//
// If the bullet lacks a bracketed pair, or the voice name doesn't
// match the Gemini roster, the entry is treated as unfilled.
//
// ParseStyleMdSound does not return an error because the callers
// (voice commands) only want "did we get a voice yes/no". Scanner-
// level failures surface as missing entries and the CLI falls back
// to requiring CLI flag overrides.
func ParseStyleMdSound(styleMd string) StyleSound {
	soundBody := extractSoundSection(styleMd)
	if soundBody == "" {
		return StyleSound{}
	}
	// Ignoring scanner errors here is deliberate — see doc comment.
	narrator, _ := parseVoiceBullet(soundBody, "Narrator voice (monologue)")
	companion, _ := parseVoiceBullet(soundBody, "Companion voice (podcast)")
	return StyleSound{Narrator: narrator, Companion: companion}
}

// ParseStyleMdSoundStrict is the error-surfacing variant of
// ParseStyleMdSound. Callers that want to distinguish "bullet missing"
// from "parser failure" (e.g. a malformed STYLE.md that trips
// bufio.Scanner) use this instead.
func ParseStyleMdSoundStrict(styleMd string) (StyleSound, error) {
	soundBody := extractSoundSection(styleMd)
	if soundBody == "" {
		return StyleSound{}, nil
	}
	narrator, nErr := parseVoiceBullet(soundBody, "Narrator voice (monologue)")
	companion, cErr := parseVoiceBullet(soundBody, "Companion voice (podcast)")
	if nErr != nil {
		return StyleSound{}, fmt.Errorf("narrator bullet: %w", nErr)
	}
	if cErr != nil {
		return StyleSound{}, fmt.Errorf("companion bullet: %w", cErr)
	}
	return StyleSound{Narrator: narrator, Companion: companion}, nil
}

// extractSoundSection returns the body of the `## Sound` section
// (everything between the heading and the next `## ` heading / EOF).
func extractSoundSection(styleMd string) string {
	loc := soundHeaderPattern.FindStringIndex(styleMd)
	if loc == nil {
		return ""
	}
	body := styleMd[loc[1]:]
	// Truncate at the next top-level `## ` heading so we don't swallow
	// unrelated sections.
	next := regexp.MustCompile(`(?m)^##\s+`).FindStringIndex(body)
	if next != nil {
		body = body[:next[0]]
	}
	return body
}

// parseVoiceBullet finds the `- **<heading>**` bullet in body and
// extracts its parsed entry. Returns nil when the bullet is missing
// or can't be parsed into a valid voice. The returned error surfaces
// scanner-level failures (e.g. bufio.ErrTooLong on an oversized
// line) so callers can distinguish "missing" from "parser tripped".
func parseVoiceBullet(body, heading string) (*StyleVoice, error) {
	needle := "**" + heading + "**"
	scanner := bufio.NewScanner(strings.NewReader(body))
	// Bump the line buffer — STYLE.md bullets occasionally have long
	// personality paragraphs on one line that brush against the
	// default 64KB limit.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var current strings.Builder
	var capturing bool

	for scanner.Scan() {
		line := scanner.Text()
		isBulletStart := bulletLinePattern.MatchString(line)

		if capturing && isBulletStart {
			// Current bullet ended; our prior capture is complete. If
			// the newly-starting bullet is a deeper-indented sub-bullet
			// we'd keep going, but we don't — the voice bullet body
			// we care about is one top-level bullet, no sub-bullets.
			break
		}
		if isBulletStart && strings.Contains(line, needle) {
			capturing = true
			current.Reset()
			current.WriteString(line)
			current.WriteByte('\n')
			continue
		}
		if capturing {
			current.WriteString(line)
			current.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan STYLE.md: %w", err)
	}
	if !capturing {
		return nil, nil
	}
	return parseBulletEntry(current.String()), nil
}

// parseBulletEntry pulls the `[voice, clause]` + personality out of a
// single bullet. Returns nil on any parse/validation failure.
func parseBulletEntry(bullet string) *StyleVoice {
	m := bracketPattern.FindStringSubmatchIndex(bullet)
	if m == nil {
		return nil
	}
	voice := strings.TrimSpace(bullet[m[2]:m[3]])
	clause := strings.TrimSpace(bullet[m[4]:m[5]])
	if !IsValidGeminiVoice(voice) {
		return nil
	}

	// Personality = the bullet with the bracketed pair removed, with
	// leading markdown noise (list marker, bold heading, em-dash)
	// stripped.
	rest := bullet[:m[0]] + bullet[m[1]:]
	rest = strings.TrimSpace(rest)
	// Strip the leading "- **Heading**" up to a leading em-dash / hyphen.
	rest = stripBulletHeading(rest)
	return &StyleVoice{
		Voice:            voice,
		BehavioralClause: clause,
		Personality:      rest,
	}
}

// stripBulletHeading removes the leading `- **Heading**` and the em-
// dash/colon that separates the heading from the body content.
func stripBulletHeading(s string) string {
	// Drop the leading `- ` / `* ` / `1. ` marker.
	s = strings.TrimLeftFunc(s, func(r rune) bool {
		return r == '-' || r == '*' || r == '+' || r == ' ' || r == '\t' ||
			(r >= '0' && r <= '9') || r == '.'
	})
	// Drop the bold heading `**...**`.
	if idx := strings.Index(s, "**"); idx == 0 {
		if close := strings.Index(s[2:], "**"); close != -1 {
			s = s[2+close+2:]
		}
	}
	// Drop the em-dash / hyphen / colon separator.
	s = strings.TrimSpace(s)
	for _, sep := range []string{"—", "–", "-", ":"} {
		if strings.HasPrefix(s, sep) {
			s = strings.TrimSpace(s[len(sep):])
			break
		}
	}
	return s
}
