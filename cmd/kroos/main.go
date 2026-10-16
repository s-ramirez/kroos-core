package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/s-ramirez/kroos-core/internal/channel"
	clichannel "github.com/s-ramirez/kroos-core/internal/channel/cli"
	discordchannel "github.com/s-ramirez/kroos-core/internal/channel/discord"
	imessagechannel "github.com/s-ramirez/kroos-core/internal/channel/imessage"
	"github.com/s-ramirez/kroos-core/internal/channel/sched"
	telegramchannel "github.com/s-ramirez/kroos-core/internal/channel/telegram"
	"github.com/s-ramirez/kroos-core/internal/cli"
	"github.com/s-ramirez/kroos-core/internal/compaction"
	"github.com/s-ramirez/kroos-core/internal/config"
	heartbeatPkg "github.com/s-ramirez/kroos-core/internal/heartbeat"
	"github.com/s-ramirez/kroos-core/internal/llm"
	"github.com/s-ramirez/kroos-core/internal/memory"
	"github.com/s-ramirez/kroos-core/internal/plugin"
	"github.com/s-ramirez/kroos-core/internal/plugin/webui"
	"github.com/s-ramirez/kroos-core/internal/prompt"
	"github.com/s-ramirez/kroos-core/internal/schedule"
	"github.com/s-ramirez/kroos-core/internal/skills"
	"github.com/s-ramirez/kroos-core/internal/subagents"
	"github.com/s-ramirez/kroos-core/internal/tools"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "init" {
		if err := runInit(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "init failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "config" {
		loadEnvAndConfig()
		if err := runConfig(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "config: %v\n", err)
			os.Exit(1)
		}
		return
	}

	explicit, err := parseRunFlags(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	loadEnvAndConfig()

	baseURL := os.Getenv("KROOS_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}

	model := os.Getenv("KROOS_MODEL")
	if model == "" {
		model = "gpt-5.4"
	}

	apiKey := os.Getenv("KROOS_API_KEY")

	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	agentDirs := []string{
		filepath.Join(home, ".kroos"),
		filepath.Join(cwd, ".kroos"),
	}
	agentCfg, err := subagents.LoadAgentConfig(agentDirs...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: loading agent config: %v\n", err)
		os.Exit(1)
	}
	if agentCfg != nil {
		baseURL = agentCfg.BaseURL
		if baseURL == "" {
			baseURL = os.Getenv("KROOS_BASE_URL")
			if baseURL == "" {
				baseURL = "https://api.openai.com"
			}
		}
		model = agentCfg.Model
		apiKey = ""
		if agentCfg.APIKeyEnv != "" {
			apiKey = os.Getenv(agentCfg.APIKeyEnv)
		}
		// CLI flags beat the agent config file.
		if explicit["base_url"] {
			baseURL = os.Getenv("KROOS_BASE_URL")
		}
		if explicit["model"] {
			model = os.Getenv("KROOS_MODEL")
		}
		if explicit["api_key"] {
			apiKey = os.Getenv("KROOS_API_KEY")
		}
	}

	llmTimeout := envDuration("KROOS_LLM_TIMEOUT", 300*time.Second)
	subagentLLMTimeout := envDuration("KROOS_SUBAGENT_LLM_TIMEOUT", llmTimeout)

	client := llm.NewClient(baseURL, model, &llm.Options{
		Timeout: llmTimeout,
		APIKey:  apiKey,
	})

	var chatClient llm.ChatClient = client
	var sessionDir string
	logDir := os.Getenv("KROOS_LOG_DIR")
	if logDir != "" {
		sessionDir = filepath.Join(logDir, time.Now().Format("20060102-150405"))
		lc, err := llm.NewLoggingClient(client, sessionDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to set up logging: %v\n", err)
		} else {
			chatClient = lc
			fmt.Fprintf(os.Stderr, "Logging to %s\n", sessionDir)
		}
	}

	// Initialize plugins.
	var plugins []plugin.Plugin
	if portStr := os.Getenv("KROOS_WEBUI_PORT"); portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil || port <= 0 {
			fmt.Fprintf(os.Stderr, "warning: invalid KROOS_WEBUI_PORT %q, using 8420\n", portStr)
			port = 8420
		}
		plugins = append(plugins, webui.New(port))
	}

	api := plugin.API{
		Log: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "[plugin] "+format+"\n", args...)
		},
	}
	for _, p := range plugins {
		p.Init(api)
	}

	subagentDirs := []string{
		filepath.Join(home, ".kroos", "subagents"),
		filepath.Join(cwd, ".kroos", "subagents"),
	}
	defs, subagentDiags, err := subagents.LoadDefinitions(subagentDirs...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading subagents: %v\n", err)
	}
	for _, d := range subagentDiags {
		fmt.Fprintf(os.Stderr, "warning: %s\n", d)
	}
	searchDelegationGuidelines := subagents.SearchDelegationGuidelines(defs)

	profiles, profileDiags, err := subagents.LoadProfiles(
		filepath.Join(home, ".kroos", "profiles"),
		filepath.Join(cwd, ".kroos", "profiles"),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading profiles: %v\n", err)
	}
	for _, d := range profileDiags {
		fmt.Fprintf(os.Stderr, "warning: %s\n", d)
	}

	memoryStore := memory.NewStore(filepath.Join(cwd, ".kroos", "memory"))

	// Per-(channel, thread) isolation for non-CLI channels. CLI and sched
	// keep the shared project workspace + memory; discord/imessage/etc
	// get their own workspace dir and memory dir provisioned on first
	// inbound message.
	kroosStateDir := filepath.Join(cwd, ".kroos", "state")
	kroosWorkspacesDir := filepath.Join(cwd, ".kroos", "workspaces")
	kroosMemoryDir := filepath.Join(cwd, ".kroos", "memory")
	provisioner := &threadProvisioner{
		exempt:         map[string]bool{clichannel.Name: true, sched.Name: true},
		workspacesRoot: kroosWorkspacesDir,
		memoryRoot:     kroosMemoryDir,
	}
	convStore := channel.NewPersistentStore(kroosStateDir, provisioner)

	// runnerRef is populated after the Runner is constructed; the memory
	// tool closes over it to route each call to the turn's active store.
	var runnerRef *cli.Runner

	scheduleStore, err := schedule.NewStore(filepath.Join(cwd, ".kroos", "schedule"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading schedule store: %v\n", err)
	}
	var scheduler *schedule.Scheduler
	if scheduleStore != nil {
		scheduler = schedule.NewScheduler(scheduleStore, 16)
	}

	hbCfg, err := heartbeatPkg.ConfigFromEnv(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v; heartbeat disabled\n", err)
	}
	heartbeatPkg.Register(scheduler, hbCfg, os.Stderr)

	imsgCfg, imsgReason, err := imessagechannel.ConfigFromEnv(os.Getenv, envBool)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v; iMessage channel disabled\n", err)
	} else if imsgReason != "" {
		fmt.Fprintf(os.Stderr, "iMessage channel disabled (%s)\n", imsgReason)
	}

	discordCfg, discReason, err := discordchannel.ConfigFromEnv(os.Getenv, envBool)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v; Discord channel disabled\n", err)
	} else if discReason != "" {
		fmt.Fprintf(os.Stderr, "Discord channel disabled (%s)\n", discReason)
	}

	telegramCfg, tgReason, err := telegramchannel.ConfigFromEnv(os.Getenv, envBool)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v; Telegram channel disabled\n", err)
	} else if tgReason != "" {
		fmt.Fprintf(os.Stderr, "Telegram channel disabled (%s)\n", tgReason)
	}

	// Profiles with senders: any open their bound group threads to non-owners.
	for _, p := range profiles {
		if p.Senders != subagents.SendersAny {
			continue
		}
		switch {
		case p.Channel == "discord" && discordCfg != nil:
			discordCfg.OpenChannels = append(discordCfg.OpenChannels, p.Threads...)
		case p.Channel == "imessage" && imsgCfg != nil:
			imsgCfg.OpenChats = append(imsgCfg.OpenChats, p.Threads...)
		case p.Channel == "telegram" && telegramCfg != nil:
			telegramCfg.OpenChats = append(telegramCfg.OpenChats, p.Threads...)
		}
	}

	builtinTools := []tools.Tool{
		tools.NewReadTool(tools.OSFS{}),
		tools.NewWriteTool(tools.OSFS{}),
		tools.NewEditTool(tools.OSFS{}),
		tools.NewBashTool(),
		tools.NewGrepTool(searchDelegationGuidelines...),
		tools.NewLsTool(tools.OSFS{}),
		tools.NewFindTool(searchDelegationGuidelines...),
		tools.NewWebFetchTool(),
		tools.NewWebSearchTool(),
		tools.NewMemoryToolResolving(func() *memory.Store {
			if runnerRef != nil {
				return runnerRef.ActiveMemoryStore()
			}
			return memoryStore
		}, memoryStore),
	}
	if scheduleStore != nil {
		builtinTools = append(builtinTools, tools.NewScheduleTool(scheduleStore))
	}
	// send_file resolves the channel registry lazily: channels are
	// registered after tools are built.
	var channelsRef *channel.Registry
	if imsgCfg != nil || discordCfg != nil || telegramCfg != nil {
		builtinTools = append(builtinTools, tools.NewSendFileTool(func(ctx context.Context, out channel.Outbound) error {
			if channelsRef == nil {
				return fmt.Errorf("channels not ready")
			}
			ch, ok := channelsRef.Lookup(out.Channel)
			if !ok {
				return fmt.Errorf("channel %q not available", out.Channel)
			}
			return ch.Send(ctx, out)
		}))
	}
	if imsgCfg != nil {
		builtinTools = append(builtinTools, tools.NewIMessageSendTool(tools.IMessageSendConfig{
			ServerURL: imsgCfg.ServerURL,
			Password:  imsgCfg.Password,
		}))
	}
	if discordCfg != nil {
		builtinTools = append(builtinTools, tools.NewDiscordSendTool(tools.DiscordSendConfig{
			Token: discordCfg.Token,
		}))
	}
	if telegramCfg != nil {
		builtinTools = append(builtinTools, tools.NewTelegramSendTool(tools.TelegramSendConfig{
			Token: telegramCfg.Token,
		}))
	}
	// Profiles pick from the unfiltered builtins; AGENTS.md's tool
	// allowlist only applies to the main agent.
	profileToolPool := append([]tools.Tool(nil), builtinTools...)
	var allowedTools, allowedPlugins map[string]struct{}
	if agentCfg != nil {
		allowedTools = nameSet(agentCfg.Tools)
		if len(agentCfg.Plugins) > 0 {
			allowedPlugins = nameSet(agentCfg.Plugins)
		}
		builtinTools = filterTools(builtinTools, allowedTools)
	}
	allTools := append([]tools.Tool(nil), builtinTools...)
	var baseHooks []tools.ToolHook
	baseHooks = append(baseHooks, &logHook{writer: os.Stderr})
	var runtimePlugins []subagents.RuntimePlugin

	// Collect after-turn hooks and prompt sections from plugins.
	var afterTurnHooks []plugin.AfterTurnHook
	var agentStartHooks []plugin.AgentStartHook
	var agentEndHooks []plugin.AgentEndHook
	var usageHooks []plugin.UsageHook
	var pluginPrompts []string
	for _, p := range plugins {
		if allowedPlugins != nil {
			if _, ok := allowedPlugins[p.Name()]; !ok {
				continue
			}
		}
		pluginTools := p.Tools()
		if allowedTools != nil {
			pluginTools = filterTools(pluginTools, allowedTools)
		}
		rp := subagents.RuntimePlugin{
			Name:         p.Name(),
			Tools:        pluginTools,
			Hooks:        p.Hooks(),
			SystemPrompt: p.SystemPromptSection(),
		}
		allTools = append(allTools, rp.Tools...)
		runtimePlugins = append(runtimePlugins, rp)
		for _, h := range p.Hooks() {
			baseHooks = append(baseHooks, h)
		}
		if ath, ok := p.(plugin.AfterTurnHook); ok {
			afterTurnHooks = append(afterTurnHooks, ath)
			runtimePlugins[len(runtimePlugins)-1].AfterTurnHook = ath
		}
		if ash, ok := p.(plugin.AgentStartHook); ok {
			agentStartHooks = append(agentStartHooks, ash)
			runtimePlugins[len(runtimePlugins)-1].AgentStartHook = ash
		}
		if aeh, ok := p.(plugin.AgentEndHook); ok {
			agentEndHooks = append(agentEndHooks, aeh)
			runtimePlugins[len(runtimePlugins)-1].AgentEndHook = aeh
		}
		if uh, ok := p.(plugin.UsageHook); ok {
			usageHooks = append(usageHooks, uh)
			runtimePlugins[len(runtimePlugins)-1].UsageHook = uh
		}
		if s := p.SystemPromptSection(); s != "" {
			pluginPrompts = append(pluginPrompts, s)
		}
	}

	skillDirs := []string{
		filepath.Join(home, ".kroos", "skills"),
		filepath.Join(cwd, ".kroos", "skills"),
	}
	loadedSkills, diags, err := skills.LoadSkills(skillDirs...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading skills: %v\n", err)
	}
	for _, d := range diags {
		fmt.Fprintf(os.Stderr, "warning: %s\n", d)
	}
	skillsRegistry := skills.NewRegistry(loadedSkills)
	reloadSkills := func() error {
		reloaded, rdiags, rerr := skills.LoadSkills(skillDirs...)
		if rerr != nil {
			return rerr
		}
		for _, d := range rdiags {
			fmt.Fprintf(os.Stderr, "warning: %s\n", d)
		}
		skillsRegistry.Replace(reloaded)
		return nil
	}
	projectSkillsDir := filepath.Join(cwd, ".kroos", "skills")
	skillWriteLogPath := filepath.Join(cwd, ".kroos", "logs", "skill_writes.log")
	// skill_write is orchestrator-only — appended to allTools, not builtinTools,
	// so subagents don't inherit it.
	allTools = append(allTools, tools.NewSkillWriteTool(projectSkillsDir, skillWriteLogPath, reloadSkills))

	subagentManager, err := subagents.NewManager(
		func(def subagents.Definition) (llm.ChatClient, error) {
			u := def.BaseURL
			if u == "" {
				u = baseURL
			}
			key := apiKey
			if def.APIKeyEnv != "" {
				key = os.Getenv(def.APIKeyEnv)
			}
			return llm.NewClient(u, def.Model, &llm.Options{
				Timeout: subagentLLMTimeout,
				APIKey:  key,
			}), nil
		},
		sessionDir,
		defs,
		builtinTools,
		baseHooks,
		runtimePlugins,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: subagents disabled: %v\n", err)
	} else {
		subagentManager.SetBaseObservers(agentStartHooks, agentEndHooks, usageHooks)
		allTools = append(allTools,
			subagents.NewSpawnTool(subagentManager),
			subagents.NewControlTool(subagentManager),
		)
	}

	registry := tools.NewRegistry(allTools...)
	for _, hook := range baseHooks {
		registry.AddHook(hook)
	}

	if section := subagents.BuildPromptSection(defs); section != "" {
		pluginPrompts = append(pluginPrompts, section)
	}

	var agentPrompt string
	var contextSize int
	if agentCfg != nil {
		agentPrompt = agentCfg.Prompt
		contextSize = agentCfg.ContextSize
	}

	// Load IDENTITY.md and USER.md from agentDirs; last found wins (project overrides home).
	contextFiles := loadContextFiles(agentDirs, "IDENTITY.md", "USER.md")

	var userActivity bool
	runner := cli.Runner{
		ImageInput:       envBool("KROOS_IMAGE_INPUT", true),
		Client:           chatClient,
		Registry:         registry,
		Skills:           skillsRegistry,
		AfterTurnHooks:   afterTurnHooks,
		AgentStartHooks:  agentStartHooks,
		AgentEndHooks:    agentEndHooks,
		UsageHooks:       usageHooks,
		PluginPrompts:    pluginPrompts,
		AgentID:          "main",
		AgentName:        "orchestrator",
		Prompt:           agentPrompt,
		ContextSize:      contextSize,
		ContextFiles:     contextFiles,
		MemoryStore:      memoryStore,
		Scheduler:        scheduler,
		Compaction:       compaction.DefaultSettings,
		HeartbeatEnabled: hbCfg.Interval > 0,
		ClientForModel: func(m string) llm.ChatClient {
			return llm.NewClient(baseURL, m, &llm.Options{
				Timeout: llmTimeout,
				APIKey:  apiKey,
			})
		},
	}

	runnerRef = &runner

	routes := make(map[channel.Key]channel.TurnHandler)
	for _, p := range profiles {
		configured := map[string]bool{"discord": discordCfg != nil, "imessage": imsgCfg != nil, "telegram": telegramCfg != nil}
		if !configured[p.Channel] {
			fmt.Fprintf(os.Stderr, "warning: profile %q bound to %s, which is not configured; skipping\n", p.Name, p.Channel)
			continue
		}
		pr := buildProfileRunner(p, profileToolPool, baseHooks, memoryStore, func(def subagents.Definition) llm.ChatClient {
			if def.Model == "" {
				return chatClient
			}
			u := def.BaseURL
			if u == "" {
				u = baseURL
			}
			key := apiKey
			if def.APIKeyEnv != "" {
				key = os.Getenv(def.APIKeyEnv)
			}
			return llm.NewClient(u, def.Model, &llm.Options{Timeout: llmTimeout, APIKey: key})
		})
		for _, t := range p.Threads {
			routes[channel.Key{Channel: p.Channel, Thread: t}] = pr
		}
		fmt.Fprintf(os.Stderr, "profile %q bound to %s threads %v (senders=%s)\n", p.Name, p.Channel, p.Threads, p.Senders)
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	if scheduler != nil {
		scheduler.Start(runCtx)
		defer scheduler.Stop()
	}

	// Create messaging channel instances first so heartbeat delivery can
	// reference them when wiring up the sched channel.
	var imsgChannel *imessagechannel.Channel
	if imsgCfg != nil {
		imsgChannel = imessagechannel.New(*imsgCfg)
	}
	var discordChannel *discordchannel.Channel
	if discordCfg != nil {
		discordChannel = discordchannel.New(*discordCfg)
	}
	var telegramChannel *telegramchannel.Channel
	if telegramCfg != nil {
		telegramChannel = telegramchannel.New(*telegramCfg)
	}

	channels := channel.NewRegistry()
	channelsRef = channels
	cliCh := clichannel.NewStdio(os.Stdin, os.Stdout)
	channels.Register(cliCh)
	if scheduler != nil {
		schedCh := &sched.Channel{
			Scheduler: scheduler,
			Target:    channel.Key{Channel: clichannel.Name, Thread: clichannel.DefaultThread},
		}
		if hbCfg.Interval > 0 {
			delivery := &sched.HeartbeatDelivery{
				Target:      hbCfg.Target,
				Model:       hbCfg.Model,
				ActiveStart: hbCfg.ActiveStart,
				ActiveEnd:   hbCfg.ActiveEnd,
				Timezone:    hbCfg.Timezone,
			}
			// CLISend wraps the CLI channel for heartbeat replies routed to terminal.
			delivery.CLISend = func(ctx context.Context, content string) error {
				return cliCh.Send(ctx, channel.Outbound{
					Channel: clichannel.Name,
					Thread:  clichannel.DefaultThread,
					Content: content,
				})
			}
			// OutboundSend delivers to the configured messaging channel.
			switch hbCfg.Target {
			case "imessage":
				if imsgChannel != nil && hbCfg.To != "" {
					ch, to := imsgChannel, hbCfg.To
					delivery.OutboundSend = func(ctx context.Context, content string) error {
						return ch.Send(ctx, channel.Outbound{Channel: "imessage", Thread: to, Content: content})
					}
				} else {
					fmt.Fprintf(os.Stderr, "warning: heartbeat target=imessage but iMessage not configured or KROOS_HEARTBEAT_TO not set\n")
				}
			case "discord":
				if discordChannel != nil && hbCfg.To != "" {
					ch, to := discordChannel, hbCfg.To
					delivery.OutboundSend = func(ctx context.Context, content string) error {
						return ch.Send(ctx, channel.Outbound{Channel: "discord", Thread: to, Content: content})
					}
				} else {
					fmt.Fprintf(os.Stderr, "warning: heartbeat target=discord but Discord not configured or KROOS_HEARTBEAT_TO not set\n")
				}
			case "telegram":
				if telegramChannel != nil && hbCfg.To != "" {
					ch, to := telegramChannel, hbCfg.To
					delivery.OutboundSend = func(ctx context.Context, content string) error {
						return ch.Send(ctx, channel.Outbound{Channel: "telegram", Thread: to, Content: content})
					}
				} else {
					fmt.Fprintf(os.Stderr, "warning: heartbeat target=telegram but Telegram not configured or KROOS_HEARTBEAT_TO not set\n")
				}
			}
			schedCh.Heartbeat = delivery
		}
		channels.Register(schedCh)
	}

	// Register messaging channels (instances were created above).
	if imsgChannel != nil {
		channels.Register(imsgChannel)
		fmt.Fprintf(os.Stderr, "iMessage channel enabled (webhook %s)\n", imsgChannel.ListenAddr())
	}
	if discordChannel != nil {
		channels.Register(discordChannel)
		fmt.Fprintf(os.Stderr, "Discord channel enabled (owners=%d, tag=%q)\n",
			len(discordCfg.OwnerIDs), discordCfg.GuildTag)
	}
	if telegramChannel != nil {
		channels.Register(telegramChannel)
		fmt.Fprintf(os.Stderr, "Telegram channel enabled (owners=%d, tag=%q)\n",
			len(telegramCfg.OwnerIDs), telegramCfg.GroupTag)
	}

	dispatcher := &channel.Dispatcher{
		Channels:    channels,
		Store:       convStore,
		Commands:    &channel.CommandExpander{Skills: skillsRegistry},
		Handler:     &channel.Router{Default: &runner, Routes: routes},
		OnUserInput: func() { userActivity = true },
		Logger: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "[dispatcher] "+format+"\n", args...)
		},
	}

	runErr := dispatcher.Run(runCtx)
	if userActivity {
		if err := runner.DistillMemory(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "warning: memory distill failed: %v\n", err)
		}
	}
	if runErr != nil && !errors.Is(runErr, io.EOF) && !errors.Is(runErr, context.Canceled) {
		_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
		os.Exit(1)
	}
}

