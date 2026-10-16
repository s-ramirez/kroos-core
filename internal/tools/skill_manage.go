package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/s-ramirez/kroos-core/internal/skills"
)

// SkillManageTool creates, patches, and deletes skills — the agent's
// procedural memory. A call is an ordered list of operations applied
// atomically: if any op fails, every touched skill directory is restored.
// New skills land in createDir; existing skills are edited where they live.
type SkillManageTool struct {
	createDir string
	roots     []string
	registry  *skills.Registry
	logPath   string
	reload    func() error
	mu        sync.Mutex
}

func NewSkillManageTool(createDir string, roots []string, registry *skills.Registry, logPath string, reload func() error) *SkillManageTool {
	return &SkillManageTool{createDir: createDir, roots: roots, registry: registry, logPath: logPath, reload: reload}
}

const (
	skillMaxDescriptionLen = 1024
	skillMaxContentChars   = 100_000
	skillMaxFileBytes      = 1 << 20
)

var skillSupportDirs = map[string]bool{"references": true, "templates": true, "scripts": true, "assets": true}

var skillManageDefinition = ToolDefinition{
	Name: "skill_manage",
	Description: "Create, update, or delete skills — your procedural memory for recurring task types. " +
		"The call is an ordered operations array (a single edit is a list of one), applied atomically: any failure rolls every touched skill back. " +
		"Ops: create (full SKILL.md text), patch (old_string/new_string — preferred; or content alone to replace SKILL.md wholesale), " +
		"write_file/remove_file (supporting files under references/, templates/, scripts/, assets/), delete (must be the only op).",
	Guidelines: []string{
		"After a non-trivial workflow (many tool calls, a tricky fix, a discovered procedure) or when the user corrects your approach, style, or format, record the lesson in the skill that governs that class of task.",
		"Prefer patching a skill you loaded this session, then an existing broader skill, then adding references/<topic>.md under one; create a new skill only when no existing skill covers the class.",
		"Name skills at the class level (e.g. \"meal-logging\"), never after one session's artifact (\"fix-bug-123\").",
		"SKILL.md = YAML frontmatter (name, description, optional disable-model-invocation) + Markdown body. Description is one line starting \"Use when <trigger>.\"",
		"Write lessons, not logs: imperative rule + why, one rule per lesson, no dates or incident narration.",
		"Load a skill with skill_view before patching it. A SKILL.md body should hold always-on rules; push topical depth into references/ and add a one-line pointer to it.",
	},
	Parameters: JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"operations": map[string]any{
				"type":        "array",
				"description": "Ordered ops; each names its target skill (lowercase alphanumeric with hyphens, max 64 chars).",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":         map[string]any{"type": "string"},
						"action":       map[string]any{"type": "string", "enum": []string{"create", "patch", "write_file", "remove_file", "delete"}},
						"content":      map[string]any{"type": "string", "description": "create / patch: full SKILL.md text (frontmatter + body). For patch it REPLACES the whole file."},
						"old_string":   map[string]any{"type": "string", "description": "patch: exact text to find."},
						"new_string":   map[string]any{"type": "string", "description": "patch: replacement; empty deletes the match."},
						"replace_all":  map[string]any{"type": "boolean", "description": "patch: replace every occurrence (default false; otherwise old_string must be unique)."},
						"file_path":    map[string]any{"type": "string", "description": "patch / write_file / remove_file: path relative to the skill dir, e.g. references/api.md. patch defaults to SKILL.md."},
						"file_content": map[string]any{"type": "string", "description": "write_file: full text of the supporting file."},
					},
					"required": []string{"name", "action"},
				},
			},
		},
		"required": []string{"operations"},
	},
}

type SkillManageParams struct {
	Operations []SkillOp `json:"operations"`
}

type SkillOp struct {
	Name        string `json:"name"`
	Action      string `json:"action"`
	Content     string `json:"content,omitempty"`
	OldString   string `json:"old_string,omitempty"`
	NewString   string `json:"new_string,omitempty"`
	ReplaceAll  bool   `json:"replace_all,omitempty"`
	FilePath    string `json:"file_path,omitempty"`
	FileContent string `json:"file_content,omitempty"`
}

