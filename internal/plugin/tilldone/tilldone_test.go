package tilldone

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/plugin"
	"github.com/s-ramirez/kroos-core/internal/tools"
)

func setup() *TillDone {
	td := New()
	td.Init(plugin.API{
		Log: func(string, ...any) {},
	})
	return td
}

func exec(t *testing.T, td *TillDone, params any) string {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	tool := td.Tools()[0]
	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return result.Output
}

func execErr(t *testing.T, td *TillDone, params any) string {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	tool := td.Tools()[0]
	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("expected error result, got: %s", result.Output)
	}
	return result.Output
}

// ── Tool tests ────────────────────────────────────────────────────────

func TestNewList(t *testing.T) {
	td := setup()
	out := exec(t, td, map[string]any{"action": "new-list", "text": "Sprint 1", "description": "Q1 goals"})
	if !strings.Contains(out, "Sprint 1") {
		t.Fatalf("expected title in output, got: %s", out)
	}
	if !strings.Contains(out, "Q1 goals") {
		t.Fatalf("expected description in output, got: %s", out)
	}
	if td.listTitle != "Sprint 1" {
		t.Fatalf("expected listTitle to be set")
	}
}

func TestNewListRequiresText(t *testing.T) {
	td := setup()
	out := execErr(t, td, map[string]any{"action": "new-list"})
	if !strings.Contains(out, "text") {
		t.Fatalf("expected text-required error, got: %s", out)
	}
}

func TestAddSingle(t *testing.T) {
	td := setup()
	out := exec(t, td, map[string]any{"action": "add", "text": "Write tests"})
	if !strings.Contains(out, "#1") {
		t.Fatalf("expected task #1, got: %s", out)
	}
	if len(td.tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(td.tasks))
	}
	if td.tasks[0].Status != statusIdle {
		t.Fatalf("expected idle status")
	}
}

func TestAddBatch(t *testing.T) {
	td := setup()
	out := exec(t, td, map[string]any{"action": "add", "texts": []string{"Task A", "Task B", "Task C"}})
	if !strings.Contains(out, "3 tasks") {
		t.Fatalf("expected batch add message, got: %s", out)
	}
	if len(td.tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(td.tasks))
	}
}

func TestAddRequiresText(t *testing.T) {
	td := setup()
	out := execErr(t, td, map[string]any{"action": "add"})
	if !strings.Contains(out, "text") {
		t.Fatalf("expected text-required error, got: %s", out)
	}
}

func TestToggleCycle(t *testing.T) {
	td := setup()
	exec(t, td, map[string]any{"action": "add", "text": "Do something"})

	// idle -> inprogress
	id := 1
	out := exec(t, td, map[string]any{"action": "toggle", "id": id})
	if !strings.Contains(out, "in progress") {
		t.Fatalf("expected in progress, got: %s", out)
	}
	if td.tasks[0].Status != statusInProgress {
		t.Fatalf("expected inprogress status")
	}

	// inprogress -> done
	out = exec(t, td, map[string]any{"action": "toggle", "id": id})
	if !strings.Contains(out, "done") {
		t.Fatalf("expected done, got: %s", out)
	}
	if td.tasks[0].Status != statusDone {
		t.Fatalf("expected done status")
	}

	// done -> idle
	out = exec(t, td, map[string]any{"action": "toggle", "id": id})
	if !strings.Contains(out, "idle") {
		t.Fatalf("expected idle, got: %s", out)
	}
}

func TestToggleSingleInProgress(t *testing.T) {
	td := setup()
	exec(t, td, map[string]any{"action": "add", "texts": []string{"A", "B"}})

	// Toggle #1 to inprogress
	exec(t, td, map[string]any{"action": "toggle", "id": 1})
	if td.tasks[0].Status != statusInProgress {
		t.Fatal("expected #1 inprogress")
	}

	// Toggle #2 to inprogress — should demote #1
	out := exec(t, td, map[string]any{"action": "toggle", "id": 2})
	if td.tasks[0].Status != statusIdle {
		t.Fatal("expected #1 demoted to idle")
	}
	if td.tasks[1].Status != statusInProgress {
		t.Fatal("expected #2 inprogress")
	}
	if !strings.Contains(out, "Auto-paused") {
		t.Fatalf("expected auto-pause message, got: %s", out)
	}
}

