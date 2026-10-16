package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBashToolRunsCommand(t *testing.T) {
	tool := NewBashTool()
	raw, _ := json.Marshal(BashParams{
		Command: "printf 'hello'",
	})

	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Output)
	}
	if !strings.Contains(result.Output, "exit_code: 0") {
		t.Fatalf("expected exit code in output, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "stdout:\nhello") {
		t.Fatalf("expected stdout in output, got %q", result.Output)
	}
}

func TestBashToolRunsPipeline(t *testing.T) {
	tool := NewBashTool()
	raw, _ := json.Marshal(BashParams{
		Command: "echo 'hello world' | tr ' ' '\\n' | sort",
	})

	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Output)
	}
	if !strings.Contains(result.Output, "hello\nworld") {
		t.Fatalf("expected sorted output, got %q", result.Output)
	}
}

func TestBashToolUsesWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("kroos"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewBashTool()
	raw, _ := json.Marshal(BashParams{
		Command: "cat sample.txt",
		Cwd:     dir,
	})

	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Output)
	}
	if !strings.Contains(result.Output, "cwd: "+dir) {
		t.Fatalf("expected cwd in output, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "stdout:\nkroos") {
		t.Fatalf("expected file contents in output, got %q", result.Output)
	}
}

func TestBashToolReportsNonZeroExitCode(t *testing.T) {
	tool := NewBashTool()
	raw, _ := json.Marshal(BashParams{
		Command: "printf 'bad' >&2; exit 9",
	})

	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("expected error result, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "exit_code: 9") {
		t.Fatalf("expected exit code in output, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "stderr:\nbad") {
		t.Fatalf("expected stderr in output, got %q", result.Output)
	}
}

func TestBashToolRejectsMissingCommand(t *testing.T) {
	tool := NewBashTool()
	raw, _ := json.Marshal(BashParams{})

	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("expected error result, got %q", result.Output)
	}
	if result.Output != "error: command is required" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}
