package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SkillWriteTool authors a project-level skill file at
// <skillsDir>/<name>/SKILL.md and triggers a registry reload so the
// new skill is invocable via /skill:<name> within the same session.
//
// Hot-reload note: freshly written skills show up in CommandExpander
// immediately, but the system-prompt <available_skills> block is
// cached per conversation, so the model won't "see" the new skill in
// its tool catalog until a /new reset (or a fresh session).
type SkillWriteTool struct {
	skillsDir string
	logPath   string
	reload    func() error
}

func NewSkillWriteTool(skillsDir, logPath string, reload func() error) *SkillWriteTool {
	return &SkillWriteTool{skillsDir: skillsDir, logPath: logPath, reload: reload}
}

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

const (
	skillMaxNameLen        = 64
	skillMaxDescriptionLen = 1024
)

var skillWriteDefinition = ToolDefinition{
	Name: "skill_write",
	Description: "Author or replace a project-level skill at .kroos/skills/<name>/SKILL.md. " +
		"Use this to teach yourself a recurring routine (e.g. \"log my meals daily\") that should persist across sessions. " +
		"After writing, the skills registry is reloaded so /skill:<name> works immediately.",
	Guidelines: []string{
		"name must be lowercase alphanumeric with hyphens (e.g. \"calorie-log\"), max 64 chars.",
		"description is required and must fit one sentence — it's what future-you reads to decide if the skill applies.",
		"body is the skill's actual instructions (plain Markdown, no frontmatter — the tool generates frontmatter).",
		"Set overwrite=true to replace an existing skill. Default is false so you don't clobber by accident.",
		"Set disable_model_invocation=true for skills that should only be user-invoked via /skill:<name> and not auto-suggested.",
		"After writing, the new skill is callable via /skill:<name> in the current session, but won't appear in the model's own <available_skills> list until a /new reset.",
	},
	Parameters: JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Skill name (lowercase alphanumeric with hyphens, max 64 chars).",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "One-sentence description of when to use this skill.",
			},
			"body": map[string]any{
				"type":        "string",
				"description": "The skill content (Markdown). Do not include YAML frontmatter — it is generated.",
			},
			"disable_model_invocation": map[string]any{
				"type":        "boolean",
				"description": "If true, the skill is hidden from the model's auto-suggest list and only invocable via /skill:<name>.",
			},
			"overwrite": map[string]any{
				"type":        "boolean",
				"description": "If true, replace an existing skill with the same name. Default false.",
			},
		},
		"required": []string{"name", "description", "body"},
	},
}

type SkillWriteParams struct {
	Name                   string `json:"name"`
	Description            string `json:"description"`
	Body                   string `json:"body"`
	DisableModelInvocation bool   `json:"disable_model_invocation"`
	Overwrite              bool   `json:"overwrite"`
}

func (t *SkillWriteTool) Definition() ToolDefinition { return skillWriteDefinition }

func (t *SkillWriteTool) Execute(_ context.Context, raw json.RawMessage) (ToolResult, error) {
	var params SkillWriteParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return errorResult("invalid JSON arguments"), nil
	}

	name := strings.TrimSpace(params.Name)
	if name == "" {
		return errorResult("name is required"), nil
	}
	if len(name) > skillMaxNameLen {
		return errorResultf("name exceeds %d characters", skillMaxNameLen), nil
	}
	if !skillNamePattern.MatchString(name) {
		return errorResult("name must be lowercase alphanumeric with hyphens (e.g. \"calorie-log\")"), nil
	}

	description := strings.TrimSpace(params.Description)
	if description == "" {
		return errorResult("description is required"), nil
	}
	if len(description) > skillMaxDescriptionLen {
		return errorResultf("description exceeds %d characters", skillMaxDescriptionLen), nil
	}
	if strings.ContainsAny(description, "\r\n") {
		return errorResult("description must be a single line (no newlines)"), nil
	}

	body := params.Body
	if strings.TrimSpace(body) == "" {
		return errorResult("body is required"), nil
	}
	if strings.HasPrefix(strings.TrimSpace(body), "---") {
		return errorResult("body must not contain YAML frontmatter — it is generated from name/description"), nil
	}

	skillDir := filepath.Join(t.skillsDir, name)
	skillFile := filepath.Join(skillDir, "SKILL.md")

	if !params.Overwrite {
		if _, err := os.Stat(skillFile); err == nil {
			return errorResultf("skill %q already exists at %s (set overwrite=true to replace)", name, skillFile), nil
		}
	}

	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return errorResultf("failed to create skill directory: %v", err), nil
	}

	content := renderSkillFile(name, description, body, params.DisableModelInvocation)
	if err := os.WriteFile(skillFile, []byte(content), 0o644); err != nil {
		return errorResultf("failed to write skill file: %v", err), nil
	}

	if err := t.appendLog(name, skillFile, params.Overwrite); err != nil {
		fmt.Fprintf(os.Stderr, "warning: skill_write log append failed: %v\n", err)
	}

	if t.reload != nil {
		if err := t.reload(); err != nil {
			return errorResultf("wrote %s but reload failed: %v", skillFile, err), nil
		}
	}

	verb := "wrote"
	if params.Overwrite {
		verb = "replaced"
	}
	return ToolResult{
		Output: fmt.Sprintf("%s skill %q at %s — invocable now via /skill:%s (add to <available_skills> on next /new)", verb, name, skillFile, name),
	}, nil
}

func renderSkillFile(name, description, body string, disableModel bool) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", name)
	fmt.Fprintf(&b, "description: %s\n", description)
	if disableModel {
		b.WriteString("disable-model-invocation: true\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n")
	return b.String()
}

func (t *SkillWriteTool) appendLog(name, path string, overwrite bool) error {
	if t.logPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(t.logPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(t.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	verb := "wrote"
	if overwrite {
		verb = "replaced"
	}
	_, err = fmt.Fprintf(f, "%s %s skill=%s path=%s\n", time.Now().UTC().Format(time.RFC3339), verb, name, path)
	return err
}
