package subagents

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/s-ramirez/kroos-core/internal/cli"
	"github.com/s-ramirez/kroos-core/internal/llm"
	"github.com/s-ramirez/kroos-core/internal/plugin"
	"github.com/s-ramirez/kroos-core/internal/tools"
	"github.com/s-ramirez/kroos-core/internal/types"
)

type ClientFactory func(def Definition) (llm.ChatClient, error)

type SpawnRequest struct {
	Agent     string
	Task      string
	Label     string
	Wait      bool
	TimeoutMS int
}

type SpawnResult struct {
	RunID   string
	Status  string
	Message string
	Result  string
}

type RunSnapshot struct {
	RunID            string
	Agent            string
	Label            string
	Task             string
	Status           string
	Result           string
	Error            string
	CreatedAt        time.Time
	StartedAt        *time.Time
	CompletedAt      *time.Time
	LastActivityAt   *time.Time
	ContextSize      int
	LastPromptTokens int
	LastTotalTokens  int
	TotalTokens      int
	NumLLMCalls      int
}

type childRun struct {
	snapshot RunSnapshot
	done     chan struct{}
	cancel   context.CancelFunc
}

type Manager struct {
	clientFactory  ClientFactory
	logDir         string
	builtinTools   []tools.Tool
	baseHooks      []tools.ToolHook
	baseAgentStart []plugin.AgentStartHook
	baseAgentEnd   []plugin.AgentEndHook
	baseUsage      []plugin.UsageHook
	plugins        map[string]RuntimePlugin
	definitions    map[string]Definition
	runs           map[string]*childRun
	nextID         uint64
	mu             sync.RWMutex
}

// SetBaseObservers installs observation hooks that apply to every subagent
// run regardless of def.Plugins — mirroring how baseHooks already applies
// tool hooks globally. Intended for plugins like the webui that passively
// observe all agents.
func (m *Manager) SetBaseObservers(
	start []plugin.AgentStartHook,
	end []plugin.AgentEndHook,
	usage []plugin.UsageHook,
) {
	m.baseAgentStart = append([]plugin.AgentStartHook(nil), start...)
	m.baseAgentEnd = append([]plugin.AgentEndHook(nil), end...)
	m.baseUsage = append([]plugin.UsageHook(nil), usage...)
}

func NewManager(clientFactory ClientFactory, logDir string, defs []Definition, builtinTools []tools.Tool, baseHooks []tools.ToolHook, plugins []RuntimePlugin) (*Manager, error) {
	if clientFactory == nil {
		return nil, fmt.Errorf("client factory is required")
	}

	definitions := make(map[string]Definition, len(defs))
	for _, def := range defs {
		if _, ok := definitions[def.Name]; ok {
			return nil, fmt.Errorf("duplicate subagent %q", def.Name)
		}
		definitions[def.Name] = def
	}

	pluginMap := make(map[string]RuntimePlugin, len(plugins))
	for _, plugin := range plugins {
		pluginMap[plugin.Name] = plugin
	}

	manager := &Manager{
		clientFactory: clientFactory,
		logDir:        strings.TrimSpace(logDir),
		builtinTools:  append([]tools.Tool(nil), builtinTools...),
		baseHooks:     append([]tools.ToolHook(nil), baseHooks...),
		plugins:       pluginMap,
		definitions:   definitions,
		runs:          make(map[string]*childRun),
	}

	for _, def := range defs {
		_, _, _, err := manager.buildRuntime(def)
		if err != nil {
			return nil, fmt.Errorf("subagent %q: %w", def.Name, err)
		}
	}

	return manager, nil
}

func (m *Manager) Definitions() []Definition {
	names := make([]string, 0, len(m.definitions))
	for name := range m.definitions {
		names = append(names, name)
	}
	slices.Sort(names)

	out := make([]Definition, 0, len(names))
	for _, name := range names {
		out = append(out, m.definitions[name])
	}
	return out
}

func (m *Manager) Spawn(ctx context.Context, req SpawnRequest) (SpawnResult, error) {
	agent := strings.TrimSpace(req.Agent)
	if agent == "" {
		return SpawnResult{}, fmt.Errorf("agent is required")
	}
	task := strings.TrimSpace(req.Task)
	if task == "" {
		return SpawnResult{}, fmt.Errorf("task is required")
	}

	def, ok := m.definitions[agent]
	if !ok {
		return SpawnResult{}, fmt.Errorf("unknown subagent %q", agent)
	}

	run := m.newRun(def.Name, task, req.Label)
	go m.execute(run, req, def)

	if req.Wait {
		waitCtx := ctx
		cancel := func() {}
		if req.TimeoutMS > 0 {
			waitCtx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutMS)*time.Millisecond)
		}
		defer cancel()

		snapshot, err := m.wait(waitCtx, run.snapshot.RunID)
		if err != nil {
			return SpawnResult{}, err
		}

		message := "subagent finished"
		if snapshot.Status == "error" {
			message = "subagent failed"
		}
		return SpawnResult{
			RunID:   snapshot.RunID,
			Status:  snapshot.Status,
			Message: message,
			Result:  coalesceResult(snapshot),
		}, nil
	}

	return SpawnResult{
		RunID:   run.snapshot.RunID,
		Status:  run.snapshot.Status,
		Message: "subagent started",
	}, nil
}

