package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/s-ramirez/kroos-core/internal/memory"
)

// memoryTool is a thin adapter over *memory.Store that exposes four
// actions (read, write, search, list) through a single OpenAI-compatible
// tool. All paths are relative to $KROOS_HOME/memory/; the store enforces the
// sandbox.
type memoryTool struct {
	store   *memory.Store
	resolve func() *memory.Store
}

func (t *memoryTool) active() *memory.Store {
	if t.resolve != nil {
		if s := t.resolve(); s != nil {
			return s
		}
	}
	return t.store
}

// MemoryParams carries arguments for every action. Only the fields that
// apply to the chosen action need to be populated; the rest are ignored.
type MemoryParams struct {
	Action string `json:"action"`

	// read / write / list
	Path string `json:"path"`

	// read
	Offset int `json:"offset"`
	Limit  int `json:"limit"`

	// write
	Content string `json:"content"`
	Mode    string `json:"mode"` // "overwrite" (default) | "append"

	// search
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`

	// list
	Dir string `json:"dir"`
}

var memoryDefinition = ToolDefinition{
	Name: "memory",
	Description: "Read/write/search/list the agent's persistent memory store ($KROOS_HOME/memory/). " +
		"Paths are relative to the memory root (e.g. \"MEMORY.md\", \"sessions/2026-04-13-1422.md\", \"vault/Knowledge/foo.md\").",
	Guidelines: []string{
		"Use action=read to recall prior context before answering questions about past sessions, user preferences, or durable facts.",
		"Use action=write with mode=append for session notes; only overwrite MEMORY.md from an explicit distill flow.",
		"Use action=search for case-insensitive literal substring scan across all markdown in memory — not semantic search; pick distinctive keywords.",
		"Use action=list to discover existing session notes and vault files before writing new ones.",
		"Paths must be relative to the memory root — never pass absolute paths.",
	},
	Parameters: JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"read", "write", "search", "list"},
			},
			"path":        map[string]any{"type": "string", "description": "Path relative to $KROOS_HOME/memory/ (read/write)"},
			"offset":      map[string]any{"type": "number", "description": "1-indexed line to start at (read only)"},
			"limit":       map[string]any{"type": "number", "description": "Maximum lines to return (read only)"},
			"content":     map[string]any{"type": "string", "description": "Content to write (write only)"},
			"mode":        map[string]any{"type": "string", "enum": []string{"overwrite", "append"}, "description": "Write mode (write only)"},
			"query":       map[string]any{"type": "string", "description": "Substring to search for (search only)"},
			"max_results": map[string]any{"type": "number", "description": "Maximum hits (search only, default 20)"},
			"dir":         map[string]any{"type": "string", "description": "Directory relative to memory root (list only; empty = root)"},
		},
		"required": []string{"action"},
	},
}

// NewMemoryTool returns the unified memory tool.
func NewMemoryTool(store *memory.Store) Tool { return &memoryTool{store: store} }

// NewMemoryToolResolving returns a memory tool that resolves its store
// per call via resolve(). When resolve returns nil the tool falls back
// to the fallback store. This lets callers route reads/writes to a
// per-conversation store while keeping a sensible default.
func NewMemoryToolResolving(resolve func() *memory.Store, fallback *memory.Store) Tool {
	return &memoryTool{store: fallback, resolve: resolve}
}

func (t *memoryTool) Definition() ToolDefinition { return memoryDefinition }

func (t *memoryTool) Execute(_ context.Context, raw json.RawMessage) (ToolResult, error) {
	var p MemoryParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return errorResult("invalid JSON arguments"), nil
	}
	switch strings.TrimSpace(p.Action) {
	case "read":
		return t.read(p), nil
	case "write":
		return t.write(p), nil
	case "search":
		return t.search(p), nil
	case "", "list":
		// list is the default when no action is given (cheapest, read-only)
		return t.list(p), nil
	default:
		return errorResultf("action must be one of: read, write, search, list (got %q)", p.Action), nil
	}
}

func (t *memoryTool) read(p MemoryParams) ToolResult {
	if strings.TrimSpace(p.Path) == "" {
		return errorResult("path is required for action=read")
	}
	data, err := t.active().Read(p.Path)
	if err != nil {
		return errorResultf("memory file not found or unreadable: %s", p.Path)
	}

	offset := p.Offset
	if offset < 1 {
		offset = 1
	}
	limit := p.Limit
	if limit <= 0 || limit > MaxReadLines {
		limit = MaxReadLines
	}

	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, limit)
	for i, line := range lines {
		lineNo := i + 1
		if lineNo < offset {
			continue
		}
		if len(out) == limit {
			break
		}
		out = append(out, fmt.Sprintf("%d\t%s", lineNo, line))
	}
	if len(out) == 0 {
		return ToolResult{Output: "(empty)"}
	}
	return ToolResult{Output: strings.Join(out, "\n")}
}

func (t *memoryTool) write(p MemoryParams) ToolResult {
	if strings.TrimSpace(p.Path) == "" {
		return errorResult("path is required for action=write")
	}
	var appendMode bool
	switch p.Mode {
	case "", "overwrite":
		appendMode = false
	case "append":
		appendMode = true
	default:
		return errorResultf("mode must be \"overwrite\" or \"append\", got %q", p.Mode)
	}
	if err := t.active().Write(p.Path, []byte(p.Content), appendMode); err != nil {
		return errorResultf("%v", err)
	}
	verb := "wrote"
	if appendMode {
		verb = "appended"
	}
	lineCount := 0
	if p.Content != "" {
		lineCount = strings.Count(p.Content, "\n") + 1
	}
	return ToolResult{Output: fmt.Sprintf("%s %d lines to memory/%s", verb, lineCount, p.Path)}
}

func (t *memoryTool) search(p MemoryParams) ToolResult {
	hits, err := t.active().Search(p.Query, p.MaxResults)
	if err != nil {
		return errorResult(err.Error())
	}
	if len(hits) == 0 {
		return ToolResult{Output: "no matches"}
	}
	lines := make([]string, 0, len(hits))
	for _, h := range hits {
		lines = append(lines, fmt.Sprintf("%s:%d: %s", h.Path, h.Line, h.Snippet))
	}
	return ToolResult{Output: strings.Join(lines, "\n")}
}

func (t *memoryTool) list(p MemoryParams) ToolResult {
	entries, err := t.active().List(p.Dir)
	if err != nil {
		return errorResult(err.Error())
	}
	if len(entries) == 0 {
		return ToolResult{Output: "(empty)"}
	}
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		kind := "f"
		if e.IsDir {
			kind = "d"
		}
		lines = append(lines, fmt.Sprintf("%s %8d  %s  %s", kind, e.Size, e.Mtime.UTC().Format(time.RFC3339), e.Name))
	}
	return ToolResult{Output: strings.Join(lines, "\n")}
}
