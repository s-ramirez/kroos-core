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
	"github.com/s-ramirez/kroos-core/internal/tasks"
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

	if len(os.Args) > 1 && (os.Args[1] == "login" || os.Args[1] == "logout") {
		if err := runLogin(os.Args[1], os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", os.Args[1], err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "models" {
		loadEnvAndConfig()
		if err := runModels(os.Args[2:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "models: %v\n", err)
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

	providers := newProvidersFromEnv()
	provider := providers.Default

	model := os.Getenv("KROOS_MODEL") // "" = provider default

	// All instance state lives in KROOS_HOME; cwd is only the tools' working dir.
	kroosHome := config.Home()
	_ = os.Setenv("KROOS_HOME", kroosHome) // visible to bash and the agent
	agentDirs := []string{kroosHome}
	agentCfg, err := subagents.LoadAgentConfig(agentDirs...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: loading agent config: %v\n", err)
		os.Exit(1)
	}
	if agentCfg != nil {
		if agentCfg.Provider != "" {
			provider = agentCfg.Provider
		}
		model = agentCfg.Model
		// CLI flags beat the agent config file.
		if explicit["provider"] {
			provider = os.Getenv("KROOS_PROVIDER")
		}
		if explicit["model"] {
			model = os.Getenv("KROOS_MODEL")
		}
	}

	llmTimeout := envDuration("KROOS_LLM_TIMEOUT", 300*time.Second)
	subagentLLMTimeout := envDuration("KROOS_SUBAGENT_LLM_TIMEOUT", llmTimeout)

	// An unusable provider (no login, no URL) isn't fatal: start on the
	// first ready one. Its model belonged to the old provider, so drop it.
	if err := providers.Ready(provider); err != nil {
		if alt := providers.FirstReady(provider); alt != provider {
			fmt.Fprintf(os.Stderr, "warning: %s unavailable (%v); using %s\n", provider, err, alt)
			provider, model = alt, ""
		} else {
			fmt.Fprintf(os.Stderr, "warning: no LLM provider ready (%v); run `kroos login` or set omlx.url\n", err)
		}
	}
	// Subagents/profiles/tasks without a provider follow the main agent's.
	providers.Default = provider
	client := providers.Client(provider, model, llmTimeout)

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

	subagentDirs := []string{filepath.Join(kroosHome, "subagents")}
	defs, subagentDiags, err := subagents.LoadDefinitions(subagentDirs...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading subagents: %v\n", err)
	}
	for _, d := range subagentDiags {
		fmt.Fprintf(os.Stderr, "warning: %s\n", d)
	}
	searchDelegationGuidelines := subagents.SearchDelegationGuidelines(defs)

	profiles, profileDiags, err := subagents.LoadProfiles(
		filepath.Join(kroosHome, "profiles"),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading profiles: %v\n", err)
	}
	for _, d := range profileDiags {
		fmt.Fprintf(os.Stderr, "warning: %s\n", d)
	}

	memoryStore := memory.NewStore(filepath.Join(kroosHome, "memory"))

	// Per-(channel, thread) isolation for non-CLI channels. CLI and sched
	// keep the shared project workspace + memory; discord/imessage/etc
	// get their own workspace dir and memory dir provisioned on first
	// inbound message.
	kroosStateDir := filepath.Join(kroosHome, "state")
	kroosWorkspacesDir := filepath.Join(kroosHome, "workspaces")
	kroosMemoryDir := filepath.Join(kroosHome, "memory")
	provisioner := &threadProvisioner{
		exempt:         map[string]bool{clichannel.Name: true, sched.Name: true},
		workspacesRoot: kroosWorkspacesDir,
		memoryRoot:     kroosMemoryDir,
	}
	convStore := channel.NewPersistentStore(kroosStateDir, provisioner)

	// runnerRef is populated after the Runner is constructed; the memory
	// tool closes over it to route each call to the turn's active store.
	var runnerRef *cli.Runner

	scheduleStore, err := schedule.NewStore(filepath.Join(kroosHome, "schedule"))
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
	if discordCfg != nil && discordCfg.AllowAll {
		fmt.Fprintln(os.Stderr, "warning: DISCORD_ALLOW_ALL_USERS=true — anyone can use the agent and its tools")
	}
	if telegramCfg != nil && telegramCfg.AllowAll {
		fmt.Fprintln(os.Stderr, "warning: TELEGRAM_ALLOW_ALL_USERS=true — anyone can use the agent and its tools")
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
		tools.NewSecretTool(config.EnvPath()),
		tools.NewMemoryToolResolving(func() *memory.Store {
			if runnerRef != nil {
				return runnerRef.ActiveMemoryStore()
			}
			return memoryStore
		}, memoryStore),
	}
	// send_file and task delivery resolve the channel registry lazily:
	// channels are registered after tools are built.
	var channelsRef *channel.Registry

	// Background tasks: the factory is assigned once tools/skills exist.
	taskCtx, cancelTasks := context.WithCancel(context.Background())
	defer cancelTasks()
	var taskFactory tasks.Factory
	taskExecutor := tasks.NewExecutor(taskCtx, filepath.Join(kroosHome, "tasks"),
		func(t tasks.Task) (tasks.AgentSpec, error) {
			if taskFactory == nil {
				return tasks.AgentSpec{}, fmt.Errorf("task runtime not ready")
			}
			return taskFactory(t)
		},
		func(ctx context.Context, d schedule.Delivery, text string) error {
			if channelsRef == nil {
				return fmt.Errorf("channels not ready")
			}
			ch, ok := channelsRef.Lookup(d.Target)
			if !ok {
				return fmt.Errorf("delivery target %q not available", d.Target)
			}
			to := d.To
			if d.Target == clichannel.Name && to == "" {
				to = clichannel.DefaultThread
			}
			if h, ok := ch.(channel.Homer); ok && to == "" {
				to = h.Home()
			}
			if to == "" {
				return fmt.Errorf("delivery target %q needs deliver_to", d.Target)
			}
			return ch.Send(ctx, channel.Outbound{Channel: d.Target, Thread: to, Content: text})
		})
	taskExecutor.Log = func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "[tasks] "+format+"\n", args...)
	}
	if scheduleStore != nil {
		builtinTools = append(builtinTools, tools.NewTaskTool(scheduleStore, taskExecutor, scheduler))
	}
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

	skillDirs := []string{filepath.Join(kroosHome, "skills")}
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
	projectSkillsDir := filepath.Join(kroosHome, "skills")
	skillLogPath := filepath.Join(kroosHome, "logs", "skill_writes.log")
	// Skill tools are orchestrator-only — appended to allTools, not
	// builtinTools, so subagents don't inherit them.
	skillUsage := skills.NewUsage(filepath.Join(projectSkillsDir, ".usage.json"))
	skillManage := tools.NewSkillManageTool(projectSkillsDir, skillDirs, skillsRegistry, skillUsage, skillLogPath, reloadSkills)
	skillTools := []tools.Tool{tools.NewSkillViewTool(skillsRegistry, skillUsage), skillManage}
	curatorCfg := cli.CuratorConfig{
		Usage:        skillUsage,
		Registry:     skillsRegistry,
		ArchiveDir:   skillManage.ArchiveDir(),
		StaleAfter:   envDuration("KROOS_SKILL_STALE_AFTER", 30*24*time.Hour),
		ArchiveAfter: envDuration("KROOS_SKILL_ARCHIVE_AFTER", 90*24*time.Hour),
		Reload:       reloadSkills,
	}
	registerCurator(scheduler, taskExecutor, envDuration("KROOS_SKILL_CURATOR_INTERVAL", 7*24*time.Hour))
	allTools = append(allTools, skillTools...)

	subagentManager, err := subagents.NewManager(
		func(def subagents.Definition) (llm.ChatClient, error) {
			return providers.Client(def.Provider, def.Model, subagentLLMTimeout), nil
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

	// Load IDENTITY.md and USER.md from agentDirs;
	contextFiles := loadContextFiles(agentDirs, "IDENTITY.md", "USER.md")

	// Background agents get the builtins minus the task tool (no
	// self-scheduling recursion), optionally narrowed per task.
	var taskToolPool []tools.Tool
	for _, t := range builtinTools {
		if t.Definition().Name != "task" {
			taskToolPool = append(taskToolPool, t)
		}
	}
	taskFactory = func(t tasks.Task) (tasks.AgentSpec, error) {
		client := chatClient
		if t.Model != "" {
			client = providers.Client("", t.Model, llmTimeout)
		}
		switch t.Agent {
		case cli.SkillReviewAgent:
			return tasks.AgentSpec{Agent: cli.NewSkillReviewRunner(client, skillTools)}, nil
		case cli.CuratorAgent:
			return cli.BuildCurator(client, skillTools, curatorCfg)
		}
		pool := taskToolPool
		if len(t.Tools) > 0 {
			pool = filterTools(pool, nameSet(t.Tools))
		}
		reg := tools.NewRegistry(pool...)
		for _, h := range baseHooks {
			reg.AddHook(h)
		}
		bg := &cli.Runner{
			Client:       client,
			Registry:     reg,
			Skills:       skillsRegistry,
			AgentID:      "task-" + t.ID,
			AgentName:    t.Name,
			Prompt:       agentPrompt,
			ContextFiles: contextFiles,
			MemoryStore:  memoryStore,
		}
		return tasks.AgentSpec{Agent: bg, System: bg.SystemPrompt(), Schemas: reg.Schemas()}, nil
	}

	var userActivity bool
	runner := cli.Runner{
		ImageInput:          envBool("KROOS_IMAGE_INPUT", true),
		Client:              chatClient,
		Registry:            registry,
		Skills:              skillsRegistry,
		AfterTurnHooks:      afterTurnHooks,
		AgentStartHooks:     agentStartHooks,
		AgentEndHooks:       agentEndHooks,
		UsageHooks:          usageHooks,
		PluginPrompts:       pluginPrompts,
		AgentID:             "main",
		AgentName:           "orchestrator",
		Prompt:              agentPrompt,
		ContextSize:         contextSize,
		ContextFiles:        contextFiles,
		MemoryStore:         memoryStore,
		Scheduler:           scheduler,
		Compaction:          compaction.DefaultSettings,
		SkillReviewInterval: envInt("KROOS_SKILL_REVIEW_INTERVAL", 10),
		Tasks:               taskExecutor,
	}

	clientFor := func(prov, m string) (llm.ChatClient, error) { return providers.Client(prov, m, llmTimeout), nil }
	runner.ClientFor = clientFor
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
			return providers.Client(def.Provider, def.Model, llmTimeout)
		})
		pr.ClientFor = clientFor
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
		hasHome := func(target string) bool {
			switch target {
			case "imessage":
				return imsgCfg != nil && imsgCfg.HomeChannel != ""
			case "discord":
				return discordCfg != nil && discordCfg.HomeChannel != ""
			case "telegram":
				return telegramCfg != nil && telegramCfg.HomeChannel != ""
			}
			return false
		}
		if hbCfg.Interval > 0 && hbCfg.Target != clichannel.Name && hbCfg.Target != "none" && hbCfg.To == "" && !hasHome(hbCfg.Target) {
			fmt.Fprintf(os.Stderr, "warning: heartbeat target=%s needs KROOS_HEARTBEAT_TO or a home channel; replies will fail to deliver\n", hbCfg.Target)
		}
		schedCh.RunTask = func(ev schedule.Event) {
			if _, ok := taskExecutor.Submit(tasks.FromEvent(ev)); !ok {
				fmt.Fprintf(os.Stderr, "[tasks] %q still running; skipped this fire\n", ev.Name)
			}
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
		Channels:     channels,
		Store:        convStore,
		Commands:     &channel.CommandExpander{Skills: skillsRegistry, Usage: skillUsage},
		ModelCommand: sessionModelCommand(providers, modelChoice{provider, displayModel(providers, model)}),
		Handler:      &channel.Router{Default: &runner, Routes: routes},
		OnUserInput:  func() { userActivity = true },
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
// registerCurator injects the weekly skill-curator task. Its first fire
// is anchored to the last recorded run so frequent restarts (auto-deploy)
// don't keep pushing it back.
func registerCurator(scheduler *schedule.Scheduler, exec *tasks.Executor, interval time.Duration) {
	if scheduler == nil || interval <= 0 {
		return
	}
	now := time.Now()
	anchor := now.Add(interval)
	if runs := exec.History(cli.CuratorAgent, 1); len(runs) > 0 {
		anchor = runs[0].FinishedAt.Add(interval)
	}
	if anchor.Before(now) {
		anchor = now.Add(5 * time.Minute)
	}
	scheduler.Inject(schedule.Job{
		ID:       cli.CuratorAgent,
		Name:     "skill-curator",
		Enabled:  true,
		Mode:     schedule.ModeTask,
		Agent:    cli.CuratorAgent,
		Deliver:  schedule.Delivery{Target: "none"},
		Text:     "Curate the skill library.",
		Schedule: schedule.Schedule{Kind: schedule.KindEvery, Every: schedule.Duration(interval), Anchor: &anchor},
	})
}

// envInt returns the integer value of the named env var, or def when
// unset or malformed.
func envInt(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: invalid %s=%q; using %d\n", name, v, def)
		return def
	}
	return n
}

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
	if err := loadDotenv(config.EnvPath()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading .env: %v\n", err)
	}
	if err := config.Apply(config.GlobalPath()); err != nil {
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

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// displayModel names the model a provider will use when none is set explicitly.
func displayModel(p *llm.Providers, model string) string {
	if model != "" {
		return model
	}
	if m := p.Models[p.Default]; m != "" {
		return m
	}
	return "auto"
}