func (t *SkillManageTool) Definition() ToolDefinition { return skillManageDefinition }

func (t *SkillManageTool) Execute(_ context.Context, raw json.RawMessage) (ToolResult, error) {
	var params SkillManageParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return errorResult("invalid JSON arguments"), nil
	}
	if len(params.Operations) == 0 {
		return errorResult("operations is required (a list of at least one op)"), nil
	}
	for i, op := range params.Operations {
		if !skills.ValidName(op.Name) {
			return errorResultf("op %d: invalid skill name %q (lowercase alphanumeric with hyphens, max 64 chars)", i, op.Name), nil
		}
		if op.Action == "delete" && len(params.Operations) > 1 {
			return errorResult("delete must be the only op in a call"), nil
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Snapshot every touched skill dir so a failure anywhere rolls back all.
	dirs := map[string]string{}
	var backups []skillBackup
	defer func() {
		for _, b := range backups {
			if b.tmp != "" {
				os.RemoveAll(b.tmp)
			}
		}
	}()
	for _, op := range params.Operations {
		if _, ok := dirs[op.Name]; ok {
			continue
		}
		dir := t.resolveDir(op.Name)
		dirs[op.Name] = dir
		b, err := backupDir(dir)
		if err != nil {
			return errorResultf("snapshot %s: %v", op.Name, err), nil
		}
		backups = append(backups, b)
	}

	var summary []string
	for i, op := range params.Operations {
		msg, err := t.apply(op, dirs[op.Name])
		if err != nil {
			for _, b := range backups {
				b.restore()
			}
			return errorResultf("op %d (%s %s) failed, all changes rolled back: %v", i, op.Action, op.Name, err), nil
		}
		summary = append(summary, msg)
		t.appendLog(op)
	}

	if t.reload != nil {
		if err := t.reload(); err != nil {
			return errorResultf("applied but reload failed: %v", err), nil
		}
	}
	return ToolResult{Output: strings.Join(summary, "\n")}, nil
}

// resolveDir returns the existing directory for name (loaded or on disk
// under createDir), else the createDir location a new skill would use.
func (t *SkillManageTool) resolveDir(name string) string {
	if s, ok := t.registry.Find(name); ok {
		return filepath.Dir(s.FilePath)
	}
	return filepath.Join(t.createDir, name)
}

func (t *SkillManageTool) apply(op SkillOp, dir string) (string, error) {
	skillFile := filepath.Join(dir, "SKILL.md")
	_, statErr := os.Stat(skillFile)
	exists := statErr == nil

	switch op.Action {
	case "create":
		if exists {
			return "", fmt.Errorf("skill already exists at %s (use patch)", skillFile)
		}
		if err := validateSkillMD(op.Name, op.Content); err != nil {
			return "", err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(skillFile, []byte(op.Content), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("created skill %q at %s", op.Name, skillFile), nil

	case "patch":
		if !exists {
			return "", fmt.Errorf("skill not found (use create)")
		}
		target := skillFile
		if op.FilePath != "" && op.FilePath != "SKILL.md" {
			p, err := supportPath(dir, op.FilePath)
			if err != nil {
				return "", err
			}
			target = p
		}
		var next string
		if op.OldString == "" {
			if op.Content == "" || target != skillFile {
				return "", fmt.Errorf("patch needs old_string/new_string (or content to rewrite SKILL.md)")
			}
			next = op.Content
		} else {
			data, err := os.ReadFile(target)
			if err != nil {
				return "", err
			}
			cur := string(data)
			n := strings.Count(cur, op.OldString)
			switch {
			case n == 0:
				return "", fmt.Errorf("old_string not found in %s", filepath.Base(target))
			case n > 1 && !op.ReplaceAll:
				return "", fmt.Errorf("old_string matches %d times in %s; make it unique or set replace_all", n, filepath.Base(target))
			}
			next = strings.ReplaceAll(cur, op.OldString, op.NewString)
		}
		if target == skillFile {
			if err := validateSkillMD(op.Name, next); err != nil {
				return "", err
			}
		} else if len(next) > skillMaxFileBytes {
			return "", fmt.Errorf("file exceeds %d bytes", skillMaxFileBytes)
		}
		if err := os.WriteFile(target, []byte(next), 0o644); err != nil {
			return "", err
		}
		rel, _ := filepath.Rel(dir, target)
		return fmt.Sprintf("patched %s/%s", op.Name, rel), nil

	case "write_file":
		if !exists {
			return "", fmt.Errorf("skill not found (create it first)")
		}
		p, err := supportPath(dir, op.FilePath)
		if err != nil {
			return "", err
		}
		if len(op.FileContent) > skillMaxFileBytes {
			return "", fmt.Errorf("file exceeds %d bytes", skillMaxFileBytes)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(op.FileContent), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("wrote %s/%s", op.Name, op.FilePath), nil

	case "remove_file":
		p, err := supportPath(dir, op.FilePath)
		if err != nil {
			return "", err
		}
		if err := os.Remove(p); err != nil {
			return "", err
		}
		return fmt.Sprintf("removed %s/%s", op.Name, op.FilePath), nil

	case "delete":
		if !exists {
			return "", fmt.Errorf("skill not found")
		}
		for _, root := range t.roots {
			if filepath.Clean(root) == filepath.Clean(dir) {
				return "", fmt.Errorf("refusing to delete skills root %s", dir)
			}
		}
		if err := os.RemoveAll(dir); err != nil {
			return "", err
		}
		return fmt.Sprintf("deleted skill %q", op.Name), nil
	}
	return "", fmt.Errorf("unknown action %q", op.Action)
}

// validateSkillMD checks frontmatter (name matches, one-line description)
// and a non-empty body.
func validateSkillMD(name, content string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("content is required")
	}
	if len(content) > skillMaxContentChars {
		return fmt.Errorf("SKILL.md is %d chars (limit %d); move depth into references/", len(content), skillMaxContentChars)
	}
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return fmt.Errorf("SKILL.md must start with YAML frontmatter (---)")
	}
	fm, body := skills.ParseFrontmatter(content)
	if len(fm) == 0 {
		return fmt.Errorf("frontmatter is not closed or empty")
	}
	if fm["name"] != name {
		return fmt.Errorf("frontmatter name %q must equal skill name %q", fm["name"], name)
	}
	desc := fm["description"]
	if desc == "" {
		return fmt.Errorf("frontmatter must include a one-line description")
	}
	if len(desc) > skillMaxDescriptionLen {
		return fmt.Errorf("description exceeds %d characters", skillMaxDescriptionLen)
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("SKILL.md needs a body after the frontmatter")
	}
	return nil
}

// supportPath resolves rel inside dir, requiring a known support subdir
// and refusing escapes.
func supportPath(dir, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("file_path is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("file_path must be relative to the skill dir")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	parts := strings.Split(clean, string(filepath.Separator))
	if len(parts) < 2 || !skillSupportDirs[parts[0]] {
		return "", fmt.Errorf("file_path must start with references/, templates/, scripts/, or assets/")
	}
	for _, p := range parts {
		if p == ".." {
			return "", fmt.Errorf("file_path must not contain ..")
		}
	}
	return filepath.Join(dir, clean), nil
}

type skillBackup struct {
	dir string
	tmp string // empty when dir did not exist
}

func backupDir(dir string) (skillBackup, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return skillBackup{dir: dir}, nil
	}
	tmp, err := os.MkdirTemp("", "kroos-skill-backup-")
	if err != nil {
		return skillBackup{}, err
	}
	if err := copyTree(dir, tmp); err != nil {
		os.RemoveAll(tmp)
		return skillBackup{}, err
	}
	return skillBackup{dir: dir, tmp: tmp}, nil
}

func (b skillBackup) restore() {
	os.RemoveAll(b.dir)
	if b.tmp != "" {
		if err := copyTree(b.tmp, b.dir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skill rollback of %s failed: %v\n", b.dir, err)
		}
	}
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

func (t *SkillManageTool) appendLog(op SkillOp) {
	if t.logPath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(t.logPath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(t.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s skill=%s file=%s\n", time.Now().UTC().Format(time.RFC3339), op.Action, op.Name, op.FilePath)
}
