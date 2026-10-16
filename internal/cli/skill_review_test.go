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

func TestCuratorHookScopeAndArchive(t *testing.T) {
	h := newReadBeforeWriteHook()
	h.allowArchive = true
	h.scope = map[string]bool{"a": true}
	mk := func(op tools.SkillOp) json.RawMessage {
		raw, _ := json.Marshal(tools.SkillManageParams{Operations: []tools.SkillOp{op}})
		return raw
	}
	if r := h.BeforeToolCall(tools.HookContext{}, "skill_manage", mk(tools.SkillOp{Name: "a", Action: "archive"})); r != "" {
		t.Fatalf("archive of candidate should pass, got %q", r)
	}
	if h.BeforeToolCall(tools.HookContext{}, "skill_manage", mk(tools.SkillOp{Name: "other", Action: "archive"})) == "" {
		t.Fatal("non-candidate should be blocked")
	}
	if r := h.BeforeToolCall(tools.HookContext{}, "skill_manage", mk(tools.SkillOp{Name: "umbrella", Action: "create"})); r != "" {
		t.Fatalf("create of umbrella should pass, got %q", r)
	}
	if h.BeforeToolCall(tools.HookContext{}, "skill_manage", mk(tools.SkillOp{Name: "a", Action: "pin"})) == "" {
		t.Fatal("pin should be blocked")
	}
}

func TestCuratorHookKeepsNewUmbrellaInScope(t *testing.T) {
	h := newReadBeforeWriteHook()
	h.scope = map[string]bool{}
	create, _ := json.Marshal(tools.SkillManageParams{Operations: []tools.SkillOp{{Name: "umbrella", Action: "create"}}})
	h.AfterToolCall(tools.HookContext{}, "skill_manage", create, tools.ToolResult{}, nil)
	patch, _ := json.Marshal(tools.SkillManageParams{Operations: []tools.SkillOp{{Name: "umbrella", Action: "patch", OldString: "a", NewString: "b"}}})
	if r := h.BeforeToolCall(tools.HookContext{}, "skill_manage", patch); r != "" {
		t.Fatalf("patch of new umbrella should pass, got %q", r)
	}
}
