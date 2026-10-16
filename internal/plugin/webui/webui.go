package webui

import (
	"net/http"

	"github.com/s-ramirez/kroos-core/internal/plugin"
	"github.com/s-ramirez/kroos-core/internal/tools"
	"github.com/s-ramirez/kroos-core/internal/types"
)

type WebUI struct {
	bus    *EventBus
	port   int
	server *http.Server
	log    func(string, ...any)
}

func New(port int) *WebUI {
	return &WebUI{
		bus:  NewEventBus(),
		port: port,
	}
}

func (w *WebUI) Name() string { return "webui" }

func (w *WebUI) Init(api plugin.API) {
	w.log = api.Log
	go w.startServer()
	api.Log("webui listening on http://localhost:%d", w.port)
}

func (w *WebUI) Tools() []tools.Tool { return nil }

func (w *WebUI) Hooks() []tools.ToolHook {
	return []tools.ToolHook{&webuiHook{bus: w.bus}}
}

func (w *WebUI) SystemPromptSection() string { return "" }

func (w *WebUI) OnAgentStart(ctx plugin.AgentLifecycleContext) {
	w.bus.Publish(Event{
		Type:      EventAgentStart,
		AgentID:   ctx.AgentID,
		AgentName: ctx.AgentName,
	})
}

func (w *WebUI) OnAgentEnd(ctx plugin.AgentLifecycleContext, _ string, _ error) {
	w.bus.Publish(Event{
		Type:      EventAgentEnd,
		AgentID:   ctx.AgentID,
		AgentName: ctx.AgentName,
	})
}

func (w *WebUI) OnUsage(ctx plugin.AgentLifecycleContext, usage *types.Usage, contextSize int) {
	if usage == nil {
		return
	}
	w.bus.Publish(Event{
		Type:         EventAgentUsage,
		AgentID:      ctx.AgentID,
		AgentName:    ctx.AgentName,
		PromptTokens: usage.PromptTokens,
		TotalTokens:  usage.TotalTokens,
		ContextSize:  contextSize,
	})
}
