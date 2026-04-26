package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupSkillsRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	t.Setenv("KINDSHIP_SKILLS_ROOT", root)
	return root
}

func TestLoadAndRender(t *testing.T) {
	setupSkillsRoot(t, map[string]string{
		"kindship-voice/prompts/test.md": "Vision: {{vision}}\nAgent: {{agent_name}}",
	})

	got, err := LoadAndRender("kindship-voice", "test", map[string]string{
		"vision":     "a quieter internet",
		"agent_name": "akasha",
	})
	if err != nil {
		t.Fatalf("LoadAndRender error: %v", err)
	}
	want := "Vision: a quieter internet\nAgent: akasha"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderLeavesUnknownPlaceholdersIntact(t *testing.T) {
	// Authoring mistakes should surface at review — better to see
	// {{typo}} in the prompt than an empty silent substitution.
	got := Render("hello {{name}} / {{missing}}", map[string]string{"name": "a"})
	want := "hello a / {{missing}}"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLoadMissingFileMentionsRollout(t *testing.T) {
	setupSkillsRoot(t, nil)

	_, err := Load("kindship-voice", "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing prompt file")
	}
	if !strings.Contains(err.Error(), "rollout-skills-to-fleet.ts") {
		t.Fatalf("error should mention the rollout script, got: %v", err)
	}
}

func TestLoadRejectsEmptyArgs(t *testing.T) {
	if _, err := Load("", "x"); err == nil {
		t.Fatal("empty skill should error")
	}
	if _, err := Load("x", ""); err == nil {
		t.Fatal("empty name should error")
	}
}
