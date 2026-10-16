package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/s-ramirez/kroos-core/internal/skills"
	"github.com/s-ramirez/kroos-core/internal/tasks"
)

func TestBuildCuratorNoopWithFewCandidates(t *testing.T) {
	dir := t.TempDir()
	cfg := CuratorConfig{
		Usage:        skills.NewUsage(filepath.Join(dir, ".usage.json")),
		Registry:     skills.NewRegistry([]skills.Skill{{Name: "one", FilePath: filepath.Join(dir, "one", "SKILL.md")}}),
		ArchiveDir:   filepath.Join(dir, ".archive"),
		StaleAfter:   time.Hour,
		ArchiveAfter: 2 * time.Hour,
	}
	cfg.Usage.MarkCreated("one")
	spec, err := BuildCurator(nil, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	text, _, _ := spec.Agent.RunConversation(context.Background(), nil, nil)
	if text != tasks.NothingToReport {
		t.Fatalf("expected no-op, got %q", text)
	}
}

func TestBuildCuratorListsCandidates(t *testing.T) {
	dir := t.TempDir()
	reg := skills.NewRegistry([]skills.Skill{
		{Name: "git-a", Description: "Use when a", FilePath: filepath.Join(dir, "git-a", "SKILL.md")},
		{Name: "git-b", Description: "Use when b", FilePath: filepath.Join(dir, "git-b", "SKILL.md")},
		{Name: "mine", Description: "hand-written", FilePath: filepath.Join(dir, "mine", "SKILL.md")},
	})
	u := skills.NewUsage(filepath.Join(dir, ".usage.json"))
	u.MarkCreated("git-a")
	u.MarkCreated("git-b")
	spec, err := BuildCurator(nil, nil, CuratorConfig{Usage: u, Registry: reg, ArchiveDir: filepath.Join(dir, ".archive"), StaleAfter: time.Hour, ArchiveAfter: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- git-a", "- git-b"} {
		if !contains(spec.Prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
	if contains(spec.Prompt, "- mine") {
		t.Fatal("hand-written skill must not be a candidate")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
