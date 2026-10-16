package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/s-ramirez/kroos-core/internal/channel"
	"github.com/s-ramirez/kroos-core/internal/schedule"
	"github.com/s-ramirez/kroos-core/internal/tasks"
)

// TaskTool is the model-facing surface for scheduled work: background
// tasks (isolated agent runs with delivery) and reminders (events
// injected into the main conversation). One tool, action enum.
type TaskTool struct {
	store     *schedule.Store
	executor  *tasks.Executor
	scheduler *schedule.Scheduler
}

// NewTaskTool returns the task tool. executor may be nil, which disables
// run_now and history. scheduler, if set, exposes built-in (synthetic)
// jobs such as heartbeat and curator to list and run_now.
func NewTaskTool(store *schedule.Store, executor *tasks.Executor, scheduler *schedule.Scheduler) *TaskTool {
	return &TaskTool{store: store, executor: executor, scheduler: scheduler}
}

var taskDefinition = ToolDefinition{
	Name: "task",
	Description: "Schedule and manage background work. mode=task (default) runs the prompt later in an isolated background agent " +
		"(fresh context, its own tools) and sends its reply to the user — e.g. \"every 6h check the price of flight X and message me if under $400\". " +
		"mode=remind injects the prompt into this conversation when it fires (\"remind me in 15m\"). " +
		"Actions: add, list, remove, run_now (run a job — including built-ins like heartbeat and curator — or an ad-hoc prompt, immediately in the background), history (recent runs).",
	Guidelines: []string{
		"Write task prompts as self-contained instructions: the background agent sees none of this conversation. Include the exact goal, thresholds, and what to say when it fires.",
		"Background agents reply " + tasks.NothingToReport + " to stay silent, so for watch-style tasks state the condition that warrants a message (\"only message me if the price is under $400 or dropped since last check\").",
		"Delivery defaults to the current chat; set deliver_target (cli | imessage | discord | telegram | none) and deliver_to to route elsewhere.",
		"Schedule with exactly one of: delay (\"15m\", one-shot), at (RFC3339), every (Go duration like \"6h\"; days unsupported, use \"24h\"), or cron (5-field, e.g. \"0 9 * * 1-5\").",
		"When a reminder fires you will see a <scheduled_event> user message — treat it as a wakeup signal, not direct user input.",
		"Use history to check what a task found on recent runs before answering questions about it.",
	},
	Parameters: JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"action":         map[string]any{"type": "string", "enum": []string{"add", "list", "remove", "run_now", "history"}},
			"name":           map[string]any{"type": "string", "description": "Short human-readable name. Required for add."},
			"prompt":         map[string]any{"type": "string", "description": "Task instructions (mode=task) or reminder text (mode=remind). Required for add; for run_now without id."},
			"mode":           map[string]any{"type": "string", "enum": []string{"task", "remind"}, "description": "Default task."},
			"delay":          map[string]any{"type": "string", "description": "One-shot after this Go duration, e.g. \"15m\"."},
			"at":             map[string]any{"type": "string", "description": "One-shot at this RFC3339 time."},
			"every":          map[string]any{"type": "string", "description": "Recurring interval (Go duration)."},
			"anchor":         map[string]any{"type": "string", "description": "RFC3339 phase for every; defaults to now."},
			"cron":           map[string]any{"type": "string", "description": "5-field cron expression."},
			"deliver_target": map[string]any{"type": "string", "enum": []string{"cli", "imessage", "discord", "telegram", "none"}, "description": "Where task replies go. Defaults to the current chat."},
			"deliver_to":     map[string]any{"type": "string", "description": "Chat/channel ID or handle for messaging targets. Defaults to the current chat."},
			"tools":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional tool allowlist for the background agent."},
			"model":          map[string]any{"type": "string", "description": "Optional model override for the background agent."},
			"id":             map[string]any{"type": "string", "description": "Job ID for remove, run_now, history."},
			"limit":          map[string]any{"type": "integer", "description": "history: max runs (default 5)."},
		},
		"required": []string{"action"},
	},
}

type TaskParams struct {
	Action        string   `json:"action"`
	Name          string   `json:"name"`
	Prompt        string   `json:"prompt"`
	Mode          string   `json:"mode"`
	Delay         string   `json:"delay"`
	At            string   `json:"at"`
	Every         string   `json:"every"`
	Anchor        string   `json:"anchor"`
	Cron          string   `json:"cron"`
	DeliverTarget string   `json:"deliver_target"`
	DeliverTo     string   `json:"deliver_to"`
	Tools         []string `json:"tools"`
	Model         string   `json:"model"`
	ID            string   `json:"id"`
	Limit         int      `json:"limit"`
}

func (t *TaskTool) Definition() ToolDefinition { return taskDefinition }