// buildProfileRunner assembles an isolated Runner for a profile: its own
// prompt, model, and a tool registry restricted to the profile's
// allowlist. The memory tool is rebuilt so it resolves against this
// runner's per-turn store instead of the main runner's.
func buildProfileRunner(p subagents.Profile, pool []tools.Tool, hooks []tools.ToolHook, defaultMemory *memory.Store, clientFor func(subagents.Definition) llm.ChatClient) *cli.Runner {
	pr := &cli.Runner{
		Client:      clientFor(p.Definition),
		Skills:      skills.NewRegistry(nil),
		AgentID:     "profile-" + p.Name,
		AgentName:   p.Name,
		Prompt:      p.Prompt,
		ContextSize: p.ContextSize,
		MemoryStore: defaultMemory,
		Compaction:  compaction.DefaultSettings,
		ImageInput:  envBool("KROOS_IMAGE_INPUT", true),
	}
	selected := filterTools(pool, nameSet(p.Tools))
	for i, t := range selected {
		if t.Definition().Name == "memory" {
			selected[i] = tools.NewMemoryToolResolving(pr.ActiveMemoryStore, defaultMemory)
		}
	}
	pr.Registry = tools.NewRegistry(selected...)
	for _, h := range hooks {
		pr.Registry.AddHook(h)
	}
	return pr
}

