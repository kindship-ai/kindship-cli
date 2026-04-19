// Package prompts reads LLM prompt markdown files from a skill directory
// on the agent container and fills in {{placeholder}} tokens.
//
// Prompts ship as part of each skill under
//
//	/home/autonomous/.claude/skills/<skill>/prompts/<name>.md
//
// They're installed by the web-side `rollout-skills-to-fleet.ts` script,
// which walks every file under each skill directory.
//
// If a prompt file is missing the error message directs the operator at
// the rollout script, because the most common failure mode is a new
// prompt that hasn't been rolled out to the fleet yet.
package prompts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultSkillsRoot is where the agent container's skill tree lives.
// Overridable via $KINDSHIP_SKILLS_ROOT for tests and local dev.
const DefaultSkillsRoot = "/home/autonomous/.claude/skills"

// SkillsRoot returns the directory agents load skills from. Env var wins
// over the compiled default so integration tests on a laptop can point
// at a fixture tree.
func SkillsRoot() string {
	if v := os.Getenv("KINDSHIP_SKILLS_ROOT"); v != "" {
		return v
	}
	return DefaultSkillsRoot
}

// Load reads <skill>/prompts/<name>.md from the skills root and returns
// its raw contents.
func Load(skill, name string) (string, error) {
	if skill == "" {
		return "", fmt.Errorf("prompts.Load: skill is empty")
	}
	if name == "" {
		return "", fmt.Errorf("prompts.Load: name is empty")
	}
	path := filepath.Join(SkillsRoot(), skill, "prompts", name+".md")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf(
				"prompt file not found: %s\n\n"+
					"This usually means the fleet skills are out of date. "+
					"From apps/web, run:\n"+
					"    pnpm script:prod scripts/rollout-skills-to-fleet.ts",
				path,
			)
		}
		return "", fmt.Errorf("read prompt %s: %w", path, err)
	}
	return string(raw), nil
}

// Render substitutes {{key}} tokens in template with values from vars.
// Missing keys are left intact so authoring mistakes surface at review
// rather than silently producing empty strings — same behavior as
// apps/web/lib/skills/load-prompt.ts::renderPromptTemplate.
func Render(template string, vars map[string]string) string {
	out := template
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}

// LoadAndRender is the common case: load <skill>/prompts/<name>.md and
// render it with vars.
func LoadAndRender(skill, name string, vars map[string]string) (string, error) {
	tmpl, err := Load(skill, name)
	if err != nil {
		return "", err
	}
	return Render(tmpl, vars), nil
}
