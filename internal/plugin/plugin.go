package plugin

import (
	"github.com/s-ramirez/kroos-core/internal/tools"
	"github.com/s-ramirez/kroos-core/internal/types"
)

// API provides plugins with controlled access to the host system.
type API struct {
	// Log writes a diagnostic message to stderr.
	Log func(format string, args ...any)
}

// AgentLifecycleContext identifies the agent run invoking a lifecycle hook.
type AgentLifecycleContext struct {
	AgentID   string
	AgentName string
}

// Plugin is the interface all plugins implement.
type Plugin interface {
	// Name returns a unique identifier for the plugin.
	Name() string

	// Init is called once at startup with the API object.
	Init(api API)

	// Tools returns the tools this plugin provides.
	Tools() []tools.Tool

	// Hooks returns the tool hooks this plugin provides.
	Hooks() []tools.ToolHook

	// SystemPromptSection returns text to append to the system prompt.
	// Return "" for no injection.
	SystemPromptSection() string
}

// AfterTurnHook is an optional interface plugins can implement.
// It is called after the agent loop produces a text response.
type AfterTurnHook interface {
	// AfterTurn is called with the assistant's final text response.
	// Return a non-empty string to inject it as a user message and
	// continue the agent loop. Return "" to end the turn normally.
	AfterTurn(assistantText string) string
}

// AgentStartHook is an optional interface plugins can implement.
// It is called when an agent run starts processing a request.
type AgentStartHook interface {
	OnAgentStart(ctx AgentLifecycleContext)
}

// AgentEndHook is an optional interface plugins can implement.
// It is called when an agent run finishes processing a request.
type AgentEndHook interface {
	OnAgentEnd(ctx AgentLifecycleContext, finalText string, err error)
}

// UsageHook is an optional interface plugins can implement.
// It is called after each LLM response with the reported token usage
// and the configured context window for the agent (0 if unknown).
type UsageHook interface {
	OnUsage(ctx AgentLifecycleContext, usage *types.Usage, contextSize int)
}
