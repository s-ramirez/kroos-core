package subagents

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9]+(-[A-Za-z0-9]+)*$`)

const (
	maxAgentNameLen        = 64
	maxAgentDescriptionLen = 1024
)

func LoadDefinitions(dirs ...string) ([]Definition, []string, error) {
	var defs []Definition
	var diagnostics []string
	seen := make(map[string]string)

	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("reading %s: %w", dir, err)
		}

		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}

			path := filepath.Join(dir, entry.Name())
			def, diags, err := loadDefinitionFile(path)
			if err != nil {
				return nil, nil, err
			}
			diagnostics = append(diagnostics, diags...)
			if def == nil {
				continue
			}

			if existing, ok := seen[def.Name]; ok {
				diagnostics = append(diagnostics,
					fmt.Sprintf("subagent %q from %s ignored: already loaded from %s", def.Name, def.FilePath, existing))
				continue
			}

			seen[def.Name] = def.FilePath
			defs = append(defs, *def)
		}
	}

	return defs, diagnostics, nil
}

func loadDefinitionFile(path string) (*Definition, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}

	frontmatter, body := parseFrontmatter(string(data))
	name := strings.TrimSpace(frontmatter["name"])
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	var diagnostics []string
	if len(name) > maxAgentNameLen {
		diagnostics = append(diagnostics, fmt.Sprintf("%s: name %q exceeds %d characters", path, name, maxAgentNameLen))
	}
	if !agentNamePattern.MatchString(name) {
		diagnostics = append(diagnostics, fmt.Sprintf("%s: name %q must be alphanumeric with hyphens", path, name))
	}

	description := strings.TrimSpace(frontmatter["description"])
	if len(description) > maxAgentDescriptionLen {
		diagnostics = append(diagnostics, fmt.Sprintf("%s: description exceeds %d characters", path, maxAgentDescriptionLen))
	}
	whenToUse := strings.TrimSpace(frontmatter["when_to_use"])
	if whenToUse == "" {
		whenToUse = strings.TrimSpace(frontmatter["when-to-use"])
	}

	model := strings.TrimSpace(frontmatter["model"])
	if model == "" {
		diagnostics = append(diagnostics, fmt.Sprintf("%s: model is required", path))
	}

	provider := strings.TrimSpace(frontmatter["provider"])

	contextSize, ctxErr := parseContextSizeField(frontmatter["context_size"])
	if ctxErr != nil {
		diagnostics = append(diagnostics, fmt.Sprintf("%s: %v", path, ctxErr))
	}

	tools := parseList(frontmatter["tools"])
	if len(tools) == 0 {
		diagnostics = append(diagnostics, fmt.Sprintf("%s: tools must contain at least one tool name", path))
	}

	plugins := parseList(frontmatter["plugins"])
	prompt := strings.TrimSpace(body)
	if prompt == "" {
		diagnostics = append(diagnostics, fmt.Sprintf("%s: prompt body is required", path))
	}

	if len(diagnostics) > 0 {
		return nil, diagnostics, nil
	}

	return &Definition{
		Name:        name,
		Description: description,
		WhenToUse:   whenToUse,
		Model:       model,
		Provider:    provider,
		ContextSize: contextSize,
		Tools:       tools,
		Plugins:     plugins,
		Prompt:      prompt,
		FilePath:    path,
	}, nil, nil
}

func parseFrontmatter(content string) (map[string]string, string) {
	fm := make(map[string]string)

	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "---") {
		return fm, content
	}

	rest := trimmed[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return fm, content
	}

	block := rest[:idx]
	body := rest[idx+4:]
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

func parseList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || value == "[]" {
		return nil
	}

	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}

	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		item := strings.Trim(part, " \t\"'")
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		items = append(items, item)
	}
	slices.Sort(items)
	return items
}

func parseContextSizeField(raw string) (int, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("context_size must be a non-negative integer")
	}
	return n, nil
}
