package prompt

import (
	"strings"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/tools"
)

func TestBuildSystemPromptIncludesToolSections(t *testing.T) {
	env := Environment{
		OS:        "linux",
		Arch:      "amd64",
		Workspace: "/home/user/project",
		Timezone:  "UTC",
		Time:      "2025-01-15T10:30:00Z",
	}

	got := BuildSystemPrompt("", []tools.Tool{
		tools.NewReadTool(tools.OSFS{}),
		tools.NewWriteTool(tools.OSFS{}),
		tools.NewEditTool(tools.OSFS{}),
		tools.NewBashTool(),
		tools.NewGrepTool(),
		tools.NewLsTool(tools.OSFS{}),
		tools.NewFindTool(),
	}, nil, env, "", nil)

	for _, fragment := range []string{
		"# Environment",
		"Platform: linux/amd64",
		"Workspace: /home/user/project",
		"Current time: 2025-01-15T10:30:00Z (UTC)",
		"# Tools",
		"## read",
		"## write",
		"## edit",
		"## bash",
		"## grep",
		"## ls",
		"## find",
		"# Tool selection",
		"Always read a file before editing it.",
		"Use bash to run shell commands",
		"Use grep to search file contents",
		"Use ls to list directory contents",
		"Use find to locate files by name",
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("expected prompt to contain %q", fragment)
		}
	}
}

func TestBuildSystemPromptIncludesPluginSections(t *testing.T) {
	env := Environment{OS: "linux", Arch: "amd64"}

	got := BuildSystemPrompt("", nil, nil, env, "", []string{
		"# My Plugin\nDo something special.",
		"",
		"# Another Plugin\nMore instructions.",
	})

	if !strings.Contains(got, "# My Plugin\nDo something special.") {
		t.Fatal("expected prompt to contain first plugin section")
	}
	if !strings.Contains(got, "# Another Plugin\nMore instructions.") {
		t.Fatal("expected prompt to contain second plugin section")
	}
}
