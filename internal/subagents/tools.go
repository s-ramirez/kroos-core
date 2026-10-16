package subagents

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/s-ramirez/kroos-core/internal/tools"
)

type spawnTool struct {
	manager *Manager
}

type subagentsTool struct {
	manager *Manager
}

type spawnParams struct {
	Agent     string `json:"agent"`
	Task      string `json:"task"`
	Label     string `json:"label"`
	Wait      *bool  `json:"wait"`
	TimeoutMS int    `json:"timeout_ms"`
}

type controlParams struct {
	Action    string `json:"action"`
	RunID     string `json:"run_id"`
	TimeoutMS int    `json:"timeout_ms"`
}

var subagentsDefinition = tools.ToolDefinition{
	Name:        "subagents",
	Description: "Inspect, wait on, or cancel Kroos subagent runs.",
	Guidelines: []string{
		"Use subagents action=list to inspect active and completed child runs.",
		"Use subagents action=wait with a run_id after starting a background subagent.",
		"Use subagents action=kill to cancel a background child run that is no longer needed.",
	},
	Parameters: tools.JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"action":     map[string]any{"type": "string", "enum": []string{"list", "wait", "kill"}},
			"run_id":     map[string]any{"type": "string", "description": "Subagent run id for wait/kill"},
			"timeout_ms": map[string]any{"type": "integer", "minimum": 0, "description": "Optional wait timeout for action=wait"},
		},
	},
	SelectionRules: []tools.SelectionRule{
		{Text: "Use subagents to inspect, wait on, or cancel background child runs."},
	},
}

func NewSpawnTool(manager *Manager) tools.Tool {
	return &spawnTool{manager: manager}
}

func NewControlTool(manager *Manager) tools.Tool {
	return &subagentsTool{manager: manager}
}

func (t *spawnTool) Definition() tools.ToolDefinition {
	return buildSessionsSpawnDefinition(t.manager.Definitions())
}

func (t *spawnTool) Execute(ctx context.Context, raw json.RawMessage) (tools.ToolResult, error) {
	if t.manager == nil {
		return errResult("subagent manager is unavailable"), nil
	}

	var params spawnParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return errResultf("invalid JSON: %v", err), nil
	}
	if strings.TrimSpace(params.Agent) == "" {
		return errResult("agent is required"), nil
	}
	if strings.TrimSpace(params.Task) == "" {
		return errResult("task is required"), nil
	}

	wait := true
	if params.Wait != nil {
		wait = *params.Wait
	}

	result, err := t.manager.Spawn(ctx, SpawnRequest{
		Agent:     params.Agent,
		Task:      params.Task,
		Label:     params.Label,
		Wait:      wait,
		TimeoutMS: params.TimeoutMS,
	})
	if err != nil {
		return errResult(err.Error()), nil
	}

	if wait {
		return tools.ToolResult{
			Output: fmt.Sprintf("Subagent %s finished (%s)\n%s", result.RunID, result.Status, result.Result),
		}, nil
	}

	return tools.ToolResult{
		Output: fmt.Sprintf("Subagent %s started in background.", result.RunID),
	}, nil
}

func (t *subagentsTool) Definition() tools.ToolDefinition {
	return subagentsDefinition
}

func (t *subagentsTool) Execute(ctx context.Context, raw json.RawMessage) (tools.ToolResult, error) {
	if t.manager == nil {
		return errResult("subagent manager is unavailable"), nil
	}

	var params controlParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return errResultf("invalid JSON: %v", err), nil
	}

	switch strings.TrimSpace(params.Action) {
	case "", "list":
		return tools.ToolResult{Output: formatRunList(t.manager.List())}, nil
	case "wait":
		if strings.TrimSpace(params.RunID) == "" {
			return errResult("run_id is required for action=wait"), nil
		}
		waitCtx := ctx
		cancel := func() {}
		if params.TimeoutMS > 0 {
			waitCtx, cancel = context.WithTimeout(ctx, time.Duration(params.TimeoutMS)*time.Millisecond)
		}
		defer cancel()
		snapshot, err := t.manager.Wait(waitCtx, params.RunID)
		if err != nil {
			return errResult(err.Error()), nil
		}
		return tools.ToolResult{
			Output: fmt.Sprintf("Subagent %s finished (%s) %s\n%s", snapshot.RunID, snapshot.Status, formatUsage(snapshot), coalesceResult(snapshot)),
		}, nil
	case "kill":
		if strings.TrimSpace(params.RunID) == "" {
			return errResult("run_id is required for action=kill"), nil
		}
		snapshot, err := t.manager.Kill(params.RunID)
		if err != nil {
			return errResult(err.Error()), nil
		}
		return tools.ToolResult{
			Output: fmt.Sprintf("Subagent %s status: %s", snapshot.RunID, snapshot.Status),
		}, nil
	default:
		return errResult("action must be one of: list, wait, kill"), nil
	}
}

