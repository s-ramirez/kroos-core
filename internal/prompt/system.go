package prompt

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/s-ramirez/kroos-core/internal/skills"
	"github.com/s-ramirez/kroos-core/internal/tools"
)

type Environment struct {
	OS        string
	Arch      string
	Workspace string
	Timezone  string
	Time      string
}

// ContextFile is a named file to embed verbatim in the system prompt under
// the "# Context Files" section. Path is shown as the section header.
type ContextFile struct {
	Path    string
	Content string
}

const defaultPrompt = "You are a coding assistant. You help the user by reading, writing, editing files, and executing commands when needed."

func BuildSystemPrompt(agentPrompt string, available []tools.Tool, loadedSkills []skills.Skill, env Environment, curatedMemory string, pluginSections []string, contextFiles ...ContextFile) string {
	if strings.TrimSpace(agentPrompt) == "" {
		agentPrompt = defaultPrompt
	}
	// Context files (IDENTITY.md, USER.md) are injected first so they frame
	// everything that follows. The agent prompt may reference or reinforce them.
	if s := buildContextFilesSection(contextFiles); s != "" {
		return strings.Join(append([]string{s, agentPrompt}, buildRemainder(available, loadedSkills, env, curatedMemory, pluginSections)...), "\n\n")
	}

	sections := []string{agentPrompt}
	sections = append(sections, buildRemainder(available, loadedSkills, env, curatedMemory, pluginSections)...)
	return strings.Join(sections, "\n\n")
}

func buildRemainder(available []tools.Tool, loadedSkills []skills.Skill, env Environment, curatedMemory string, pluginSections []string) []string {
	var sections []string

	sections = append(sections, buildEnvironment(env))

	if s := buildCuratedMemorySection(curatedMemory, available); s != "" {
		sections = append(sections, s)
	}

	if len(available) > 0 {
		sections = append(sections, buildToolGuidelines(available))
		if s := buildToolSelectionRules(available); s != "" {
			sections = append(sections, s)
		}
	}

	if s := buildSkillsSection(loadedSkills); s != "" {
		sections = append(sections, s)
	}

	for _, s := range pluginSections {
		if s != "" {
			sections = append(sections, s)
		}
	}

	return sections
}

func buildContextFilesSection(files []ContextFile) string {
	var valid []ContextFile
	for _, f := range files {
		if strings.TrimSpace(f.Path) != "" && strings.TrimSpace(f.Content) != "" {
			valid = append(valid, f)
		}
	}
	if len(valid) == 0 {
		return ""
	}

	hasIdentity := false
	for _, f := range valid {
		base := filepath.Base(f.Path)
		if strings.EqualFold(base, "identity.md") {
			hasIdentity = true
			break
		}
	}

	lines := []string{"# Context Files"}
	lines = append(lines, "The following files describe who you are and who you're helping.")
	if hasIdentity {
		lines = append(lines, "IDENTITY.md defines your persona — embody it. Avoid generic replies; follow its guidance unless a higher-priority instruction overrides it.")
	}
	lines = append(lines, "You can update these files with the write or edit tool as you learn more.")
	lines = append(lines, "")

	for _, f := range valid {
		lines = append(lines, "## "+f.Path)
		lines = append(lines, "")
		lines = append(lines, f.Content)
	}

	return strings.Join(lines, "\n")
}

func buildCuratedMemorySection(curated string, available []tools.Tool) string {
	curated = strings.TrimSpace(curated)
	hasMemoryTools := false
	for _, t := range available {
		if t.Definition().Name == "memory" {
			hasMemoryTools = true
			break
		}
	}
	if curated == "" && !hasMemoryTools {
		return ""
	}

	lines := []string{"# Memory"}
	if hasMemoryTools {
		lines = append(lines,
			"You have a persistent memory store at .kroos/memory/ with three layers:",
			"- MEMORY.md — curated long-term facts (shown below). Only the /memory:distill flow should rewrite it.",
			"- sessions/YYYY-MM-DD-HHMM.md — durable session notes. Use `memory` with action=write to save anything worth remembering from this session.",
			"- vault/{Memory,Knowledge,Journal,Notes}/*.md — permanent reference notes. Use `memory` with action=read/search/list to recall.",
		)
	}
	if curated != "" {
		lines = append(lines, "", "<curated_memory>", curated, "</curated_memory>")
	}
	return strings.Join(lines, "\n")
}

func buildSkillsSection(loadedSkills []skills.Skill) string {
	var visible []skills.Skill
	for _, s := range loadedSkills {
		if !s.DisableModelInvocation {
			visible = append(visible, s)
		}
	}
	if len(visible) == 0 {
		return ""
	}

	lines := []string{
		"The following skills provide specialized instructions for specific tasks.",
		"When the task matches a skill's description, load it first (skill_view if available, else read its location).",
		"When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.",
		"",
		"<available_skills>",
	}
	for _, s := range visible {
		lines = append(lines, "  <skill>")
		lines = append(lines, fmt.Sprintf("    <name>%s</name>", s.Name))
		lines = append(lines, fmt.Sprintf("    <description>%s</description>", s.Description))
		lines = append(lines, fmt.Sprintf("    <location>%s</location>", s.FilePath))
		lines = append(lines, "  </skill>")
	}
	lines = append(lines, "</available_skills>")
	return strings.Join(lines, "\n")
}

func buildEnvironment(env Environment) string {
	lines := []string{"# Environment"}

	if env.OS != "" || env.Arch != "" {
		lines = append(lines, fmt.Sprintf("- Platform: %s/%s", env.OS, env.Arch))
	}
	if env.Workspace != "" {
		lines = append(lines, fmt.Sprintf("- Workspace: %s", env.Workspace))
	}
	if env.Time != "" {
		line := fmt.Sprintf("- Current time: %s", env.Time)
		if env.Timezone != "" {
			line += fmt.Sprintf(" (%s)", env.Timezone)
		}
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func buildToolGuidelines(available []tools.Tool) string {
	lines := []string{"# Tools"}

	for _, tool := range available {
		definition := tool.Definition()
		if len(definition.Guidelines) == 0 {
			continue
		}

		lines = append(lines, "")
		lines = append(lines, "## "+definition.Name)
		for _, guideline := range definition.Guidelines {
			lines = append(lines, "- "+guideline)
		}
	}

	return strings.Join(lines, "\n")
}

// buildToolSelectionRules walks the SelectionRules declared on each
// available tool and emits the bullets whose Requires are satisfied.
// Rules are emitted in the order tools are listed, preserving each
// tool's own rule order — keeping the per-tool narrative intact.
func buildToolSelectionRules(available []tools.Tool) string {
	present := make(map[string]struct{}, len(available))
	for _, t := range available {
		present[t.Definition().Name] = struct{}{}
	}

	lines := []string{"# Tool selection"}
	for _, t := range available {
		for _, rule := range t.Definition().SelectionRules {
			if !allPresent(rule.Requires, present) {
				continue
			}
			lines = append(lines, "- "+rule.Text)
		}
	}
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func allPresent(required []string, present map[string]struct{}) bool {
	for _, name := range required {
		if _, ok := present[name]; !ok {
			return false
		}
	}
	return true
}
