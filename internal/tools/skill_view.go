package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/s-ramirez/kroos-core/internal/skills"
)

// SkillViewTool loads a skill's SKILL.md (plus a listing of its support
// files) or one support file, and bumps a usage counter in a sidecar
// JSON file so skill activity can be tracked without touching SKILL.md.
type SkillViewTool struct {
	registry  *skills.Registry
	usagePath string
	mu        sync.Mutex
}

func NewSkillViewTool(registry *skills.Registry, usagePath string) *SkillViewTool {
	return &SkillViewTool{registry: registry, usagePath: usagePath}
}

var skillViewDefinition = ToolDefinition{
	Name:        "skill_view",
	Description: "Load a skill by name: returns its SKILL.md and lists its supporting files. Pass file_path to load one supporting file (e.g. references/api.md).",
	Guidelines: []string{
		"When a task matches a skill in <available_skills>, load it with skill_view before starting.",
	},
	Parameters: JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"name":      map[string]any{"type": "string", "description": "Skill name."},
			"file_path": map[string]any{"type": "string", "description": "Optional supporting file relative to the skill dir."},
		},
		"required": []string{"name"},
	},
}

type SkillViewParams struct {
	Name     string `json:"name"`
	FilePath string `json:"file_path"`
}

func (t *SkillViewTool) Definition() ToolDefinition { return skillViewDefinition }

func (t *SkillViewTool) Execute(_ context.Context, raw json.RawMessage) (ToolResult, error) {
	var params SkillViewParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return errorResult("invalid JSON arguments"), nil
	}
	s, ok := t.registry.Find(params.Name)
	if !ok {
		var names []string
		for _, sk := range t.registry.Snapshot() {
			names = append(names, sk.Name)
		}
		return errorResultf("skill %q not found; available: %s", params.Name, strings.Join(names, ", ")), nil
	}
	dir := filepath.Dir(s.FilePath)

	if params.FilePath != "" && params.FilePath != "SKILL.md" {
		p, err := supportPath(dir, params.FilePath)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return errorResultf("read %s: %v", params.FilePath, err), nil
		}
		return ToolResult{Output: string(data)}, nil
	}

	data, err := os.ReadFile(s.FilePath)
	if err != nil {
		return errorResultf("read %s: %v", s.FilePath, err), nil
	}
	t.bump(s.Name)

	var b strings.Builder
	fmt.Fprintf(&b, "<skill name=%q dir=%q>\n%s\n</skill>", s.Name, dir, strings.TrimRight(string(data), "\n"))
	if files := listSupportFiles(dir); len(files) > 0 {
		b.WriteString("\n\nSupporting files (load with skill_view file_path):\n")
		for _, f := range files {
			b.WriteString("- " + f + "\n")
		}
	}
	return ToolResult{Output: b.String()}, nil
}

func listSupportFiles(dir string) []string {
	var out []string
	for sub := range skillSupportDirs {
		root := filepath.Join(dir, sub)
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(dir, path)
			out = append(out, filepath.ToSlash(rel))
			return nil
		})
	}
	sort.Strings(out)
	return out
}

type skillUsage struct {
	UseCount int       `json:"use_count"`
	LastUsed time.Time `json:"last_used"`
}

// bump is best-effort: usage tracking must never fail a view.
func (t *SkillViewTool) bump(name string) {
	if t.usagePath == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	usage := map[string]skillUsage{}
	if data, err := os.ReadFile(t.usagePath); err == nil {
		_ = json.Unmarshal(data, &usage)
	}
	u := usage[name]
	u.UseCount++
	u.LastUsed = time.Now().UTC()
	usage[name] = u
	data, err := json.MarshalIndent(usage, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(t.usagePath), 0o755); err != nil {
		return
	}
	tmp := t.usagePath + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, t.usagePath)
	}
}