func (t *TaskTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var p TaskParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return errorResult("invalid JSON arguments"), nil
	}
	switch p.Action {
	case "add":
		return t.handleAdd(ctx, p)
	case "list":
		return t.handleList()
	case "remove":
		return t.handleRemove(p)
	case "run_now":
		return t.handleRunNow(ctx, p)
	case "history":
		return t.handleHistory(p)
	case "":
		return errorResult("action is required (add/list/remove/run_now/history)"), nil
	default:
		return errorResultf("unknown action %q (must be add/list/remove/run_now/history)", p.Action), nil
	}
}

func (t *TaskTool) handleAdd(ctx context.Context, p TaskParams) (ToolResult, error) {
	if p.Name == "" {
		return errorResult("name is required for add"), nil
	}
	if p.Prompt == "" {
		return errorResult("prompt is required for add"), nil
	}
	mode := p.Mode
	if mode == "" {
		mode = schedule.ModeTask
	}
	if mode != schedule.ModeTask && mode != schedule.ModeRemind {
		return errorResultf("mode must be task or remind, got %q", mode), nil
	}
	sched, err := t.parseSchedule(p)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	job := schedule.Job{Name: p.Name, Enabled: true, Text: p.Prompt, Mode: mode, Schedule: sched}
	if mode == schedule.ModeTask {
		job.Deliver = resolveDelivery(ctx, p)
		job.Tools = p.Tools
		job.Model = p.Model
	}
	added, err := t.store.Add(job)
	if err != nil {
		return errorResultf("%v", err), nil
	}
	return ToolResult{Output: formatJobLine("scheduled", added)}, nil
}

func (t *TaskTool) parseSchedule(p TaskParams) (schedule.Schedule, error) {
	set := 0
	for _, v := range []string{p.Delay, p.At, p.Every, p.Cron} {
		if v != "" {
			set++
		}
	}
	if set != 1 {
		return schedule.Schedule{}, fmt.Errorf("set exactly one of delay, at, every, cron")
	}
	switch {
	case p.Delay != "":
		d, err := time.ParseDuration(p.Delay)
		if err != nil || d < 0 {
			return schedule.Schedule{}, fmt.Errorf("delay must be a non-negative Go duration (e.g. \"15m\")")
		}
		at := t.store.Now().Add(d)
		return schedule.Schedule{Kind: schedule.KindAt, At: &at}, nil
	case p.At != "":
		at, err := time.Parse(time.RFC3339, p.At)
		if err != nil {
			return schedule.Schedule{}, fmt.Errorf("at must be RFC3339: %v", err)
		}
		return schedule.Schedule{Kind: schedule.KindAt, At: &at}, nil
	case p.Every != "":
		every, err := time.ParseDuration(p.Every)
		if err != nil || every <= 0 {
			return schedule.Schedule{}, fmt.Errorf("every must be a positive Go duration (e.g. \"6h\")")
		}
		anchor := t.store.Now()
		if p.Anchor != "" {
			if anchor, err = time.Parse(time.RFC3339, p.Anchor); err != nil {
				return schedule.Schedule{}, fmt.Errorf("anchor must be RFC3339: %v", err)
			}
		}
		return schedule.Schedule{Kind: schedule.KindEvery, Every: schedule.Duration(every), Anchor: &anchor}, nil
	default:
		if _, err := schedule.ParseCron(p.Cron); err != nil {
			return schedule.Schedule{}, fmt.Errorf("cron must be a 5-field expression: %v", err)
		}
		return schedule.Schedule{Kind: schedule.KindCron, Cron: p.Cron}, nil
	}
}

// resolveDelivery defaults to the chat the tool was called from.
func resolveDelivery(ctx context.Context, p TaskParams) schedule.Delivery {
	d := schedule.Delivery{Target: p.DeliverTarget, To: p.DeliverTo}
	if key, ok := channel.KeyFromContext(ctx); ok {
		if d.Target == "" {
			d.Target = key.Channel
		}
		if d.To == "" && d.Target == key.Channel {
			d.To = key.Thread
		}
	}
	if d.Target == "" {
		d.Target = "cli"
	}
	return d
}

func (t *TaskTool) handleList() (ToolResult, error) {
	jobs := append(t.store.List(), t.scheduler.Synthetic()...)
	if len(jobs) == 0 {
		return ToolResult{Output: "no scheduled tasks"}, nil
	}
	var b strings.Builder
	for i, j := range jobs {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(formatJobBlock(j))
	}
	return ToolResult{Output: b.String()}, nil
}

