package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/memory"
)

func newTestStore(t *testing.T) *memory.Store {
	t.Helper()
	return memory.NewStore(t.TempDir())
}

func execMemory(t *testing.T, tool Tool, p MemoryParams) ToolResult {
	t.Helper()
	raw, _ := json.Marshal(p)
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return res
}

func TestMemoryWriteAndRead(t *testing.T) {
	tool := NewMemoryTool(newTestStore(t))

	if res := execMemory(t, tool, MemoryParams{Action: "write", Path: "MEMORY.md", Content: "alpha\nbeta\n"}); res.IsError {
		t.Fatalf("write failed: %s", res.Output)
	}

	res := execMemory(t, tool, MemoryParams{Action: "read", Path: "MEMORY.md"})
	if res.IsError {
		t.Fatalf("read failed: %s", res.Output)
	}
	if !strings.Contains(res.Output, "1\talpha") || !strings.Contains(res.Output, "2\tbeta") {
		t.Fatalf("unexpected output: %q", res.Output)
	}
}

func TestMemoryWriteAppendMode(t *testing.T) {
	store := newTestStore(t)
	tool := NewMemoryTool(store)

	execMemory(t, tool, MemoryParams{Action: "write", Path: "sessions/s.md", Content: "a\n"})
	execMemory(t, tool, MemoryParams{Action: "write", Path: "sessions/s.md", Content: "b\n", Mode: "append"})

	data, err := store.Read("sessions/s.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "a\nb\n" {
		t.Fatalf("unexpected content: %q", data)
	}
}

func TestMemoryWriteRejectsEscape(t *testing.T) {
	tool := NewMemoryTool(newTestStore(t))
	res := execMemory(t, tool, MemoryParams{Action: "write", Path: "../escape.md", Content: "x"})
	if !res.IsError {
		t.Fatalf("expected sandbox error, got %q", res.Output)
	}
}

func TestMemoryWriteRejectsInvalidMode(t *testing.T) {
	tool := NewMemoryTool(newTestStore(t))
	res := execMemory(t, tool, MemoryParams{Action: "write", Path: "x.md", Content: "y", Mode: "delete"})
	if !res.IsError {
		t.Fatalf("expected mode error, got %q", res.Output)
	}
}

func TestMemoryReadMissing(t *testing.T) {
	tool := NewMemoryTool(newTestStore(t))
	res := execMemory(t, tool, MemoryParams{Action: "read", Path: "missing.md"})
	if !res.IsError {
		t.Fatalf("expected error, got %q", res.Output)
	}
}

func TestMemorySearchCaseInsensitive(t *testing.T) {
	store := newTestStore(t)
	if err := store.Write("MEMORY.md", []byte("User: Alice\nTimezone: UTC\n"), false); err != nil {
		t.Fatal(err)
	}
	tool := NewMemoryTool(store)
	res := execMemory(t, tool, MemoryParams{Action: "search", Query: "alice"})
	if res.IsError {
		t.Fatalf("search failed: %s", res.Output)
	}
	if !strings.Contains(res.Output, "MEMORY.md:1") {
		t.Fatalf("expected match on line 1: %q", res.Output)
	}
}

func TestMemorySearchNoMatches(t *testing.T) {
	store := newTestStore(t)
	if err := store.Write("MEMORY.md", []byte("hello\n"), false); err != nil {
		t.Fatal(err)
	}
	tool := NewMemoryTool(store)
	res := execMemory(t, tool, MemoryParams{Action: "search", Query: "zzzz"})
	if res.Output != "no matches" {
		t.Fatalf("unexpected: %q", res.Output)
	}
}

func TestMemoryListReturnsSortedEntries(t *testing.T) {
	store := newTestStore(t)
	if err := store.Write("sessions/b.md", []byte("x"), false); err != nil {
		t.Fatal(err)
	}
	if err := store.Write("sessions/a.md", []byte("x"), false); err != nil {
		t.Fatal(err)
	}
	tool := NewMemoryTool(store)
	res := execMemory(t, tool, MemoryParams{Action: "list", Dir: "sessions"})
	if res.IsError {
		t.Fatalf("list failed: %s", res.Output)
	}
	idxA := strings.Index(res.Output, "a.md")
	idxB := strings.Index(res.Output, "b.md")
	if idxA < 0 || idxB < 0 || idxA > idxB {
		t.Fatalf("expected a.md before b.md: %q", res.Output)
	}
}

func TestMemoryUnknownAction(t *testing.T) {
	tool := NewMemoryTool(newTestStore(t))
	res := execMemory(t, tool, MemoryParams{Action: "delete", Path: "x"})
	if !res.IsError {
		t.Fatalf("expected error for unknown action, got %q", res.Output)
	}
}
