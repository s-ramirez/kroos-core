package subagents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadAgentConfig loads an optional .kroos/AGENTS.md file. Searches dirs in order;
// last found wins (project overrides home). Returns nil if no file present.
func LoadAgentConfig(dirs ...string) (*Definition, error) {
	var found *Definition
	for _, dir := range dirs {
		path := filepath.Join(dir, "AGENTS.md")
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		def, err := loadAgentFile(path)
		if err != nil {
			return nil, err
		}
		found = def
	}
	return found, nil
}

func loadAgentFile(path string) (*Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	frontmatter, body := parseFrontmatter(string(data))

	model := strings.TrimSpace(frontmatter["model"])
	if model == "" {
		return nil, fmt.Errorf("%s: model is required", path)
	}

	tools := parseList(frontmatter["tools"])
	if len(tools) == 0 {
		return nil, fmt.Errorf("%s: tools must contain at least one tool name", path)
	}

	contextSize, err := parseContextSizeField(frontmatter["context_size"])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	return &Definition{
		Name:        "orchestrator",
		Description: strings.TrimSpace(frontmatter["description"]),
		Model:       model,
		Provider:    strings.TrimSpace(frontmatter["provider"]),
		ContextSize: contextSize,
		Tools:       tools,
		Plugins:     parseList(frontmatter["plugins"]),
		Prompt:      strings.TrimSpace(body),
		FilePath:    path,
	}, nil
}
