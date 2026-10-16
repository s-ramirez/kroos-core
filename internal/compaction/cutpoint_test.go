package compaction

import (
	"strings"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/types"
)

func TestFindCutPointKeepsRecentTurns(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: strings.Repeat("old", 100)}, // ~75 tokens
		{Role: "assistant", Content: strings.Repeat("old", 100)},
		{Role: "user", Content: strings.Repeat("mid", 100)},
		{Role: "assistant", Content: strings.Repeat("mid", 100)},
		{Role: "user", Content: strings.Repeat("new", 100)},
		{Role: "assistant", Content: strings.Repeat("new", 100)},
	}
	// Ask to keep ~150 tokens. The final two ~75-token messages should fit.
	cut := FindCutPoint(msgs, 1, 150)
	if cut < 3 || cut > 5 {
		t.Fatalf("cut %d outside expected range [3,5]", cut)
	}
	// Kept slice must start at user or assistant.
	if r := msgs[cut].Role; r != "user" && r != "assistant" {
		t.Fatalf("cut role %q is not a valid cut point", r)
	}
}

func TestFindCutPointSkipsToolMessages(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "old"},
		{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "t1", Function: types.ToolCallFunction{Name: "read", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "t1", Content: strings.Repeat("result", 200)},
		{Role: "assistant", Content: "done"},
		{Role: "user", Content: strings.Repeat("recent", 200)},
		{Role: "assistant", Content: strings.Repeat("reply", 200)},
	}
	cut := FindCutPoint(msgs, 1, 200)
	if cut >= len(msgs) {
		t.Fatalf("cut landed at end: %d", cut)
	}
	if msgs[cut].Role == "tool" {
		t.Fatalf("cut landed on tool message at index %d", cut)
	}
}

func TestFindCutPointEmpty(t *testing.T) {
	if got := FindCutPoint(nil, 0, 100); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}