type logHook struct {
	writer io.Writer
}

func (h *logHook) BeforeToolCall(_ tools.HookContext, name string, params json.RawMessage) string {
	fmt.Fprintf(h.writer, "%s [STARTED]\n", formatToolCall(name, params))
	return ""
}

func (h *logHook) AfterToolCall(_ tools.HookContext, name string, params json.RawMessage, result tools.ToolResult, err error) {
	status := "DONE"
	if err != nil || result.IsError {
		status = "ERROR"
	}
	fmt.Fprintf(h.writer, "%s [%s]\n", formatToolCall(name, params), status)
}

func formatToolCall(name string, params json.RawMessage) string {
	return fmt.Sprintf("%s(%s)", name, formatParams(params))
}

// envDuration returns the duration value of the named env var, treating empty
// or unset as def. Malformed values log a warning and fall back to def.
func envDuration(name string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		fmt.Fprintf(os.Stderr, "warning: %s=%q is not a valid duration; using default %s\n", name, v, def)
		return def
	}
	return d
}

// envBool returns the boolean value of the named env var, treating empty
// or unset as def. Malformed values log a warning and fall back to def so
// typos don't silently flip behavior.
func envBool(name string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s=%q is not a valid boolean; using default %v\n", name, v, def)
		return def
	}
	return b
}

