package voice

import "testing"

const fixtureStyleMd = `# Style

## Foundations

- **Palette** — paper #F7F6F3 …

## Sound

- **Narrator voice (monologue)** — ` + "`[Kore, low-drama, steady, slightly dry]`" + `. Curious rather than soothing; leaves breaths in place.
- **Companion voice (podcast)** — ` + "`[Puck, upbeat but unforced]`" + `. Contrasts the narrator with youthful energy.
- **Music** — sparse felt-piano if any.

## Video
`

func TestParseStyleMdSound_Happy(t *testing.T) {
	got := ParseStyleMdSound(fixtureStyleMd)

	if got.Narrator == nil {
		t.Fatal("narrator should not be nil")
	}
	if got.Narrator.Voice != "Kore" {
		t.Errorf("narrator voice = %q, want Kore", got.Narrator.Voice)
	}
	if got.Narrator.BehavioralClause != "low-drama, steady, slightly dry" {
		t.Errorf("narrator clause wrong: %q", got.Narrator.BehavioralClause)
	}
	if got.Narrator.Personality == "" {
		t.Error("narrator personality should not be empty")
	}

	if got.Companion == nil {
		t.Fatal("companion should not be nil")
	}
	if got.Companion.Voice != "Puck" {
		t.Errorf("companion voice = %q, want Puck", got.Companion.Voice)
	}
}

func TestParseStyleMdSound_MissingSound(t *testing.T) {
	got := ParseStyleMdSound("# Style\n\n## Foundations\n- foo\n")
	if got.Narrator != nil || got.Companion != nil {
		t.Errorf("expected empty result when Sound section missing")
	}
}

func TestParseStyleMdSound_UnfilledBullet(t *testing.T) {
	// No bracket pair → entry is nil.
	md := `## Sound
- **Narrator voice (monologue)** — low-drama, steady. Personality prose.
`
	got := ParseStyleMdSound(md)
	if got.Narrator != nil {
		t.Errorf("expected nil narrator when bracket pair missing, got %+v", got.Narrator)
	}
}

func TestParseStyleMdSound_InvalidVoice(t *testing.T) {
	md := `## Sound
- **Narrator voice (monologue)** — ` + "`[NotAGeminiVoice, clause]`" + `. Personality.
`
	got := ParseStyleMdSound(md)
	if got.Narrator != nil {
		t.Errorf("expected nil narrator when voice not in roster, got %+v", got.Narrator)
	}
}

func TestIsValidGeminiVoice(t *testing.T) {
	if !IsValidGeminiVoice("Kore") {
		t.Error("Kore should be valid")
	}
	if IsValidGeminiVoice("NotReal") {
		t.Error("NotReal should be invalid")
	}
}
