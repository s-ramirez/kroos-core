package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/s-ramirez/kroos-core/internal/schedule"
)

// ScheduleTool is the model-facing surface for the persistent task scheduler.
// One tool, action enum — keeps the system-prompt footprint small while
// giving the model add/list/remove/wake. Heartbeat (if configured) is
// invisible here on purpose: it lives in the scheduler as a synthetic job,
// not in the store, so users can't accidentally clobber it.
type ScheduleTool struct {
	store *schedule.Store
}

func NewScheduleTool(store *schedule.Store) *ScheduleTool {
	return &ScheduleTool{store: store}
}

var scheduleDefinition = ToolDefinition{
	Name: "schedule",
	Description: "Manage scheduled events that fire later in this session. Use this to set reminders, " +
		"recurring check-ins, or self-paced wakeups (\"check back in 15 minutes\"). " +
		"Actions: add (create a job), list (show all), remove (delete by id), wake (sugar for a one-shot delayed event).",
	Guidelines: []string{
		"Use action=wake with delay (e.g. \"15m\") for the common case of \"remind me in N\" — it is shorthand for action=add with kind=at.",
		"Use action=add with kind=every and an interval like \"30m\" for recurring tasks. Anchor defaults to now.",
		"Use action=add with kind=at and an RFC3339 timestamp for absolute future times.",
		"Use action=add with kind=cron and a 5-field cron expression (\"min hour dom month dow\") for calendar-aligned recurring tasks (e.g. \"0 9 * * 1-5\" for weekday 9am).",
		"When a scheduled event fires, you will see a <scheduled_event> tag in a user message — treat it as a wakeup signal, not direct user input.",
		"Durations use Go syntax: \"15m\", \"1h30m\", \"24h\". Days are not supported — use \"24h\" for daily.",
		"list returns user-managed jobs only; the heartbeat (if configured via KROOS_HEARTBEAT_INTERVAL) does not appear here.",
	},
	Parameters: JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"add", "list", "remove", "wake"},
				"description": "Required. One of add/list/remove/wake.",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "Human-readable name. Required for add. Optional for wake (defaults to \"wake\").",
			},
			"kind": map[string]any{
				"type":        "string",
				"enum":        []string{"at", "every", "cron"},
				"description": "Required for add. \"at\" for one-shot, \"every\" for fixed interval, \"cron\" for calendar-aligned recurring.",
			},
			"at": map[string]any{
				"type":        "string",
				"description": "RFC3339 timestamp (e.g. \"2026-04-13T15:30:00Z\"). Required for add with kind=at.",
			},
			"every": map[string]any{
				"type":        "string",
				"description": "Interval as a Go duration (e.g. \"15m\", \"1h\", \"24h\"). Required for add with kind=every.",
			},
			"anchor": map[string]any{
				"type":        "string",
				"description": "RFC3339 timestamp setting the phase of an every job. Optional; defaults to now.",
			},
			"cron": map[string]any{
				"type":        "string",
				"description": "5-field standard cron expression (e.g. \"0 9 * * 1-5\"). Required for add with kind=cron.",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "The message delivered when the event fires. Required for add and wake.",
			},
			"wakeMode": map[string]any{
				"type":        "string",
				"enum":        []string{"now", "next-turn"},
				"description": "Optional. How aggressively the event interrupts. Defaults to \"now\".",
			},
			"id": map[string]any{
				"type":        "string",
				"description": "Job ID. Required for remove.",
			},
			"delay": map[string]any{
				"type":        "string",
				"description": "Delay as a Go duration (e.g. \"15m\"). Required for wake.",
			},
		},
		"required": []string{"action"},
	},
}