func (t *TaskTool) handleRemove(p TaskParams) (ToolResult, error) {
	if p.ID == "" {
		return errorResult("id is required for remove"), nil
	}
	if j, ok := t.findJob(p.ID); ok && j.Synthetic {
		return errorResultf("%q is a built-in job; disable it via config instead", p.ID), nil
	}
	ok, err := t.store.Remove(p.ID)
	if err != nil {
		return errorResultf("%v", err), nil
	}
	if !ok {
		return errorResultf("no task with id %q", p.ID), nil
	}
	return ToolResult{Output: fmt.Sprintf("removed task %s", p.ID)}, nil
}

func (t *TaskTool) handleRunNow(ctx context.Context, p TaskParams) (ToolResult, error) {
	if t.executor == nil {
		return errorResult("background tasks are not available"), nil
	}
	var task tasks.Task
	if p.ID != "" {
		job, ok := t.findJob(p.ID)
		if !ok {
			return errorResultf("no task with id %q", p.ID), nil
		}
		if job.Mode != schedule.ModeTask {
			return errorResultf("task %q is a reminder; only mode=task jobs can run_now", p.ID), nil
		}
		task = tasks.FromJob(job)
	} else {
		if p.Prompt == "" {
			return errorResult("run_now needs id or prompt"), nil
		}
		name := p.Name
		if name == "" {
			name = "adhoc"
		}
		task = tasks.Task{ID: "adhoc-" + name, Name: name, Prompt: p.Prompt, Tools: p.Tools, Model: p.Model, Deliver: resolveDelivery(ctx, p)}
	}
	runID, ok := t.executor.Submit(task)
	if !ok {
		return errorResultf("task %q is already running", task.Name), nil
	}
	return ToolResult{Output: fmt.Sprintf("started %q in the background (run %s); its reply goes to %s", task.Name, runID, describeDelivery(task.Deliver))}, nil
}

func (t *TaskTool) handleHistory(p TaskParams) (ToolResult, error) {
	if t.executor == nil {
		return errorResult("background tasks are not available"), nil
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 5
	}
	runs := t.executor.History(p.ID, limit)
	if len(runs) == 0 {
		return ToolResult{Output: "no runs recorded"}, nil
	}
	var b strings.Builder
	for _, r := range runs {
		fmt.Fprintf(&b, "[%s] %q %s at %s (delivered=%t, %s)\n", r.TaskID, r.Name, r.Status,
			r.FinishedAt.Format(time.RFC3339), r.Delivered, r.FinishedAt.Sub(r.StartedAt).Round(time.Second))
		if r.Error != "" {
			fmt.Fprintf(&b, "  error: %s\n", r.Error)
		}
		if r.Summary != "" {
			fmt.Fprintf(&b, "  result: %s\n", r.Summary)
		}
	}
	return ToolResult{Output: strings.TrimRight(b.String(), "\n")}, nil
}

func (t *TaskTool) findJob(id string) (schedule.Job, bool) {
	for _, j := range append(t.store.List(), t.scheduler.Synthetic()...) {
		if j.ID == id {
			return j, true
		}
	}
	return schedule.Job{}, false
}

func describeDelivery(d schedule.Delivery) string {
	if d.To == "" {
		return d.Target
	}
	return d.Target + ":" + d.To
}

func formatJobLine(verb string, j schedule.Job) string {
	next := "?"
	if j.NextRunAt != nil {
		next = j.NextRunAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("%s [%s] %q (%s) — next run %s", verb, j.ID, j.Name, jobMode(j), next)
}

func formatJobBlock(j schedule.Job) string {
	var b strings.Builder
	next := "(none)"
	if j.NextRunAt != nil {
		next = j.NextRunAt.UTC().Format(time.RFC3339)
	}
	enabled := "enabled"
	if !j.Enabled {
		enabled = "disabled"
	}
	builtin := ""
	if j.Synthetic {
		builtin = " (built-in)"
	}
	fmt.Fprintf(&b, "[%s] %q %s%s — %s, next %s, %s\n", j.ID, j.Name, jobMode(j), builtin, DescribeSchedule(j.Schedule), next, enabled)
	if j.Mode == schedule.ModeTask {
		fmt.Fprintf(&b, "  deliver: %s\n", describeDelivery(j.Deliver))
	}
	fmt.Fprintf(&b, "  prompt: %s", j.Text)
	return b.String()
}

func jobMode(j schedule.Job) string {
	if j.Mode == "" {
		return schedule.ModeRemind
	}
	return j.Mode
}

// DescribeSchedule renders a schedule for humans ("every 6h0m0s").
func DescribeSchedule(s schedule.Schedule) string {
	switch s.Kind {
	case schedule.KindAt:
		if s.At == nil {
			return "at (unset)"
		}
		return "at " + s.At.UTC().Format(time.RFC3339)
	case schedule.KindEvery:
		return "every " + time.Duration(s.Every).String()
	case schedule.KindCron:
		return "cron " + s.Cron
	default:
		return string(s.Kind)
	}
}
