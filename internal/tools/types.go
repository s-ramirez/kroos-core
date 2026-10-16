package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/s-ramirez/kroos-core/internal/types"
)

type JSONSchema map[string]any

type ToolDefinition struct {
	Name        string
	Description string
	Guidelines  []string
	Parameters  JSONSchema

	// SelectionRules are one-line hints the system prompt emits under
	// `# Tool selection`. Each rule may declare additional tool names it
	// requires to also be available (see SelectionRule.Requires); rules
	// whose requirements are unmet are silently omitted. This lets each
	// tool carry its own selection guidance without a central switch.
	SelectionRules []SelectionRule
}

// SelectionRule is one bullet emitted in the system prompt's tool-selection
// section. Requires names other tools that must be present for the rule to
// apply (e.g. the "always read before edit" rule needs both read and edit).
type SelectionRule struct {
	Text     string
	Requires []string
}

type ToolResult struct {
	Output  string
	IsError bool
}

type Tool interface {
	Definition() ToolDefinition
	Execute(ctx context.Context, params json.RawMessage) (ToolResult, error)
}

// ErrorResult builds an IsError tool result with a leading "error: " prefix.
// Exported so callers outside this package (subagents, plugin tools) share
// the same shape.
func ErrorResult(message string) ToolResult {
	return ToolResult{
		Output:  "error: " + message,
		IsError: true,
	}
}

// ErrorResultf is the fmt.Sprintf flavor of ErrorResult.
func ErrorResultf(format string, args ...any) ToolResult {
	return ErrorResult(fmt.Sprintf(format, args...))
}

// package-internal aliases keep the existing call sites terse.
func errorResult(message string) ToolResult           { return ErrorResult(message) }
func errorResultf(format string, a ...any) ToolResult { return ErrorResultf(format, a...) }

// shouldSkipDir reports whether a directory name should be skipped when walking
// the filesystem for grep, find, etc.
func shouldSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "__pycache__", ".cache":
		return true
	}
	return false
}

// HookContext carries identity information about the agent invoking a tool.
type HookContext struct {
	AgentID   string // "main" or "subagent-1", etc.
	AgentName string // human-readable name, e.g. "orchestrator" or "researcher"
}

// ToolHook receives notifications before and after tool execution.
// Both methods are optional — return nil from either to take no action.
type ToolHook interface {
	// BeforeToolCall is invoked before a tool executes.
	// Return a non-empty string to block execution (the string is used as the error message).
	// Return "" to allow execution to proceed.
	BeforeToolCall(hctx HookContext, name string, params json.RawMessage) string

	// AfterToolCall is invoked after a tool finishes executing.
	AfterToolCall(hctx HookContext, name string, params json.RawMessage, result ToolResult, err error)
}

type Registry struct {
	tools map[string]Tool
	list  []Tool
	hooks []ToolHook
}

func NewRegistry(available ...Tool) *Registry {
	byName := make(map[string]Tool, len(available))
	for _, tool := range available {
		byName[tool.Definition().Name] = tool
	}

	return &Registry{
		tools: byName,
		list:  available,
	}
}

// AddHook registers a hook that is notified before and after tool executions.
func (r *Registry) AddHook(hook ToolHook) {
	r.hooks = append(r.hooks, hook)
}

// RunBeforeHooks calls BeforeToolCall on all registered hooks.
// Returns a non-empty block reason if any hook blocks execution.
func (r *Registry) RunBeforeHooks(hctx HookContext, name string, params json.RawMessage) string {
	for _, hook := range r.hooks {
		if reason := hook.BeforeToolCall(hctx, name, params); reason != "" {
			return reason
		}
	}
	return ""
}

// RunAfterHooks calls AfterToolCall on all registered hooks.
func (r *Registry) RunAfterHooks(hctx HookContext, name string, params json.RawMessage, result ToolResult, err error) {
	for _, hook := range r.hooks {
		hook.AfterToolCall(hctx, name, params, result, err)
	}
}

func (r *Registry) Get(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *Registry) List() []Tool {
	out := make([]Tool, len(r.list))
	copy(out, r.list)
	return out
}

func (r *Registry) Schemas() []types.ChatRequestTool {
	schemas := make([]types.ChatRequestTool, 0, len(r.list))
	for _, tool := range r.list {
		definition := tool.Definition()
		schemas = append(schemas, types.ChatRequestTool{
			Type: "function",
			Function: types.ChatRequestFunction{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  definition.Parameters,
			},
		})
	}
	return schemas
}
