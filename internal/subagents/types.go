package subagents

import (
	"github.com/s-ramirez/kroos-core/internal/plugin"
	"github.com/s-ramirez/kroos-core/internal/tools"
)

type Definition struct {
	Name        string
	Description string
	WhenToUse   string
	Model       string
	Provider    string
	ContextSize int
	Tools       []string
	Plugins     []string
	Prompt      string
	FilePath    string
}

type RuntimePlugin struct {
	Name           string
	Tools          []tools.Tool
	Hooks          []tools.ToolHook
	SystemPrompt   string
	AfterTurnHook  plugin.AfterTurnHook
	AgentStartHook plugin.AgentStartHook
	AgentEndHook   plugin.AgentEndHook
	UsageHook      plugin.UsageHook
}