func nameSet(names []string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

func filterTools(in []tools.Tool, allowed map[string]struct{}) []tools.Tool {
	if _, ok := allowed["*"]; ok {
		out := make([]tools.Tool, len(in))
		copy(out, in)
		return out
	}
	filtered := make([]tools.Tool, 0, len(in))
	for _, t := range in {
		if _, ok := allowed[t.Definition().Name]; ok {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// loadEnvAndConfig fills unset env vars from .env, then project config, then
// global config.
func loadEnvAndConfig() {
	if err := loadDotenv(".env"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading .env: %v\n", err)
	}
	if err := config.Apply(config.ProjectPath(), config.GlobalPath()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading config: %v\n", err)
	}
}

func loadDotenv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(line[len("export "):])
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if n := len(val); n >= 2 {
			if (val[0] == '"' && val[n-1] == '"') || (val[0] == '\'' && val[n-1] == '\'') {
				val = val[1 : n-1]
			}
		}
		if _, ok := os.LookupEnv(key); ok {
			continue
		}
		os.Setenv(key, val)
	}
	return scanner.Err()
}

// loadContextFiles reads the named files from each dir in order; last found
// wins (project-level overrides home-level). Files that don't exist are skipped.
func loadContextFiles(dirs []string, names ...string) []prompt.ContextFile {
	latest := make(map[string]prompt.ContextFile, len(names))
	for _, dir := range dirs {
		for _, name := range names {
			path := filepath.Join(dir, name)
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			latest[name] = prompt.ContextFile{Path: path, Content: string(data)}
		}
	}
	result := make([]prompt.ContextFile, 0, len(names))
	for _, name := range names {
		if cf, ok := latest[name]; ok {
			result = append(result, cf)
		}
	}
	return result
}

// threadProvisioner allocates per-(channel, thread) workspaces and
// memory stores on first sighting. Channels listed in exempt are
// left untouched: their conversations share the process cwd and the
// project-level memory store.
type threadProvisioner struct {
	exempt         map[string]bool
	workspacesRoot string
	memoryRoot     string
}

func (p *threadProvisioner) Provision(k channel.Key) (string, *memory.Store, error) {
	if p.exempt[k.Channel] {
		return "", nil, nil
	}
	thread := sanitizePathSegment(k.Thread)
	ch := sanitizePathSegment(k.Channel)
	if thread == "" || ch == "" {
		return "", nil, nil
	}
	ws := filepath.Join(p.workspacesRoot, ch, thread)
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return "", nil, err
	}
	memDir := filepath.Join(p.memoryRoot, ch, thread)
	return ws, memory.NewStore(memDir), nil
}

func sanitizePathSegment(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func formatParams(params json.RawMessage) string {
	trimmed := strings.TrimSpace(string(params))
	if trimmed == "" || trimmed == "null" {
		return ""
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, params); err != nil {
		if len(trimmed) > 160 {
			return trimmed[:160] + "..."
		}
		return trimmed
	}

	formatted := compact.String()
	if len(formatted) > 160 {
		return formatted[:160] + "..."
	}
	return formatted
}