func (m *Manager) List() []RunSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]RunSnapshot, 0, len(m.runs))
	for _, run := range m.runs {
		out = append(out, run.snapshot)
	}
	return out
}

func (m *Manager) Wait(ctx context.Context, runID string) (RunSnapshot, error) {
	return m.wait(ctx, runID)
}

func (m *Manager) Kill(runID string) (RunSnapshot, error) {
	m.mu.Lock()
	run, ok := m.runs[runID]
	if !ok {
		m.mu.Unlock()
		return RunSnapshot{}, fmt.Errorf("unknown subagent %q", runID)
	}
	if run.snapshot.Status != "running" {
		snapshot := run.snapshot
		m.mu.Unlock()
		return snapshot, nil
	}

	run.snapshot.Status = "canceled"
	now := time.Now()
	run.snapshot.CompletedAt = &now
	cancel := run.cancel
	snapshot := run.snapshot
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return snapshot, nil
}

type runtimeHooks struct {
	afterTurn  []plugin.AfterTurnHook
	agentStart []plugin.AgentStartHook
	agentEnd   []plugin.AgentEndHook
	usage      []plugin.UsageHook
}

func (m *Manager) buildRuntime(def Definition) (*tools.Registry, []string, runtimeHooks, error) {
	allowedPlugins := make(map[string]RuntimePlugin, len(def.Plugins))
	for _, name := range def.Plugins {
		plugin, ok := m.plugins[name]
		if !ok {
			return nil, nil, runtimeHooks{}, fmt.Errorf("unknown plugin %q", name)
		}
		allowedPlugins[name] = plugin
	}

	allTools := make(map[string]tools.Tool)
	for _, tool := range m.builtinTools {
		allTools[tool.Definition().Name] = tool
	}
	for _, name := range def.Plugins {
		for _, tool := range allowedPlugins[name].Tools {
			allTools[tool.Definition().Name] = tool
		}
	}

	var selectedTools []tools.Tool
	if len(def.Tools) == 1 && def.Tools[0] == "*" {
		selectedTools = make([]tools.Tool, 0, len(allTools))
		for _, tool := range allTools {
			selectedTools = append(selectedTools, tool)
		}
	} else {
		selectedTools = make([]tools.Tool, 0, len(def.Tools))
		for _, name := range def.Tools {
			tool, ok := allTools[name]
			if !ok {
				return nil, nil, runtimeHooks{}, fmt.Errorf("unknown tool %q", name)
			}
			selectedTools = append(selectedTools, tool)
		}
	}

	registry := tools.NewRegistry(selectedTools...)
	for _, hook := range m.baseHooks {
		registry.AddHook(hook)
	}

	var prompts []string
	var hooks runtimeHooks
	hooks.agentStart = append(hooks.agentStart, m.baseAgentStart...)
	hooks.agentEnd = append(hooks.agentEnd, m.baseAgentEnd...)
	hooks.usage = append(hooks.usage, m.baseUsage...)
	for _, name := range def.Plugins {
		p := allowedPlugins[name]
		for _, hook := range p.Hooks {
			registry.AddHook(hook)
		}
		if p.SystemPrompt != "" {
			prompts = append(prompts, p.SystemPrompt)
		}
		if p.AfterTurnHook != nil {
			hooks.afterTurn = append(hooks.afterTurn, p.AfterTurnHook)
		}
		if p.AgentStartHook != nil {
			hooks.agentStart = append(hooks.agentStart, p.AgentStartHook)
		}
		if p.AgentEndHook != nil {
			hooks.agentEnd = append(hooks.agentEnd, p.AgentEndHook)
		}
		if p.UsageHook != nil {
			hooks.usage = append(hooks.usage, p.UsageHook)
		}
	}

	return registry, prompts, hooks, nil
}

