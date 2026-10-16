package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/types"
)

type fakeClient struct {
	response *types.ChatResponse
	err      error
}

func (f *fakeClient) Chat(_ context.Context, _ []types.Message, _ []types.ChatRequestTool) (*types.ChatResponse, error) {
	return f.response, f.err
}

func TestLoggingClientWritesRequestAndResponse(t *testing.T) {
	dir := t.TempDir()

	content := "hello"
	inner := &fakeClient{
		response: &types.ChatResponse{
			Choices: []types.Choice{{
				Message: types.ResponseMessage{
					Role:    "assistant",
					Content: &content,
				},
			}},
		},
	}

	lc, err := NewLoggingClient(inner, dir)
	if err != nil {
		t.Fatal(err)
	}

	msgs := []types.Message{{Role: "user", Content: "test prompt"}}
	resp, err := lc.Chat(context.Background(), msgs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if types.GetResponseText(resp) != "hello" {
		t.Fatalf("unexpected response: %v", resp)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 log files, got %d", len(entries))
	}

	var hasReq, hasResp bool
	for _, e := range entries {
		if strings.Contains(e.Name(), "request") {
			hasReq = true
			data, _ := os.ReadFile(filepath.Join(dir, e.Name()))
			var parsed map[string]any
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("invalid request JSON: %v", err)
			}
			if parsed["messages"] == nil {
				t.Fatal("request log missing messages")
			}
		}
		if strings.Contains(e.Name(), "response") {
			hasResp = true
		}
	}
	if !hasReq || !hasResp {
		t.Fatal("missing request or response log file")
	}
}

func TestLoggingClientLogsError(t *testing.T) {
	dir := t.TempDir()

	inner := &fakeClient{err: fmt.Errorf("connection refused")}

	lc, err := NewLoggingClient(inner, dir)
	if err != nil {
		t.Fatal(err)
	}

	_, err = lc.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error")
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("expected 2 log files, got %d", len(entries))
	}

	// Check the response log contains the error
	for _, e := range entries {
		if strings.Contains(e.Name(), "response") {
			data, _ := os.ReadFile(filepath.Join(dir, e.Name()))
			var parsed map[string]any
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("invalid response JSON: %v", err)
			}
			if parsed["error"] == nil {
				t.Fatal("error response missing error field")
			}
		}
	}
}

func TestLoggingClientSequenceNumbers(t *testing.T) {
	dir := t.TempDir()
	content := "ok"
	inner := &fakeClient{
		response: &types.ChatResponse{
			Choices: []types.Choice{{
				Message: types.ResponseMessage{Content: &content},
			}},
		},
	}

	lc, err := NewLoggingClient(inner, dir)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		lc.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 6 {
		t.Fatalf("expected 6 log files (3 req + 3 resp), got %d", len(entries))
	}

	// Check sequence prefixes exist
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	for _, prefix := range []string{"000_", "001_", "002_"} {
		found := false
		for _, n := range names {
			if strings.HasPrefix(n, prefix) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no file with prefix %s found in %v", prefix, names)
		}
	}
}
