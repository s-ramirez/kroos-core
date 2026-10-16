package subagents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/s-ramirez/kroos-core/internal/llm"
	"github.com/s-ramirez/kroos-core/internal/tools"
	"github.com/s-ramirez/kroos-core/internal/types"
)

type loggedClient struct {
	response string
}

func (c *loggedClient) Chat(_ context.Context, _ []types.Message, _ []types.ChatRequestTool) (*types.ChatResponse, error) {
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message: types.ResponseMessage{
				Role:    "assistant",
				Content: strPtr(c.response),
			},
		}},
	}, nil
}

func TestManagerLogsSubagentTranscripts(t *testing.T) {
	logDir := t.TempDir()
	manager, err := NewManager(
		func(def Definition) (llm.ChatClient, error) {
			return &loggedClient{response: "done"}, nil
		},
		logDir,
		[]Definition{{
			Name:   "scout",
			Model:  "small-model",
			Tools:  []string{"read"},
			Prompt: "You are Scout.",
		}},
		[]tools.Tool{testTool{name: "read"}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}

	result, err := manager.Spawn(context.Background(), SpawnRequest{
		Agent: "scout",
		Task:  "inspect",
		Wait:  true,
	})
	if err != nil {
		t.Fatalf("Spawn returned error: %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("expected completed status, got %q", result.Status)
	}

	snapshots := manager.List()
	if len(snapshots) != 1 {
		t.Fatalf("expected one snapshot, got %d", len(snapshots))
	}
	if snapshots[0].StartedAt == nil {
		t.Fatalf("expected StartedAt to be set")
	}
	if snapshots[0].LastActivityAt == nil {
		t.Fatalf("expected LastActivityAt to be set")
	}

	subagentRoot := filepath.Join(logDir, "subagents")
	entries, err := os.ReadDir(subagentRoot)
	if err != nil {
		t.Fatalf("ReadDir returned error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one subagent log directory, got %d", len(entries))
	}
	if !strings.Contains(entries[0].Name(), "subagent-1_scout") {
		t.Fatalf("unexpected subagent log directory name: %q", entries[0].Name())
	}

	files, err := os.ReadDir(filepath.Join(subagentRoot, entries[0].Name()))
	if err != nil {
		t.Fatalf("ReadDir log session returned error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected request/response logs, got %d files", len(files))
	}
}

func TestFormatRunListIncludesTiming(t *testing.T) {
	now := time.Now()
	started := now.Add(-5 * time.Minute)
	lastActivity := now.Add(-30 * time.Second)
	output := formatRunList([]RunSnapshot{{
		RunID:            "subagent-1",
		Agent:            "rocky",
		Status:           "running",
		Task:             "test task",
		CreatedAt:        started,
		StartedAt:        &started,
		LastActivityAt:   &lastActivity,
		NumLLMCalls:      2,
		LastPromptTokens: 120,
		TotalTokens:      240,
	}})
	if !strings.Contains(output, "elapsed=") {
		t.Fatalf("expected elapsed timing in output: %q", output)
	}
	if !strings.Contains(output, "last_activity=") {
		t.Fatalf("expected last_activity timing in output: %q", output)
	}
}
