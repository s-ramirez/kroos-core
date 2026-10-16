package tilldone

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/s-ramirez/kroos-core/internal/tools"
)

type taskStatus string

const (
	statusIdle       taskStatus = "idle"
	statusInProgress taskStatus = "inprogress"
	statusDone       taskStatus = "done"
)

var nextStatus = map[taskStatus]taskStatus{
	statusIdle:       statusInProgress,
	statusInProgress: statusDone,
	statusDone:       statusIdle,
}

var statusIcon = map[taskStatus]string{
	statusIdle:       "○",
	statusInProgress: "●",
	statusDone:       "✓",
}

var statusLabel = map[taskStatus]string{
	statusIdle:       "idle",
	statusInProgress: "in progress",
	statusDone:       "done",
}

type task struct {
	ID     int        `json:"id"`
	Text   string     `json:"text"`
	Status taskStatus `json:"status"`
}

type tilldoneParams struct {
	Action      string   `json:"action"`
	Text        string   `json:"text"`
	Texts       []string `json:"texts"`
	Description string   `json:"description"`
	ID          *int     `json:"id"`
}

type tilldoneTool struct {
	state *TillDone
}

var tilldoneDefinition = tools.ToolDefinition{
	Name: "tilldone",
	Description: "Manage your task list. You MUST add tasks before using any other tools. " +
		"Actions: new-list (text=title, description), add (text or texts[] for batch), " +
		"toggle (id) cycles idle->inprogress->done, remove (id), update (id + text), list, clear. " +
		"Always toggle a task to inprogress before starting work on it, and to done when finished. " +
		"Use new-list to start a themed list with a title and description. " +
		"If the user's new request does not fit the current list's theme, use clear then new-list.",
	Parameters: tools.JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"new-list", "add", "toggle", "remove", "update", "list", "clear"},
				"description": "The action to perform",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "Task text (for add/update), or list title (for new-list)",
			},
			"texts": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Multiple task texts (for add). Use to batch-add several tasks.",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "List description (for new-list)",
			},
			"id": map[string]any{
				"type":        "integer",
				"description": "Task ID (for toggle/remove/update)",
			},
		},
		"required": []string{"action"},
	},
}

func (t *tilldoneTool) Definition() tools.ToolDefinition {
	return tilldoneDefinition
}

func (t *tilldoneTool) Execute(_ context.Context, raw json.RawMessage) (tools.ToolResult, error) {
	var params tilldoneParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return errResult("invalid JSON arguments"), nil
	}

	t.state.mu.Lock()
	defer t.state.mu.Unlock()

	switch params.Action {
	case "new-list":
		return t.newList(params), nil
	case "add":
		return t.add(params), nil
	case "toggle":
		return t.toggle(params), nil
	case "remove":
		return t.remove(params), nil
	case "update":
		return t.update(params), nil
	case "list":
		return t.list(), nil
	case "clear":
		return t.clear(), nil
	default:
		return errResult(fmt.Sprintf("unknown action: %s", params.Action)), nil
	}
}

func (t *tilldoneTool) newList(p tilldoneParams) tools.ToolResult {
	if p.Text == "" {
		return errResult("text (title) required for new-list")
	}

	t.state.tasks = nil
	t.state.nextID = 1
	t.state.listTitle = p.Text
	t.state.listDescription = p.Description

	msg := fmt.Sprintf("New list: %q", p.Text)
	if p.Description != "" {
		msg += " — " + p.Description
	}
	return tools.ToolResult{Output: msg}
}

func (t *tilldoneTool) add(p tilldoneParams) tools.ToolResult {
	items := p.Texts
	if len(items) == 0 && p.Text != "" {
		items = []string{p.Text}
	}
	if len(items) == 0 {
		return errResult("text or texts required for add")
	}

	added := make([]task, 0, len(items))
	for _, text := range items {
		tk := task{ID: t.state.nextID, Text: text, Status: statusIdle}
		t.state.nextID++
		t.state.tasks = append(t.state.tasks, tk)
		added = append(added, tk)
	}

	if len(added) == 1 {
		return tools.ToolResult{Output: fmt.Sprintf("Added task #%d: %s", added[0].ID, added[0].Text)}
	}

	ids := make([]string, len(added))
	for i, tk := range added {
		ids[i] = "#" + itoa(tk.ID)
	}
	return tools.ToolResult{Output: fmt.Sprintf("Added %d tasks: %s", len(added), strings.Join(ids, ", "))}
}