func formatRunList(runs []RunSnapshot) string {
	if len(runs) == 0 {
		return "No subagents."
	}

	sort.Slice(runs, func(i, j int) bool {
		return runs[i].CreatedAt.Before(runs[j].CreatedAt)
	})

	now := time.Now()
	lines := make([]string, 0, len(runs))
	for _, run := range runs {
		line := fmt.Sprintf("%s [%s] %s", run.RunID, run.Status, run.Agent)
		if run.Label != "" {
			line += " " + run.Label
		}
		if run.Task != "" {
			line += " - " + truncate(run.Task, 80)
		}
		if timing := formatTiming(run, now); timing != "" {
			line += " " + timing
		}
		if usage := formatUsage(run); usage != "" {
			line += " " + usage
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func formatTiming(snapshot RunSnapshot, now time.Time) string {
	parts := make([]string, 0, 2)
	if snapshot.StartedAt != nil {
		end := now
		if snapshot.CompletedAt != nil {
			end = *snapshot.CompletedAt
		}
		parts = append(parts, fmt.Sprintf("elapsed=%s", roundDuration(end.Sub(*snapshot.StartedAt))))
	}
	if snapshot.LastActivityAt != nil && snapshot.CompletedAt == nil {
		parts = append(parts, fmt.Sprintf("last_activity=%s_ago", roundDuration(now.Sub(*snapshot.LastActivityAt))))
	}
	if len(parts) == 0 {
		return ""
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func roundDuration(d time.Duration) time.Duration {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Second:
		return d.Round(100 * time.Millisecond)
	case d < time.Minute:
		return d.Round(time.Second)
	default:
		return d.Round(time.Minute)
	}
}

func formatUsage(snapshot RunSnapshot) string {
	if snapshot.NumLLMCalls == 0 {
		return ""
	}
	if snapshot.ContextSize > 0 {
		remaining := snapshot.ContextSize - snapshot.LastPromptTokens
		pct := float64(snapshot.LastPromptTokens) / float64(snapshot.ContextSize) * 100
		return fmt.Sprintf("[calls=%d last_prompt=%d / ctx=%d (%.1f%%) left=%d cumulative=%d]",
			snapshot.NumLLMCalls, snapshot.LastPromptTokens, snapshot.ContextSize, pct, remaining, snapshot.TotalTokens)
	}
	return fmt.Sprintf("[calls=%d last_prompt=%d cumulative=%d]",
		snapshot.NumLLMCalls, snapshot.LastPromptTokens, snapshot.TotalTokens)
}

func BuildPromptSection(defs []Definition) string {
	if len(defs) == 0 {
		return ""
	}

	sorted := append([]Definition(nil), defs...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	lines := []string{
		"# Subagents",
		"Use sessions_spawn to delegate work to a named fresh subagent.",
		"Each subagent starts with zero conversation context, so include all necessary context in the delegated task.",
		"",
		"Available subagents:",
	}
	for _, def := range sorted {
		lines = append(lines, "- "+formatAgentListing(def))
	}

	return strings.Join(lines, "\n")
}

func SearchDelegationGuidelines(defs []Definition) []string {
	if len(defs) == 0 {
		return nil
	}

	sorted := append([]Definition(nil), defs...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	lines := []string{
		"For open-ended or multi-round exploration, consider sessions_spawn with a specialized subagent instead of doing every search yourself.",
	}
	for _, def := range sorted {
		lines = append(lines, formatAgentListing(def))
	}
	return lines
}

func buildSessionsSpawnDefinition(defs []Definition) tools.ToolDefinition {
	guidelines := []string{
		"Use sessions_spawn when a task can be delegated to a focused helper agent.",
		"Always provide the agent name.",
		"Set wait to false to run a child in the background and continue working.",
		"After a background spawn, use the subagents tool to list, wait on, or cancel child runs.",
	}
	if len(defs) > 0 {
		guidelines = append(guidelines, "Available subagents:")
		for _, def := range defs {
			guidelines = append(guidelines, formatAgentListing(def))
		}
	}

	return tools.ToolDefinition{
		Name:        "sessions_spawn",
		Description: "Start a named Kroos subagent on a delegated task. The subagent always starts with fresh context. By default the tool waits for the child to finish and returns its result.",
		Guidelines:  guidelines,
		Parameters: tools.JSONSchema{
			"type": "object",
			"properties": map[string]any{
				"agent":      map[string]any{"type": "string", "description": "Subagent name to run"},
				"task":       map[string]any{"type": "string", "description": "Task for the child agent"},
				"label":      map[string]any{"type": "string", "description": "Short label for the child run"},
				"wait":       map[string]any{"type": "boolean", "description": "Wait for the child to finish before returning. Defaults to true."},
				"timeout_ms": map[string]any{"type": "integer", "minimum": 0, "description": "Optional timeout for the child run"},
			},
			"required": []string{"agent", "task"},
		},
		SelectionRules: []tools.SelectionRule{
			{Text: "Use sessions_spawn when a task can be delegated to a focused helper agent."},
			{Text: "Always provide the target subagent name and include the necessary context in the delegated task."},
			{Text: "Default to wait=true unless parallel progress is important."},
		},
	}
}

func formatAgentListing(def Definition) string {
	whenToUse := strings.TrimSpace(def.WhenToUse)
	if whenToUse == "" {
		whenToUse = strings.TrimSpace(def.Description)
	}
	if whenToUse == "" {
		whenToUse = "specialized subagent"
	}
	return fmt.Sprintf("%s: %s (Tools: %s)", def.Name, whenToUse, strings.Join(def.Tools, ", "))
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func errResult(msg string) tools.ToolResult               { return tools.ErrorResult(msg) }
func errResultf(format string, a ...any) tools.ToolResult { return tools.ErrorResultf(format, a...) }
