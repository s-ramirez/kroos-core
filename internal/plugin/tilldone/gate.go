package tilldone

import (
	"encoding/json"

	"github.com/s-ramirez/kroos-core/internal/tools"
)

// tilldoneGate blocks non-tilldone tool calls when task discipline is violated.
type tilldoneGate struct {
	state *TillDone
}

func (g *tilldoneGate) BeforeToolCall(_ tools.HookContext, name string, _ json.RawMessage) string {
	if name == "tilldone" {
		return ""
	}

	g.state.mu.Lock()
	defer g.state.mu.Unlock()

	// Reset nudge flag — the agent is actively working.
	g.state.nudgedThisCycle = false

	if len(g.state.tasks) == 0 {
		return "No tasks defined. You MUST use tilldone (action: new-list) and tilldone (action: add) to define your tasks before using any other tools. Plan your work first!"
	}

	allDone := true
	hasInProgress := false
	for _, t := range g.state.tasks {
		if t.Status != statusDone {
			allDone = false
		}
		if t.Status == statusInProgress {
			hasInProgress = true
		}
	}

	if allDone {
		return "All tasks are done. You MUST use tilldone (action: add) for new tasks or tilldone (action: new-list) to start a fresh list before using any other tools."
	}

	if !hasInProgress {
		return "No task is in progress. You MUST use tilldone (action: toggle) to mark a task as in-progress before doing any work."
	}

	return ""
}

func (g *tilldoneGate) AfterToolCall(_ tools.HookContext, _ string, _ json.RawMessage, _ tools.ToolResult, _ error) {
}