func (m *Manager) execute(run *childRun, req SpawnRequest, def Definition) {
	defer close(run.done)

	ctx := context.Background()
	if req.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutMS)*time.Millisecond)
		run.cancel = cancel
		defer cancel()
	} else {
		ctx, run.cancel = context.WithCancel(context.Background())
		defer run.cancel()
	}

	startedAt := time.Now()
	m.update(run.snapshot.RunID, func(snapshot *RunSnapshot) {
		snapshot.StartedAt = &startedAt
		snapshot.LastActivityAt = &startedAt
	})

	client, err := m.clientFactory(def)
	if err != nil {
		completedAt := time.Now()
		m.update(run.snapshot.RunID, func(snapshot *RunSnapshot) {
			snapshot.CompletedAt = &completedAt
			snapshot.Status = "error"
			snapshot.Error = err.Error()
		})
		return
	}
	client, err = m.wrapLoggedClient(run.snapshot.RunID, def.Name, client)
	if err != nil {
		completedAt := time.Now()
		m.update(run.snapshot.RunID, func(snapshot *RunSnapshot) {
			snapshot.CompletedAt = &completedAt
			snapshot.Status = "error"
			snapshot.Error = err.Error()
		})
		return
	}

	registry, _, hooks, err := m.buildRuntime(def)
	if err != nil {
		completedAt := time.Now()
		m.update(run.snapshot.RunID, func(snapshot *RunSnapshot) {
			snapshot.CompletedAt = &completedAt
			snapshot.Status = "error"
			snapshot.Error = err.Error()
		})
		return
	}

	runID := run.snapshot.RunID
	m.update(runID, func(snapshot *RunSnapshot) {
		snapshot.ContextSize = def.ContextSize
	})

	runner := cli.Runner{
		Client:          client,
		Registry:        registry,
		Skills:          nil,
		AfterTurnHooks:  hooks.afterTurn,
		AgentStartHooks: hooks.agentStart,
		AgentEndHooks:   hooks.agentEnd,
		UsageHooks:      hooks.usage,
		PluginPrompts:   nil,
		AgentID:         runID,
		AgentName:       def.Name,
		ContextSize:     def.ContextSize,
		OnUsage: func(usage *types.Usage) {
			if usage == nil {
				return
			}
			now := time.Now()
			m.update(runID, func(snapshot *RunSnapshot) {
				snapshot.NumLLMCalls++
				snapshot.LastPromptTokens = usage.PromptTokens
				snapshot.LastTotalTokens = usage.TotalTokens
				snapshot.TotalTokens += usage.TotalTokens
				snapshot.LastActivityAt = &now
			})
		},
	}

	messages := []types.Message{
		{Role: "system", Content: def.Prompt},
		{Role: "user", Content: buildDelegationMessage(def, req)},
	}

	finalText, _, err := runner.RunConversation(ctx, messages, registry.Schemas())
	completedAt := time.Now()
	m.update(run.snapshot.RunID, func(snapshot *RunSnapshot) {
		snapshot.CompletedAt = &completedAt
		if snapshot.Status == "canceled" {
			return
		}
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				snapshot.Status = "timeout"
				snapshot.Error = err.Error()
				return
			}
			if errors.Is(err, context.Canceled) {
				snapshot.Status = "canceled"
				snapshot.Error = err.Error()
				return
			}
			snapshot.Status = "error"
			snapshot.Error = err.Error()
			return
		}
		snapshot.Status = "completed"
		snapshot.Result = finalText
	})
}

func (m *Manager) newRun(agent, task, label string) *childRun {
	id := fmt.Sprintf("subagent-%d", atomic.AddUint64(&m.nextID, 1))
	run := &childRun{
		snapshot: RunSnapshot{
			RunID:     id,
			Agent:     agent,
			Label:     strings.TrimSpace(label),
			Task:      task,
			Status:    "running",
			CreatedAt: time.Now(),
		},
		done: make(chan struct{}),
	}

	m.mu.Lock()
	m.runs[id] = run
	m.mu.Unlock()
	return run
}

func (m *Manager) update(runID string, fn func(*RunSnapshot)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[runID]
	if !ok {
		return
	}
	fn(&run.snapshot)
}

func (m *Manager) wait(ctx context.Context, runID string) (RunSnapshot, error) {
	m.mu.RLock()
	run, ok := m.runs[runID]
	if !ok {
		m.mu.RUnlock()
		return RunSnapshot{}, fmt.Errorf("unknown subagent %q", runID)
	}
	done := run.done
	m.mu.RUnlock()

	select {
	case <-done:
	case <-ctx.Done():
		return RunSnapshot{}, ctx.Err()
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	run, ok = m.runs[runID]
	if !ok {
		return RunSnapshot{}, fmt.Errorf("unknown subagent %q", runID)
	}
	return run.snapshot, nil
}

func buildDelegationMessage(def Definition, req SpawnRequest) string {
	lines := []string{
		fmt.Sprintf("You are running as the %q sub-agent for the main Kroos agent.", def.Name),
		"Return a concise result for the parent agent, not end-user-oriented prose.",
	}
	if strings.TrimSpace(req.Label) != "" {
		lines = append(lines, fmt.Sprintf("Task label: %s", strings.TrimSpace(req.Label)))
	}
	lines = append(lines, "", "Task:", req.Task)
	return strings.Join(lines, "\n")
}

func (m *Manager) wrapLoggedClient(runID, agent string, client llm.ChatClient) (llm.ChatClient, error) {
	if strings.TrimSpace(m.logDir) == "" {
		return client, nil
	}

	sessionDir := filepath.Join(m.logDir, "subagents", fmt.Sprintf("%s_%s", runID, agent))
	logged, err := llm.NewLoggingClient(client, sessionDir)
	if err != nil {
		return nil, fmt.Errorf("creating subagent log directory: %w", err)
	}
	return logged, nil
}

func coalesceResult(snapshot RunSnapshot) string {
	if snapshot.Result != "" {
		return snapshot.Result
	}
	if snapshot.Error != "" {
		return "error: " + snapshot.Error
	}
	return snapshot.Status
}