type ScheduleParams struct {
	Action   string `json:"action"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	At       string `json:"at"`
	Every    string `json:"every"`
	Anchor   string `json:"anchor"`
	Cron     string `json:"cron"`
	Text     string `json:"text"`
	WakeMode string `json:"wakeMode"`
	ID       string `json:"id"`
	Delay    string `json:"delay"`
}

func (t *ScheduleTool) Definition() ToolDefinition { return scheduleDefinition }

func (t *ScheduleTool) Execute(_ context.Context, raw json.RawMessage) (ToolResult, error) {
	var params ScheduleParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return errorResult("invalid JSON arguments"), nil
	}
	switch params.Action {
	case "add":
		return t.handleAdd(params)
	case "list":
		return t.handleList()
	case "remove":
		return t.handleRemove(params)
	case "wake":
		return t.handleWake(params)
	case "":
		return errorResult("action is required (add/list/remove/wake)"), nil
	default:
		return errorResultf("unknown action %q (must be add/list/remove/wake)", params.Action), nil
	}
}

func (t *ScheduleTool) handleAdd(p ScheduleParams) (ToolResult, error) {
	if p.Name == "" {
		return errorResult("name is required for add"), nil
	}
	if p.Text == "" {
		return errorResult("text is required for add"), nil
	}
	if p.WakeMode != "" && p.WakeMode != schedule.WakeModeNow && p.WakeMode != schedule.WakeModeNextTurn {
		return errorResultf("wakeMode must be %q or %q, got %q", schedule.WakeModeNow, schedule.WakeModeNextTurn, p.WakeMode), nil
	}

	job := schedule.Job{
		Name:     p.Name,
		Enabled:  true,
		Text:     p.Text,
		WakeMode: p.WakeMode,
	}

	switch p.Kind {
	case string(schedule.KindAt):
		if p.At == "" {
			return errorResult("at is required for add with kind=at"), nil
		}
		at, err := time.Parse(time.RFC3339, p.At)
		if err != nil {
			return errorResultf("at must be RFC3339: %v", err), nil
		}
		job.Schedule = schedule.Schedule{Kind: schedule.KindAt, At: &at}

	case string(schedule.KindEvery):
		if p.Every == "" {
			return errorResult("every is required for add with kind=every"), nil
		}
		every, err := time.ParseDuration(p.Every)
		if err != nil {
			return errorResultf("every must be a Go duration: %v", err), nil
		}
		if every <= 0 {
			return errorResult("every must be positive"), nil
		}
		anchor := t.store.Now()
		if p.Anchor != "" {
			parsed, err := time.Parse(time.RFC3339, p.Anchor)
			if err != nil {
				return errorResultf("anchor must be RFC3339: %v", err), nil
			}
			anchor = parsed
		}
		job.Schedule = schedule.Schedule{
			Kind:   schedule.KindEvery,
			Every:  schedule.Duration(every),
			Anchor: &anchor,
		}

	case string(schedule.KindCron):
		if p.Cron == "" {
			return errorResult("cron is required for add with kind=cron"), nil
		}
		if _, err := schedule.ParseCron(p.Cron); err != nil {
			return errorResultf("cron must be a 5-field expression: %v", err), nil
		}
		job.Schedule = schedule.Schedule{
			Kind: schedule.KindCron,
			Cron: p.Cron,
		}

	case "":
		return errorResult("kind is required for add (at, every, or cron)"), nil
	default:
		return errorResultf("kind must be \"at\", \"every\", or \"cron\", got %q", p.Kind), nil
	}

	added, err := t.store.Add(job)
	if err != nil {
		return errorResultf("%v", err), nil
	}
	return ToolResult{Output: formatJobLine("scheduled", added)}, nil
}

func (t *ScheduleTool) handleList() (ToolResult, error) {
	jobs := t.store.List()
	if len(jobs) == 0 {
		return ToolResult{Output: "no scheduled jobs"}, nil
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

func (t *ScheduleTool) handleRemove(p ScheduleParams) (ToolResult, error) {
	if p.ID == "" {
		return errorResult("id is required for remove"), nil
	}
	ok, err := t.store.Remove(p.ID)
	if err != nil {
		return errorResultf("%v", err), nil
	}
	if !ok {
		return errorResultf("no job with id %q", p.ID), nil
	}
	return ToolResult{Output: fmt.Sprintf("removed job %s", p.ID)}, nil
}

func (t *ScheduleTool) handleWake(p ScheduleParams) (ToolResult, error) {
	if p.Text == "" {
		return errorResult("text is required for wake"), nil
	}
	if p.Delay == "" {
		return errorResult("delay is required for wake (e.g. \"15m\")"), nil
	}
	delay, err := time.ParseDuration(p.Delay)
	if err != nil {
		return errorResultf("delay must be a Go duration: %v", err), nil
	}
	if delay < 0 {
		return errorResult("delay must be >= 0"), nil
	}
	if p.WakeMode != "" && p.WakeMode != schedule.WakeModeNow && p.WakeMode != schedule.WakeModeNextTurn {
		return errorResultf("wakeMode must be %q or %q, got %q", schedule.WakeModeNow, schedule.WakeModeNextTurn, p.WakeMode), nil
	}

	name := p.Name
	if name == "" {
		name = "wake"
	}
	at := t.store.Now().Add(delay)
	job := schedule.Job{
		Name:     name,
		Enabled:  true,
		Text:     p.Text,
		WakeMode: p.WakeMode,
		Schedule: schedule.Schedule{Kind: schedule.KindAt, At: &at},
	}
	added, err := t.store.Add(job)
	if err != nil {
		return errorResultf("%v", err), nil
	}
	return ToolResult{Output: formatJobLine(fmt.Sprintf("wake in %s", delay), added)}, nil
}

func formatJobLine(verb string, j schedule.Job) string {
	next := "?"
	if j.NextRunAt != nil {
		next = j.NextRunAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("%s [%s] %q — next run %s", verb, j.ID, j.Name, next)
}

func formatJobBlock(j schedule.Job) string {
	var b strings.Builder
	sched := describeSchedule(j.Schedule)
	next := "(none)"
	if j.NextRunAt != nil {
		next = j.NextRunAt.UTC().Format(time.RFC3339)
	}
	enabled := "enabled"
	if !j.Enabled {
		enabled = "disabled"
	}
	fmt.Fprintf(&b, "[%s] %q — %s, next %s, %s\n", j.ID, j.Name, sched, next, enabled)
	fmt.Fprintf(&b, "  text: %s", j.Text)
	return b.String()
}

func describeSchedule(s schedule.Schedule) string {
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
