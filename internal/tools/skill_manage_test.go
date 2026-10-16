package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/skills"
)

const testSkillMD = "---\nname: meal-log\ndescription: Use when logging meals.\n---\n\n# Meal log\n\nAppend each meal.\n"

func newSkillManageForTest(t *testing.T) (*SkillManageTool, *skills.Registry, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "skills")
	reg := skills.NewRegistry(nil)
	reload := func() error {
		loaded, _, err := skills.LoadSkills(dir)
		if err != nil {
			return err
		}
		reg.Replace(loaded)
		return nil
	}
	return NewSkillManageTool(dir, []string{dir}, reg, "", reload), reg, dir
}

func runSkillManage(t *testing.T, tool *SkillManageTool, ops ...SkillOp) ToolResult {
	t.Helper()
	raw, _ := json.Marshal(SkillManageParams{Operations: ops})
	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return result
}

func TestSkillManageCreatePatchAndView(t *testing.T) {
	tool, reg, dir := newSkillManageForTest(t)

	res := runSkillManage(t, tool,
		SkillOp{Name: "meal-log", Action: "create", Content: testSkillMD},
		SkillOp{Name: "meal-log", Action: "write_file", FilePath: "references/macros.md", FileContent: "protein first"},
	)
	if res.IsError {
		t.Fatalf("create: %s", res.Output)
	}
	if _, ok := reg.Find("meal-log"); !ok {
		t.Fatal("expected registry reload to pick up skill")
	}

	res = runSkillManage(t, tool, SkillOp{Name: "meal-log", Action: "patch", OldString: "Append each meal.", NewString: "Append each meal with kcal."})
	if res.IsError {
		t.Fatalf("patch: %s", res.Output)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "meal-log", "SKILL.md"))
	if !strings.Contains(string(data), "with kcal") {
		t.Fatalf("patch not applied: %s", data)
	}

	view := NewSkillViewTool(reg, filepath.Join(dir, ".usage.json"))
	raw, _ := json.Marshal(SkillViewParams{Name: "meal-log"})
	out, _ := view.Execute(context.Background(), raw)
	if out.IsError || !strings.Contains(out.Output, "with kcal") || !strings.Contains(out.Output, "references/macros.md") {
		t.Fatalf("unexpected view: %s", out.Output)
	}
	if _, err := os.Stat(filepath.Join(dir, ".usage.json")); err != nil {
		t.Fatalf("expected usage file: %v", err)
	}
}

func TestSkillManageRollsBackOnFailure(t *testing.T) {
	tool, _, dir := newSkillManageForTest(t)
	if res := runSkillManage(t, tool, SkillOp{Name: "meal-log", Action: "create", Content: testSkillMD}); res.IsError {
		t.Fatalf("create: %s", res.Output)
	}

	res := runSkillManage(t, tool,
		SkillOp{Name: "meal-log", Action: "patch", OldString: "Append each meal.", NewString: "changed"},
		SkillOp{Name: "other", Action: "create", Content: testSkillMD}, // name mismatch → fails
	)
	if !res.IsError {
		t.Fatal("expected failure")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "meal-log", "SKILL.md"))
	if string(data) != testSkillMD {
		t.Fatalf("expected rollback, got %s", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "other")); !os.IsNotExist(err) {
		t.Fatal("expected new skill dir removed on rollback")
	}
}

func TestSkillManageValidation(t *testing.T) {
	tool, _, _ := newSkillManageForTest(t)
	runSkillManage(t, tool, SkillOp{Name: "meal-log", Action: "create", Content: testSkillMD})

	cases := []SkillOp{
		{Name: "Bad Name", Action: "create", Content: testSkillMD},
		{Name: "meal-log", Action: "create", Content: testSkillMD},                                  // exists
		{Name: "meal-log", Action: "patch", OldString: "nope", NewString: "x"},                      // no match
		{Name: "meal-log", Action: "patch", OldString: "name: meal-log", NewString: "name: x"},      // breaks frontmatter
		{Name: "meal-log", Action: "write_file", FilePath: "../escape.md", FileContent: "x"},        // escape
		{Name: "meal-log", Action: "write_file", FilePath: "notes.md", FileContent: "x"},            // bad subdir
		{Name: "new-one", Action: "create", Content: "---\nname: new-one\ndescription: d\n---\n\n"}, // empty body
	}
	for i, op := range cases {
		if res := runSkillManage(t, tool, op); !res.IsError {
			t.Errorf("case %d: expected error, got %s", i, res.Output)
		}
	}
	res := runSkillManage(t, tool, SkillOp{Name: "meal-log", Action: "delete"}, SkillOp{Name: "meal-log", Action: "delete"})
	if !res.IsError {
		t.Error("expected delete-with-other-ops to fail")
	}
	if res := runSkillManage(t, tool, SkillOp{Name: "meal-log", Action: "delete"}); res.IsError {
		t.Errorf("delete: %s", res.Output)
	}
}