func TestToggleRequiresID(t *testing.T) {
	td := setup()
	out := execErr(t, td, map[string]any{"action": "toggle"})
	if !strings.Contains(out, "id required") {
		t.Fatalf("expected id-required error, got: %s", out)
	}
}

func TestToggleNotFound(t *testing.T) {
	td := setup()
	out := execErr(t, td, map[string]any{"action": "toggle", "id": 99})
	if !strings.Contains(out, "not found") {
		t.Fatalf("expected not-found error, got: %s", out)
	}
}

func TestRemove(t *testing.T) {
	td := setup()
	exec(t, td, map[string]any{"action": "add", "text": "Remove me"})
	out := exec(t, td, map[string]any{"action": "remove", "id": 1})
	if !strings.Contains(out, "Removed") {
		t.Fatalf("expected removed message, got: %s", out)
	}
	if len(td.tasks) != 0 {
		t.Fatal("expected empty task list")
	}
}

func TestUpdate(t *testing.T) {
	td := setup()
	exec(t, td, map[string]any{"action": "add", "text": "Old text"})
	out := exec(t, td, map[string]any{"action": "update", "id": 1, "text": "New text"})
	if !strings.Contains(out, "New text") {
		t.Fatalf("expected updated text, got: %s", out)
	}
	if td.tasks[0].Text != "New text" {
		t.Fatal("expected task text to be updated")
	}
}

func TestList(t *testing.T) {
	td := setup()
	exec(t, td, map[string]any{"action": "new-list", "text": "My List"})
	exec(t, td, map[string]any{"action": "add", "texts": []string{"A", "B"}})
	out := exec(t, td, map[string]any{"action": "list"})
	if !strings.Contains(out, "My List") {
		t.Fatalf("expected list title, got: %s", out)
	}
	if !strings.Contains(out, "#1") || !strings.Contains(out, "#2") {
		t.Fatalf("expected task IDs, got: %s", out)
	}
}

func TestListEmpty(t *testing.T) {
	td := setup()
	out := exec(t, td, map[string]any{"action": "list"})
	if !strings.Contains(out, "No tasks") {
		t.Fatalf("expected no-tasks message, got: %s", out)
	}
}

func TestClear(t *testing.T) {
	td := setup()
	exec(t, td, map[string]any{"action": "add", "texts": []string{"A", "B", "C"}})
	out := exec(t, td, map[string]any{"action": "clear"})
	if !strings.Contains(out, "3 task(s)") {
		t.Fatalf("expected clear count, got: %s", out)
	}
	if len(td.tasks) != 0 {
		t.Fatal("expected empty task list")
	}
	if td.listTitle != "" {
		t.Fatal("expected list title cleared")
	}
}

func TestNewListResetsState(t *testing.T) {
	td := setup()
	exec(t, td, map[string]any{"action": "add", "text": "Old task"})
	exec(t, td, map[string]any{"action": "new-list", "text": "Fresh"})
	if len(td.tasks) != 0 {
		t.Fatal("expected tasks cleared on new-list")
	}
	if td.nextID != 1 {
		t.Fatal("expected nextID reset")
	}
}

// ── Gate tests ────────────────────────────────────────────────────────

func TestGateAllowsTilldone(t *testing.T) {
	td := setup()
	gate := td.Hooks()[0]
	if reason := gate.BeforeToolCall(tools.HookContext{}, "tilldone", nil); reason != "" {
		t.Fatalf("expected tilldone to be allowed, got: %s", reason)
	}
}

func TestGateBlocksWhenNoTasks(t *testing.T) {
	td := setup()
	gate := td.Hooks()[0]
	reason := gate.BeforeToolCall(tools.HookContext{}, "bash", nil)
	if reason == "" {
		t.Fatal("expected gate to block when no tasks")
	}
}

