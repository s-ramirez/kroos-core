package cli

import (
	"encoding/json"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/tools"
)

func TestReadBeforeWriteHook(t *testing.T) {
	h := newReadBeforeWriteHook()
	patch, _ := json.Marshal(tools.SkillManageParams{Operations: []tools.SkillOp{{Name: "a", Action: "patch", OldString: "x", NewString: "y"}}})
	del, _ := json.Marshal(tools.SkillManageParams{Operations: []tools.SkillOp{{Name: "a", Action: "delete"}}})
	create, _ := json.Marshal(tools.SkillManageParams{Operations: []tools.SkillOp{{Name: "b", Action: "create"}}})

	if h.BeforeToolCall(tools.HookContext{}, "skill_manage", patch) == "" {
		t.Fatal("expected patch before view to be blocked")
	}
	if h.BeforeToolCall(tools.HookContext{}, "skill_manage", create) != "" {
		t.Fatal("create should not need a prior view")
	}
	view, _ := json.Marshal(tools.SkillViewParams{Name: "a"})
	h.AfterToolCall(tools.HookContext{}, "skill_view", view, tools.ToolResult{}, nil)
	if reason := h.BeforeToolCall(tools.HookContext{}, "skill_manage", patch); reason != "" {
		t.Fatalf("expected patch after view allowed, got %q", reason)
	}
	if h.BeforeToolCall(tools.HookContext{}, "skill_manage", del) == "" {
		t.Fatal("expected delete blocked")
	}
}
