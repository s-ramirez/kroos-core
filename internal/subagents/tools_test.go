package subagents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/llm"
	"github.com/s-ramirez/kroos-core/internal/tools"
	"github.com/s-ramirez/kroos-core/internal/types"
)

type recordingClient struct {
	response string
	model    string
	messages []types.Message
	tools    []types.ChatRequestTool
}

func (c *recordingClient) Chat(_ context.Context, messages []types.Message, toolDefs []types.ChatRequestTool) (*types.ChatResponse, error) {
	c.messages = append([]types.Message(nil), messages...)
	c.tools = append([]types.ChatRequestTool(nil), toolDefs...)
	return &types.ChatResponse{
		Choices: []types.Choice{{
			Message: types.ResponseMessage{
				Role:    "assistant",
				Content: strPtr(c.response),
			},
		}},
	}, nil
}

type testTool struct {
	name string
}

func (t testTool) Definition() tools.ToolDefinition {
	return tools.ToolDefinition{
		Name:        t.name,
		Description: t.name,
		Parameters: tools.JSONSchema{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t testTool) Execute(_ context.Context, _ json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{Output: t.name}, nil
}

func TestSpawnToolRunsNamedAgent(t *testing.T) {
	client := &recordingClient{response: "child result"}
	def := Definition{
		Name:      "scout",
		WhenToUse: "Use for repo exploration",
		Model:     "small-model",
		Tools:     []string{"read"},
		Prompt:    "You are Scout.",
	}
	manager, err := NewManager(
		func(def Definition) (llm.ChatClient, error) {
			client.model = def.Model
			return client, nil
		},
		"",
		[]Definition{def},
		[]tools.Tool{testTool{name: "read"}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{
		"agent": "scout",
		"task":  "inspect the project",
	})
	result, err := NewSpawnTool(manager).Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %s", result.Output)
	}
	if client.model != "small-model" {
		t.Fatalf("expected model override, got %q", client.model)
	}
	if !strings.Contains(result.Output, "child result") {
		t.Fatalf("expected child result in output, got %q", result.Output)
	}
	if len(client.tools) != 1 || client.tools[0].Function.Name != "read" {
		t.Fatalf("expected only read tool, got %#v", client.tools)
	}
	if len(client.messages) != 2 {
		t.Fatalf("expected two messages, got %d", len(client.messages))
	}
	if got, ok := client.messages[0].Content.(string); !ok || got != "You are Scout." {
		t.Fatalf("expected agent prompt, got %q", got)
	}
	if got, ok := client.messages[1].Content.(string); !ok || !strings.Contains(got, "inspect the project") {
		t.Fatalf("expected delegated task in user message, got %q", got)
	}

	definition := NewSpawnTool(manager).Definition()
	if len(definition.Guidelines) == 0 || !strings.Contains(strings.Join(definition.Guidelines, "\n"), "Use for repo exploration") {
		t.Fatalf("expected dynamic agent listing in sessions_spawn definition, got %#v", definition.Guidelines)
	}
}

func TestSubagentsToolListsBackgroundRuns(t *testing.T) {
	client := &recordingClient{response: "background result"}
	manager, err := NewManager(
		func(def Definition) (llm.ChatClient, error) {
			return client, nil
		},
		"",
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

	spawnRaw, _ := json.Marshal(map[string]any{
		"agent": "scout",
		"task":  "do work",
		"wait":  false,
	})
	spawnResult, err := NewSpawnTool(manager).Execute(context.Background(), spawnRaw)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if spawnResult.IsError {
		t.Fatalf("expected success, got error result: %s", spawnResult.Output)
	}

	listRaw, _ := json.Marshal(map[string]any{
		"action": "list",
	})
	listResult, err := NewControlTool(manager).Execute(context.Background(), listRaw)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if listResult.IsError {
		t.Fatalf("expected success, got error result: %s", listResult.Output)
	}
	if !strings.Contains(listResult.Output, "subagent-1") {
		t.Fatalf("expected subagent id in output, got %q", listResult.Output)
	}
	if !strings.Contains(listResult.Output, "scout") {
		t.Fatalf("expected agent name in output, got %q", listResult.Output)
	}
}

func strPtr(value string) *string {
	return &value
}