func TestGateBlocksWhenAllDone(t *testing.T) {
	td := setup()
	exec(t, td, map[string]any{"action": "add", "text": "Task"})
	exec(t, td, map[string]any{"action": "toggle", "id": 1}) // idle -> inprogress
	exec(t, td, map[string]any{"action": "toggle", "id": 1}) // inprogress -> done

	gate := td.Hooks()[0]
	reason := gate.BeforeToolCall(tools.HookContext{}, "bash", nil)
	if reason == "" {
		t.Fatal("expected gate to block when all done")
	}
}

func TestGateBlocksWhenNoneInProgress(t *testing.T) {
	td := setup()
	exec(t, td, map[string]any{"action": "add", "text": "Task"})

	gate := td.Hooks()[0]
	reason := gate.BeforeToolCall(tools.HookContext{}, "bash", nil)
	if reason == "" {
		t.Fatal("expected gate to block when none in progress")
	}
}

func TestGateAllowsWhenInProgress(t *testing.T) {
	td := setup()
	exec(t, td, map[string]any{"action": "add", "text": "Task"})
	exec(t, td, map[string]any{"action": "toggle", "id": 1})

	gate := td.Hooks()[0]
	reason := gate.BeforeToolCall(tools.HookContext{}, "bash", nil)
	if reason != "" {
		t.Fatalf("expected gate to allow, got: %s", reason)
	}
}

// ── AfterTurn tests ──────────────────────────────────────────────────

func TestAfterTurnNoTasks(t *testing.T) {
	td := setup()
	if msg := td.AfterTurn("done"); msg != "" {
		t.Fatalf("expected no nudge with no tasks, got: %s", msg)
	}
}

func TestAfterTurnAllDone(t *testing.T) {
	td := setup()
	exec(t, td, map[string]any{"action": "add", "text": "Task"})
	exec(t, td, map[string]any{"action": "toggle", "id": 1})
	exec(t, td, map[string]any{"action": "toggle", "id": 1})

	if msg := td.AfterTurn("done"); msg != "" {
		t.Fatalf("expected no nudge when all done, got: %s", msg)
	}
}

func TestAfterTurnNudgesOnIncomplete(t *testing.T) {
	td := setup()
	exec(t, td, map[string]any{"action": "add", "text": "Unfinished"})

	msg := td.AfterTurn("here's my response")
	if msg == "" {
		t.Fatal("expected nudge for incomplete tasks")
	}
	if !strings.Contains(msg, "incomplete") {
		t.Fatalf("expected nudge message, got: %s", msg)
	}
}

func TestAfterTurnNudgesOnlyOnce(t *testing.T) {
	td := setup()
	exec(t, td, map[string]any{"action": "add", "text": "Unfinished"})

	msg1 := td.AfterTurn("response 1")
	if msg1 == "" {
		t.Fatal("expected first nudge")
	}

	msg2 := td.AfterTurn("response 2")
	if msg2 != "" {
		t.Fatalf("expected no second nudge, got: %s", msg2)
	}
}

func TestAfterTurnResetsOnToolCall(t *testing.T) {
	td := setup()
	exec(t, td, map[string]any{"action": "add", "text": "Unfinished"})
	exec(t, td, map[string]any{"action": "toggle", "id": 1})

	// First nudge
	td.AfterTurn("response")

	// Gate call resets nudge flag
	gate := td.Hooks()[0]
	gate.BeforeToolCall(tools.HookContext{}, "bash", nil)

	// Should nudge again
	msg := td.AfterTurn("response 2")
	if msg == "" {
		t.Fatal("expected nudge after reset")
	}
}

// ── Interface compliance ─────────────────────────────────────────────

func TestImplementsPlugin(t *testing.T) {
	var _ plugin.Plugin = New()
}

func TestImplementsAfterTurnHook(t *testing.T) {
	var _ plugin.AfterTurnHook = New()
}
