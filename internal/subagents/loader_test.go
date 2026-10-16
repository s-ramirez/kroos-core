package subagents

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefinitionsReadsMarkdownFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scout.md"), []byte(`---
name: scout
description: Explore the codebase
when_to_use: Use for repo exploration
model: qwen
tools: [read, grep, ls]
plugins: []
---
You are Scout.
`), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	defs, diags, err := LoadDefinitions(dir)
	if err != nil {
		t.Fatalf("LoadDefinitions returned error: %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("expected no diagnostics, got %#v", diags)
	}
	if len(defs) != 1 {
		t.Fatalf("expected one definition, got %d", len(defs))
	}
	if defs[0].Name != "scout" {
		t.Fatalf("unexpected name: %q", defs[0].Name)
	}
	if defs[0].Model != "qwen" {
		t.Fatalf("unexpected model: %q", defs[0].Model)
	}
	if defs[0].WhenToUse != "Use for repo exploration" {
		t.Fatalf("unexpected when_to_use: %q", defs[0].WhenToUse)
	}
	if got := defs[0].Tools; len(got) != 3 || got[0] != "grep" || got[1] != "ls" || got[2] != "read" {
		t.Fatalf("unexpected tools: %#v", got)
	}
	if defs[0].Prompt != "You are Scout." {
		t.Fatalf("unexpected prompt: %q", defs[0].Prompt)
	}
}

// A missing model means "use the provider's default model".
func TestLoadDefinitionsAllowsMissingModel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scout.md"), []byte(`---
name: scout
tools: [read]
plugins: []
---
You are Scout.
`), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	defs, diags, err := LoadDefinitions(dir)
	if err != nil {
		t.Fatalf("LoadDefinitions returned error: %v", err)
	}
	if len(diags) != 0 || len(defs) != 1 || defs[0].Model != "" {
		t.Fatalf("defs=%#v diags=%v", defs, diags)
	}
}
