package main

import (
	"context"
	"strings"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/channel"
	"github.com/s-ramirez/kroos-core/internal/llm"
)

func TestModelCatalogResolve(t *testing.T) {
	cat := modelCatalog{choices: []modelChoice{{"chatgpt", "gpt-5.4"}, {"omlx", "gemma"}, {"omlx", "gpt-5.4"}}}
	cases := map[string]modelChoice{
		"2":             {"omlx", "gemma"},
		"gemma":         {"omlx", "gemma"},
		"gpt-5.4":       {"chatgpt", "gpt-5.4"},
		"omlx:qwen/x":   {"omlx", "qwen/x"},
		"unlisted":      {"chatgpt", "unlisted"},
		"mlx-org/model": {"chatgpt", "mlx-org/model"},
	}
	for in, want := range cases {
		got, err := cat.resolve(in, "chatgpt")
		if err != nil || got != want {
			t.Errorf("%q: got %v, %v want %v", in, got, err, want)
		}
	}
	if _, err := cat.resolve("9", "chatgpt"); err == nil {
		t.Error("want out-of-range error")
	}
}

func TestSessionModelCommand(t *testing.T) {
	p := &llm.Providers{Default: "chatgpt", OMLXURL: "http://127.0.0.1:1", AuthPath: t.TempDir() + "/auth.json"}
	cmd := sessionModelCommand(p, modelChoice{"chatgpt", "gpt-5.4"})
	conv := &channel.Conversation{}
	if out := cmd(context.Background(), conv, "omlx:gemma"); !strings.Contains(out, "omlx:gemma") || conv.Model != "gemma" || conv.Provider != "omlx" {
		t.Fatalf("set: %q %+v", out, conv)
	}
	// chatgpt isn't logged in here: refuse rather than break the next turn.
	if out := cmd(context.Background(), conv, "chatgpt:gpt-5.4"); !strings.HasPrefix(out, "error") || conv.Provider != "omlx" {
		t.Fatalf("not-logged-in: %q %+v", out, conv)
	}
	if cmd(context.Background(), conv, "default"); conv.Model != "" || conv.Provider != "" {
		t.Fatalf("reset: %+v", conv)
	}
}
