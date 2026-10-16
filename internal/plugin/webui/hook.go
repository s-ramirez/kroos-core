package webui

import (
	"encoding/json"

	"github.com/s-ramirez/kroos-core/internal/tools"
)

type webuiHook struct {
	bus *EventBus
}

func (h *webuiHook) BeforeToolCall(hctx tools.HookContext, name string, params json.RawMessage) string {
	if name == "sessions_spawn" {
		var sp struct {
			Agent string `json:"agent"`
			Label string `json:"label"`
		}
		_ = json.Unmarshal(params, &sp)
		h.bus.Publish(Event{
			Type:      EventAgentSpawn,
			AgentID:   hctx.AgentID,
			AgentName: sp.Agent,
		})
	}

	h.bus.Publish(Event{
		Type:      EventToolStart,
		AgentID:   hctx.AgentID,
		AgentName: hctx.AgentName,
		ToolName:  name,
	})
	return ""
}

func (h *webuiHook) AfterToolCall(hctx tools.HookContext, name string, params json.RawMessage, result tools.ToolResult, err error) {
	status := "ok"
	if err != nil || result.IsError {
		status = "error"
	}

	h.bus.Publish(Event{
		Type:      EventToolEnd,
		AgentID:   hctx.AgentID,
		AgentName: hctx.AgentName,
		ToolName:  name,
		Status:    status,
	})

	if name == "sessions_spawn" {
		var sp struct {
			Agent string `json:"agent"`
		}
		_ = json.Unmarshal(params, &sp)
		if status == "ok" {
			h.bus.Publish(Event{
				Type:      EventAgentDone,
				AgentID:   hctx.AgentID,
				AgentName: sp.Agent,
			})
		}
	}
}
