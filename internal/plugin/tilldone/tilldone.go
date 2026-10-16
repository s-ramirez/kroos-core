package tilldone

import (
	"sync"

	"github.com/s-ramirez/kroos-core/internal/plugin"
	"github.com/s-ramirez/kroos-core/internal/tools"
)

// TillDone enforces task discipline: the agent must define tasks and mark
// one in-progress before it can use other tools.
type TillDone struct {
	mu              sync.Mutex
	tasks           []task
	nextID          int
	listTitle       string
	listDescription string
	nudgedThisCycle bool
	api             plugin.API
}

// New creates a new TillDone plugin instance.
func New() *TillDone {
	return &TillDone{nextID: 1}
}

func (td *TillDone) Name() string { return "tilldone" }

func (td *TillDone) Init(api plugin.API) {
	td.api = api
}

func (td *TillDone) Tools() []tools.Tool {
	return []tools.Tool{&tilldoneTool{state: td}}
}

func (td *TillDone) Hooks() []tools.ToolHook {
	return []tools.ToolHook{&tilldoneGate{state: td}}
}

func (td *TillDone) SystemPromptSection() string {
	return `# Task Management (tilldone)
You have a task management tool called "tilldone". When the user gives you work:
1. First create a task list with tilldone (action: new-list), then add tasks (action: add).
2. Before starting work on a task, mark it in-progress (action: toggle).
3. When a task is complete, mark it done (action: toggle).
4. You MUST have at least one task in-progress before using other tools.
5. After completing all tasks, present results to the user.`
}

// AfterTurn implements plugin.AfterTurnHook. It nudges the agent to continue
// if there are incomplete tasks.
func (td *TillDone) AfterTurn(assistantText string) string {
	td.mu.Lock()
	defer td.mu.Unlock()

	if len(td.tasks) == 0 || td.nudgedThisCycle {
		return ""
	}

	var incomplete []task
	for _, t := range td.tasks {
		if t.Status != statusDone {
			incomplete = append(incomplete, t)
		}
	}

	if len(incomplete) == 0 {
		return ""
	}

	td.nudgedThisCycle = true

	lines := make([]string, 0, len(incomplete)+2)
	lines = append(lines, "[System] You still have incomplete tasks:")
	lines = append(lines, "")
	for _, t := range incomplete {
		lines = append(lines, "  "+statusIcon[t.Status]+" #"+itoa(t.ID)+" ["+statusLabel[t.Status]+"]: "+t.Text)
	}
	lines = append(lines, "")
	lines = append(lines, "Continue working on them or mark them done with tilldone toggle. Don't stop until it's done!")

	result := ""
	for i, l := range lines {
		if i > 0 {
			result += "\n"
		}
		result += l
	}
	return result
}

// resetNudge is called by the gate when a new tool call arrives,
// indicating the agent is actively working.
func (td *TillDone) resetNudge() {
	td.nudgedThisCycle = false
}
