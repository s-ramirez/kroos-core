package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const skillFileName = "SKILL.md"

var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

const maxNameLen = 64
const maxDescriptionLen = 1024

// LoadSkills discovers and loads skills from the given directories.
// Directories are scanned in order; the first skill with a given name wins.
func LoadSkills(dirs ...string) ([]Skill, []string, error) {
	var skills []Skill
	var diagnostics []string
	seen := make(map[string]string) // name -> filePath of winner

	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}

		found, diags, err := scanDir(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("scanning %s: %w", dir, err)
		}
		diagnostics = append(diagnostics, diags...)

		for _, s := range found {
			if existing, ok := seen[s.Name]; ok {
				diagnostics = append(diagnostics,
					fmt.Sprintf("skill %q from %s ignored: already loaded from %s", s.Name, s.FilePath, existing))
				continue
			}
			seen[s.Name] = s.FilePath
			skills = append(skills, s)
		}
	}

	return skills, diagnostics, nil
}

// scanDir walks a directory looking for SKILL.md files.
// If dir itself contains SKILL.md, it is treated as a single skill root.
// Otherwise, direct subdirectories are checked for SKILL.md.
func scanDir(dir string) ([]Skill, []string, error) {
	skillFile := filepath.Join(dir, skillFileName)
	if _, err := os.Stat(skillFile); err == nil {
		s, diags, err := loadSkillFile(skillFile)
		if err != nil {
			return nil, nil, err
		}
		if s != nil {
			return []Skill{*s}, diags, nil
		}
		return nil, diags, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}

	var skills []Skill
	var diagnostics []string

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		subFile := filepath.Join(dir, entry.Name(), skillFileName)
		if _, err := os.Stat(subFile); err != nil {
			continue
		}
		s, diags, err := loadSkillFile(subFile)
		if err != nil {
			return nil, nil, err
		}
		diagnostics = append(diagnostics, diags...)
		if s != nil {
			skills = append(skills, *s)
		}
	}

	return skills, diagnostics, nil
}

// loadSkillFile reads and parses a single SKILL.md file.
func loadSkillFile(path string) (*Skill, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}

	frontmatter, body := parseFrontmatter(string(data))

	name := frontmatter["name"]
	if name == "" {
		name = filepath.Base(filepath.Dir(path))
	}

	description := frontmatter["description"]

	var diagnostics []string

	if diags := validateName(name, path); len(diags) > 0 {
		diagnostics = append(diagnostics, diags...)
	}

	if description == "" {
		diagnostics = append(diagnostics, fmt.Sprintf("%s: description is required", path))
		return nil, diagnostics, nil
	}

	if len(description) > maxDescriptionLen {
		diagnostics = append(diagnostics,
			fmt.Sprintf("%s: description exceeds %d characters", path, maxDescriptionLen))
	}

	disableModel := frontmatter["disable-model-invocation"] == "true"

	return &Skill{
		Name:                   name,
		Description:            description,
		Content:                strings.TrimSpace(body),
		DisableModelInvocation: disableModel,
		FilePath:               path,
	}, diagnostics, nil
}

func validateName(name, path string) []string {
	var diags []string
	if len(name) > maxNameLen {
		diags = append(diags, fmt.Sprintf("%s: name %q exceeds %d characters", path, name, maxNameLen))
	}
	if !namePattern.MatchString(name) {
		diags = append(diags, fmt.Sprintf("%s: name %q must be lowercase alphanumeric with hyphens", path, name))
	}
	return diags
}

// parseFrontmatter splits a markdown file into YAML frontmatter key-value pairs
// and the remaining body. Only simple "key: value" lines are supported.
func parseFrontmatter(content string) (map[string]string, string) {
	fm := make(map[string]string)

	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "---") {
		return fm, content
	}

	// Find the closing ---
	rest := trimmed[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return fm, content
	}

	block := rest[:idx]
	body := rest[idx+4:] // skip past \n---

	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		value := strings.TrimSpace(line[colon+1:])
		fm[key] = value
	}

	return fm, body
}

// ParseFrontmatter is the exported form of parseFrontmatter, used by
// skill-authoring tools to validate SKILL.md content before writing.
func ParseFrontmatter(content string) (map[string]string, string) {
	return parseFrontmatter(content)
}

// ValidName reports whether name is a legal skill name.
func ValidName(name string) bool {
	return len(name) <= maxNameLen && namePattern.MatchString(name)
}
