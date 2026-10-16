package compaction

import (
	"context"
	"strings"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/types"
)

type stubClient struct {
	reply string
	seen  []types.Message
	err   error
}

func (s *stubClient) Chat(_ context.Context, messages []types.Message, _ []types.ChatRequestTool) (*types.ChatResponse, error) {
	s.seen = messages
	if s.err != nil {
		return nil, s.err
	}
	reply := s.reply
	return &types.ChatResponse{
		Choices: []types.Choice{{Message: types.ResponseMessage{Role: "assistant", Content: &reply}}},
	}, nil
}

func TestShouldCompact(t *testing.T) {
	s := Settings{Enabled: true, ReserveTokens: 1000}
	if !ShouldCompact(9500, 10000, s) {
		t.Fatal("expected true at 9500/10000 with reserve 1000")
	}
	if ShouldCompact(8500, 10000, s) {
		t.Fatal("expected false at 8500/10000")
	}
	if ShouldCompact(9999, 10000, Settings{}) {
		t.Fatal("disabled settings must return false")
	}
}

func TestCompactReplacesOldMessages(t *testing.T) {
	old := strings.Repeat("x", 4000)
	msgs := []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: old},
		{Role: "assistant", Content: old},
		{Role: "user", Content: old},
		{Role: "assistant", Content: old},
		{Role: "user", Content: "recent"},
		{Role: "assistant", Content: "recent reply"},
	}
	client := &stubClient{reply: "## Goal\nport compaction"}
	res, err := Compact(context.Background(), client, msgs, "", Settings{KeepRecentTokens: 50}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) >= len(msgs) {
		t.Fatalf("expected fewer messages, got %d (was %d)", len(res.Messages), len(msgs))
	}
	if res.Messages[0].Role != "system" {
		t.Fatalf("system message missing")
	}
	if res.Messages[1].Role != "user" {
		t.Fatalf("expected synthetic user summary at index 1, got %q", res.Messages[1].Role)
	}
	content, _ := res.Messages[1].Content.(string)
	if !strings.Contains(content, CompactionSummaryPrefix) || !strings.Contains(content, res.Summary) {
		t.Fatalf("synthetic message missing prefix/summary: %q", content)
	}
}

func TestCompactNoopWhenNothingToCut(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
	}
	client := &stubClient{reply: "should not be called"}
	res, err := Compact(context.Background(), client, msgs, "", Settings{KeepRecentTokens: 1_000_000}, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != len(msgs) {
		t.Fatalf("expected noop, got %d messages", len(res.Messages))
	}
	if res.Summary != "" {
		t.Fatalf("expected empty summary, got %q", res.Summary)
	}
	if client.seen != nil {
		t.Fatal("client should not be called on noop")
	}
}

func TestSerializeConversation(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "ignored"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there", ToolCalls: []types.ToolCall{
			{Function: types.ToolCallFunction{Name: "read", Arguments: `{"path":"a.go"}`}},
		}},
		{Role: "tool", Content: strings.Repeat("line\n", 1000)},
	}
	out := SerializeConversation(msgs)
	if strings.Contains(out, "ignored") {
		t.Fatal("system content leaked into serialization")
	}
	if !strings.Contains(out, "[User]: hello") {
		t.Fatalf("missing user line: %q", out)
	}
	if !strings.Contains(out, "[Assistant tool calls]: read(") {
		t.Fatalf("missing tool calls line: %q", out)
	}
	if !strings.Contains(out, "[truncated") {
		t.Fatal("long tool result should be truncated")
	}
}
