package channel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/skills"
)

func TestCommandExpanderReturnsRawInputWhenNoPrefix(t *testing.T) {
	e := &CommandExpander{}
	got, err := e.Expand("hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello world" {
		t.Fatalf("expected unchanged input, got %q", got)
	}
}

func TestCommandExpanderRewritesMemoryDistill(t *testing.T) {
	e := &CommandExpander{}
	got, err := e.Expand(MemoryDistillCommand)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != DistillInstruction {
		t.Fatalf("expected distill instruction, got %q", got)
	}
}

func TestCommandExpanderSkillLoadsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("skill body"), 0o644); err != nil {
		t.Fatal(err)
	}

	e := &CommandExpander{
		Skills: skills.NewRegistry([]skills.Skill{{Name: "review", FilePath: path}}),
	}
	got, err := e.Expand("/skill:review")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "skill body" {
		t.Fatalf("expected skill body, got %q", got)
	}
}

func TestCommandExpanderSkillWithArgsAppendsThem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("skill body"), 0o644); err != nil {
		t.Fatal(err)
	}

	e := &CommandExpander{
		Skills: skills.NewRegistry([]skills.Skill{{Name: "review", FilePath: path}}),
	}
	got, err := e.Expand("/skill:review PR 42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "skill body") || !strings.HasSuffix(got, "PR 42") {
		t.Fatalf("expected body + args, got %q", got)
	}
}

func TestCommandExpanderUnknownSkillReturnsError(t *testing.T) {
	e := &CommandExpander{}
	if _, err := e.Expand("/skill:missing"); err == nil {
		t.Fatal("expected error for unknown skill")
	}
}

func TestParseResetCommand(t *testing.T) {
	cases := []struct {
		in      string
		matched bool
		tail    string
	}{
		{"/new", true, ""},
		{"/reset", true, ""},
		{"/new do a thing", true, "do a thing"},
		{"/reset  with   spaces", true, "with   spaces"},
		{"/new\nmulti", true, "multi"},
		{"/newish", false, ""},
		{"/resetting", false, ""},
		{"hello /new", false, ""},
		{"", false, ""},
	}
	for _, tc := range cases {
		matched, tail := ParseResetCommand(tc.in)
		if matched != tc.matched || tail != tc.tail {
			t.Errorf("ParseResetCommand(%q) = (%v, %q), want (%v, %q)", tc.in, matched, tail, tc.matched, tc.tail)
		}
	}
}

func TestCommandExpanderRecordsSkillUsage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("skill body"), 0o644); err != nil {
		t.Fatal(err)
	}
	usage := skills.NewUsage(filepath.Join(dir, ".usage.json"))
	e := &CommandExpander{
		Skills: skills.NewRegistry([]skills.Skill{{Name: "review", FilePath: path}}),
		Usage:  usage,
	}
	if _, err := e.Expand("/skill:review"); err != nil {
		t.Fatal(err)
	}
	if got := usage.Load()["review"]; got.UseCount != 1 || got.LastUsed == nil {
		t.Fatalf("expected one recorded use, got %+v", got)
	}
}

func TestExpandMenuAliases(t *testing.T) {
	e := &CommandExpander{Skills: skills.NewRegistry(nil)}
	if got, _ := e.Expand("/distill"); got != DistillInstruction {
		t.Fatalf("/distill = %q", got)
	}
	if _, err := e.Expand("/skill missing args"); err == nil || !strings.Contains(err.Error(), `"missing"`) {
		t.Fatalf("/skill alias err = %v", err)
	}
	if got := CommandText("model", " 3 "); got != "/model 3" {
		t.Fatalf("CommandText = %q", got)
	}
	if got := CommandText("doctor", ""); got != "/doctor" {
		t.Fatalf("CommandText = %q", got)
	}
}
