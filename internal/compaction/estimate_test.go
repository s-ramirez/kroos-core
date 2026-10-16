package compaction

import (
	"testing"

	"github.com/s-ramirez/kroos-core/internal/types"
)

func TestEstimateTokensString(t *testing.T) {
	m := types.Message{Role: "user", Content: "abcdefgh"} // 8 chars → 2 tokens
	if got := EstimateTokens(m); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestEstimateTokensToolCalls(t *testing.T) {
	m := types.Message{
		Role:    "assistant",
		Content: "ok",
		ToolCalls: []types.ToolCall{
			{Function: types.ToolCallFunction{Name: "read", Arguments: `{"path":"a"}`}},
		},
	}
	// chars = 2 ("ok") + 4 ("read") + 12 (args) = 18 → ceil(18/4) = 5
	if got := EstimateTokens(m); got != 5 {
		t.Fatalf("want 5, got %d", got)
	}
}

func TestEstimateContextTokensHybrid(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
		{Role: "user", Content: "followup 1234"}, // 13 chars → 4 tokens
	}
	usage := &types.Usage{TotalTokens: 1000}
	// authoritative prefix 1000 + trailing msgs[3] = 1000 + 4
	got := EstimateContextTokens(msgs, usage, 2)
	if got != 1004 {
		t.Fatalf("want 1004, got %d", got)
	}
}

func TestEstimateContextTokensFallback(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: "abcd"},  // 1
		{Role: "user", Content: "efghi"}, // 2
	}
	got := EstimateContextTokens(msgs, nil, -1)
	if got != 3 {
		t.Fatalf("want 3, got %d", got)
	}
}