func (t *tilldoneTool) toggle(p tilldoneParams) tools.ToolResult {
	if p.ID == nil {
		return errResult("id required for toggle")
	}

	idx := t.findTask(*p.ID)
	if idx == -1 {
		return errResult(fmt.Sprintf("task #%d not found", *p.ID))
	}

	tk := &t.state.tasks[idx]
	prev := tk.Status
	tk.Status = nextStatus[tk.Status]

	// Enforce single in-progress: demote any other active task.
	var demoted []int
	if tk.Status == statusInProgress {
		for i := range t.state.tasks {
			if i != idx && t.state.tasks[i].Status == statusInProgress {
				t.state.tasks[i].Status = statusIdle
				demoted = append(demoted, t.state.tasks[i].ID)
			}
		}
	}

	msg := fmt.Sprintf("Task #%d: %s -> %s", tk.ID, statusLabel[prev], statusLabel[tk.Status])
	if len(demoted) > 0 {
		ids := make([]string, len(demoted))
		for i, id := range demoted {
			ids[i] = "#" + itoa(id)
		}
		msg += fmt.Sprintf("\n(Auto-paused %s -> idle. Only one task can be in progress at a time.)", strings.Join(ids, ", "))
	}
	return tools.ToolResult{Output: msg}
}

func (t *tilldoneTool) remove(p tilldoneParams) tools.ToolResult {
	if p.ID == nil {
		return errResult("id required for remove")
	}

	idx := t.findTask(*p.ID)
	if idx == -1 {
		return errResult(fmt.Sprintf("task #%d not found", *p.ID))
	}

	removed := t.state.tasks[idx]
	t.state.tasks = append(t.state.tasks[:idx], t.state.tasks[idx+1:]...)
	return tools.ToolResult{Output: fmt.Sprintf("Removed task #%d: %s", removed.ID, removed.Text)}
}

func (t *tilldoneTool) update(p tilldoneParams) tools.ToolResult {
	if p.ID == nil {
		return errResult("id required for update")
	}
	if p.Text == "" {
		return errResult("text required for update")
	}

	idx := t.findTask(*p.ID)
	if idx == -1 {
		return errResult(fmt.Sprintf("task #%d not found", *p.ID))
	}

	old := t.state.tasks[idx].Text
	t.state.tasks[idx].Text = p.Text
	return tools.ToolResult{Output: fmt.Sprintf("Updated #%d: %q -> %q", *p.ID, old, p.Text)}
}

func (t *tilldoneTool) list() tools.ToolResult {
	if len(t.state.tasks) == 0 {
		return tools.ToolResult{Output: "No tasks defined yet."}
	}

	var sb strings.Builder
	if t.state.listTitle != "" {
		sb.WriteString(t.state.listTitle)
		sb.WriteString(":\n")
	}
	for _, tk := range t.state.tasks {
		fmt.Fprintf(&sb, "[%s] #%d (%s): %s\n", statusIcon[tk.Status], tk.ID, statusLabel[tk.Status], tk.Text)
	}
	return tools.ToolResult{Output: strings.TrimRight(sb.String(), "\n")}
}

func (t *tilldoneTool) clear() tools.ToolResult {
	count := len(t.state.tasks)
	t.state.tasks = nil
	t.state.nextID = 1
	t.state.listTitle = ""
	t.state.listDescription = ""
	return tools.ToolResult{Output: fmt.Sprintf("Cleared %d task(s)", count)}
}

func (t *tilldoneTool) findTask(id int) int {
	for i, tk := range t.state.tasks {
		if tk.ID == id {
			return i
		}
	}
	return -1
}

func errResult(msg string) tools.ToolResult {
	return tools.ToolResult{Output: "error: " + msg, IsError: true}
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
