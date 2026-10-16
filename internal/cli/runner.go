package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/s-ramirez/kroos-core/internal/channel"
	"github.com/s-ramirez/kroos-core/internal/compaction"
	"github.com/s-ramirez/kroos-core/internal/llm"
	"github.com/s-ramirez/kroos-core/internal/memory"
	"github.com/s-ramirez/kroos-core/internal/plugin"
	"github.com/s-ramirez/kroos-core/internal/prompt"
	"github.com/s-ramirez/kroos-core/internal/schedule"
	"github.com/s-ramirez/kroos-core/internal/skills"
	"github.com/s-ramirez/kroos-core/internal/tasks"
	"github.com/s-ramirez/kroos-core/internal/tools"
	"github.com/s-ramirez/kroos-core/internal/types"
)

// Runner holds the long-lived agent configuration. It exposes three
// entry points:
//
//   - HandleTurn: processes one dispatcher turn against a channel
//     Conversation. Used by the channel dispatcher as the TurnHandler.
//   - RunConversation: runs a fresh conversation to completion for
//     callers that own the full message slice (subagents).
//   - DistillMemory: a silent single-turn run that asks the model to
//     refresh MEMORY.md at session exit.
type Runner struct {
	Client llm.ChatClient
	// ClientFor, if set, builds the client for a conversation whose /model
	// override is set (Conversation.Provider/Model).
	ClientFor func(provider, model string) (llm.ChatClient, error)
	Registry  *tools.Registry
	Skills    *skills.Registry
	// AfterTurnHooks run after each assistant text response. The first hook
	// that returns a non-empty string wins: its message is appended as a user
	// turn, the agent loop continues, and remaining hooks are skipped for
	// that turn. Register higher-priority hooks earlier in the slice.
	AfterTurnHooks  []plugin.AfterTurnHook
	AgentStartHooks []plugin.AgentStartHook
	AgentEndHooks   []plugin.AgentEndHook
	PluginPrompts   []string
	AgentID         string // "main" or "subagent-N"
	AgentName       string // human-readable name
	Prompt          string // opening of the system prompt (from AGENTS.md body)
	// ContextFiles are embedded verbatim at the top of the system prompt.
	// Typically IDENTITY.md and USER.md from the .kroos/ workspace.
	ContextFiles []prompt.ContextFile
	ContextSize  int // model context window in tokens; 0 = unknown
	OnUsage      func(usage *types.Usage)
	UsageHooks   []plugin.UsageHook

	// MemoryStore, if set, provides persistent memory for the agent.
	// When non-nil, MEMORY.md is loaded and injected into the system prompt
	// and DistillMemory can be invoked to refresh MEMORY.md.
	MemoryStore *memory.Store

	// Compaction, if enabled, automatically trims old messages once the
	// estimated context usage crosses the threshold. Only fires when
	// ContextSize > 0.
	Compaction compaction.Settings

	// Scheduler, if set, is consulted when building the system prompt so
	// the model sees currently-enabled user-managed jobs at startup. The
	// scheduler's event stream is handled by the sched channel adapter,
	// not by the Runner directly.
	Scheduler *schedule.Scheduler

	// ImageInput, if true, inlines image attachments as image_url content
	// parts for vision-capable models. Other attachments (and all of them
	// when false) are referenced by local path only.
	ImageInput bool

	// activeMemoryStore is the per-turn memory store override. Set by
	// HandleTurn to conv.MemoryStore (or r.MemoryStore) and cleared on
	// return. Read via ActiveMemoryStore so tools can route to the
	// conversation-scoped store.
	activeMemoryStore *memory.Store

	// Tasks runs background work (skill reviews here; scheduled tasks
	// via the sched channel and task tool).
	Tasks *tasks.Executor

	// SkillReviewInterval, when > 0, submits a background skill review to
	// Tasks after this many tool calls accumulate (main agent only).
	SkillReviewInterval int
	skillReview         skillReviewState
}

// ActiveMemoryStore returns the memory store for the in-flight turn,
// or r.MemoryStore when no turn is active. The memory tool uses this
// to resolve per-conversation memory.
func (r *Runner) ActiveMemoryStore() *memory.Store {
	if r.activeMemoryStore != nil {
		return r.activeMemoryStore
	}
	return r.MemoryStore
}

const maxEmptyRetries = 2
const initialRateLimitBackoff = 30 * time.Second
const maxRateLimitBackoff = 5 * time.Minute
const emptyResponseRecoveryPrompt = "Your previous reply was empty. You must now produce a direct assistant response using the conversation and any tool results already returned. Do not call any more tools. Do not repeat completed work. If the task is complete, give the final answer now."

// HandleTurn implements channel.TurnHandler. The dispatcher serializes
// turns per conversation, so this method has exclusive access to
// conv.Messages for its duration.
//
// The first turn on a fresh conversation lazily builds and prepends the
// system prompt so every surface (CLI, scheduled event, future webui)
// shares the same initialization path.
func (r *Runner) HandleTurn(ctx context.Context, conv *channel.Conversation, userContent string, reply channel.ReplyFunc) error {
	if r.Client == nil {
		return fmt.Errorf("client is required")
	}

	client := r.Client
	if conv.Model != "" && r.ClientFor != nil {
		c, err := r.ClientFor(conv.Provider, conv.Model)
		if err != nil {
			return fmt.Errorf("model override %s: %w", conv.Model, err)
		}
		client = c
	}

	// Per-conversation workspace: chdir for the duration of the turn.
	// Safe because the dispatcher serializes turns.
	if conv.Workspace != "" {
		if err := os.MkdirAll(conv.Workspace, 0o755); err == nil {
			if orig, err := os.Getwd(); err == nil {
				if err := os.Chdir(conv.Workspace); err == nil {
					defer os.Chdir(orig)
				}
			}
		}
	}

	// Per-conversation memory override (used by buildSystemPrompt and
	// by the memory tool, which reads r.ActiveMemoryStore() per call).
	memStore := r.MemoryStore
	if conv.MemoryStore != nil {
		memStore = conv.MemoryStore
	}
	prevActive := r.activeMemoryStore
	r.activeMemoryStore = memStore
	defer func() { r.activeMemoryStore = prevActive }()

	if len(conv.Messages) == 0 {
		conv.Messages = append(conv.Messages, types.Message{
			Role:    "system",
			Content: r.buildSystemPromptWith(memStore),
		})
		conv.SkillsVersion = r.Skills.Version()
	} else if v := r.Skills.Version(); v != conv.SkillsVersion && conv.Messages[0].Role == "system" {
		// Skills changed (skill_manage or review) — refresh <available_skills>.
		conv.Messages[0].Content = r.buildSystemPromptWith(memStore)
		conv.SkillsVersion = v
	}

	conv.Messages = append(conv.Messages, types.Message{
		Role:    "user",
		Content: buildUserContent(userContent, conv.Attachments, r.ImageInput),
	})
	ctx = channel.WithKey(ctx, conv.Key)

	var toolSchemas []types.ChatRequestTool
	if r.Registry != nil {
		toolSchemas = r.Registry.Schemas()
	}

	state := &compactionState{
		summary:    conv.LastSummary,
		usage:      conv.LastUsage,
		usageIndex: conv.LastUsageIndex,
	}
	if state.usageIndex == 0 && state.usage == nil {
		state.usageIndex = -1
	}
	_, err := r.agentLoop(ctx, &conv.Messages, toolSchemas, reply, client, state)
	conv.LastSummary = state.summary
	conv.LastUsage = state.usage
	conv.LastUsageIndex = state.usageIndex
	if err == nil {
		r.maybeReviewSkills(conv.Messages, toolSchemas, state)
	}
	return err
}

// compactionState is the per-conversation compaction bookkeeping. The
// runner carries a reference for the duration of a single turn and
// writes the updated values back to the Conversation afterwards.
type compactionState struct {
	summary    string
	usage      *types.Usage
	usageIndex int

	// toolCalls counts tool calls this turn; usedSkillManage notes that the
	// agent already curated skills itself. Both feed the skill review.
	toolCalls       int
	usedSkillManage bool
}

// buildSystemPrompt assembles the system prompt from the runner's
// current configuration. It is invoked once per conversation, when the
// conversation is first seen by HandleTurn.
func (r *Runner) buildSystemPrompt() string {
	return r.buildSystemPromptWith(r.MemoryStore)
}

func (r *Runner) buildSystemPromptWith(memStore *memory.Store) string {
	var availableTools []tools.Tool
	if r.Registry != nil {
		availableTools = r.Registry.List()
	}

	now := time.Now()
	tz, _ := now.Zone()
	cwd, _ := os.Getwd()
	env := prompt.Environment{
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Workspace: cwd,
		Timezone:  tz,
		Time:      now.Format(time.RFC3339),
	}

	curated := loadCuratedFrom(memStore)
	pluginPrompts := r.composePluginPrompts()
	return prompt.BuildSystemPrompt(r.Prompt, availableTools, r.Skills.Snapshot(), env, curated, pluginPrompts, r.ContextFiles...)
}

func jobModeOf(j schedule.Job) string {
	if j.Mode == "" {
		return schedule.ModeRemind
	}
	return j.Mode
}

// SystemPrompt returns the runner's system prompt (used by background
// task factories that run isolated conversations).
func (r *Runner) SystemPrompt() string { return r.buildSystemPrompt() }

// composePluginPrompts returns plugin sections plus a synthesized
// <scheduled_tasks> block describing currently-enabled user jobs.
func (r *Runner) composePluginPrompts() []string {
	sections := append([]string(nil), r.PluginPrompts...)
	if r.Scheduler == nil {
		return sections
	}
	if s := buildScheduledTasksSection(r.Scheduler.Store().List()); s != "" {
		sections = append(sections, s)
	}
	return sections
}

// buildScheduledTasksSection renders enabled jobs as an XML block for the
// system prompt. The tool's own guidelines already cover <scheduled_event>
// semantics, so this section is pure state — a snapshot of what is pending
// at session start.
func buildScheduledTasksSection(jobs []schedule.Job) string {
	var enabled []schedule.Job
	for _, j := range jobs {
		if j.Enabled {
			enabled = append(enabled, j)
		}
	}
	if len(enabled) == 0 {
		return ""
	}
	lines := []string{
		"# Scheduled tasks",
		"",
		"These jobs are persisted. Reminders fire here as <scheduled_event> user messages; tasks run in an isolated background agent and deliver their reply.",
		"Use the task tool to inspect, add, remove, run, or review the history of jobs.",
		"",
		"<scheduled_tasks>",
	}
	for _, j := range enabled {
		sched := describeSchedule(j.Schedule)
		next := "(none)"
		if j.NextRunAt != nil {
			next = j.NextRunAt.UTC().Format(time.RFC3339)
		}
		lines = append(lines,
			"  <task>",
			fmt.Sprintf("    <id>%s</id>", j.ID),
			fmt.Sprintf("    <name>%s</name>", j.Name),
			fmt.Sprintf("    <mode>%s</mode>", jobModeOf(j)),
			fmt.Sprintf("    <schedule>%s</schedule>", sched),
			fmt.Sprintf("    <next-run>%s</next-run>", next),
			fmt.Sprintf("    <text>%s</text>", j.Text),
			"  </task>",
		)
	}
	lines = append(lines, "</scheduled_tasks>")
	return strings.Join(lines, "\n")
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

func (r *Runner) RunConversation(
	ctx context.Context,
	messages []types.Message,
	toolSchemas []types.ChatRequestTool,
) (string, []types.Message, error) {
	state := &compactionState{usageIndex: -1}
	finalText, err := r.agentLoop(ctx, &messages, toolSchemas, nil, r.Client, state)
	return finalText, messages, err
}

// agentLoop is the core LLM + tool-dispatch cycle. emit, if non-nil,
// receives user-visible output: the final assistant text, usage
// summaries, and compaction status lines. Pass nil for silent runs
// (subagents, DistillMemory).
func (r *Runner) agentLoop(
	ctx context.Context,
	messages *[]types.Message,
	toolSchemas []types.ChatRequestTool,
	emit channel.ReplyFunc,
	client llm.ChatClient,
	state *compactionState,
) (finalText string, err error) {
	lifecycleCtx := plugin.AgentLifecycleContext{
		AgentID:   r.AgentID,
		AgentName: r.AgentName,
	}
	for _, h := range r.AgentStartHooks {
		h.OnAgentStart(lifecycleCtx)
	}
	defer func() {
		for _, h := range r.AgentEndHooks {
			h.OnAgentEnd(lifecycleCtx, finalText, err)
		}
	}()

	hadToolCalls := false
	emptyRetries := 0
	rateLimitBackoff := initialRateLimitBackoff
	rateLimitStartedAt := time.Time{}

	for {
		if r.maybeCompact(ctx, messages, emit, state) {
			hadToolCalls = false
			emptyRetries = 0
		}

		resp, err := client.Chat(ctx, *messages, toolSchemas)
		if err != nil {
			if errors.Is(err, llm.ErrRateLimited) {
				now := time.Now()
				if rateLimitStartedAt.IsZero() {
					rateLimitStartedAt = now
				}
				if now.Sub(rateLimitStartedAt) >= maxRateLimitBackoff {
					if emit != nil {
						if emitErr := emit("I keep hitting the API rate limit. I retried with exponential backoff for 5 minutes and it still isn't through.\n"); emitErr != nil {
							return "", emitErr
						}
					}
					return "", err
				}
				fmt.Fprintf(os.Stderr, "[runner] API rate limited; backing off for %s\n", rateLimitBackoff)
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(rateLimitBackoff):
					if rateLimitBackoff < maxRateLimitBackoff {
						rateLimitBackoff *= 2
						if rateLimitBackoff > maxRateLimitBackoff {
							rateLimitBackoff = maxRateLimitBackoff
						}
					}
					continue
				}
			}
			return "", err
		}
		rateLimitBackoff = initialRateLimitBackoff
		rateLimitStartedAt = time.Time{}

		r.reportUsage(resp, emit)
		if resp.Usage != nil {
			state.usage = resp.Usage
			state.usageIndex = len(*messages) - 1
		}

		toolCalls := types.GetToolCalls(resp)
		if len(toolCalls) == 0 {
			text := types.GetResponseText(resp)

			if text == "" && hadToolCalls && emptyRetries < maxEmptyRetries {
				emptyRetries++
				*messages = append(*messages, types.Message{
					Role:    "assistant",
					Content: "",
				})
				*messages = append(*messages, types.Message{
					Role:    "user",
					Content: emptyResponseRecoveryPrompt,
				})
				continue
			}

			if text == "" {
				text = fallbackNoResponseMessage(*messages, hadToolCalls)
			}

			if emit != nil {
				if err := emit(text + "\n"); err != nil {
					return "", err
				}
			}

			*messages = append(*messages, types.Message{
				Role:    "assistant",
				Content: text,
			})

			// Check after-turn hooks — plugins can inject a follow-up message.
			injected := false
			for _, h := range r.AfterTurnHooks {
				if msg := h.AfterTurn(text); msg != "" {
					*messages = append(*messages, types.Message{
						Role:    "user",
						Content: msg,
					})
					hadToolCalls = false
					emptyRetries = 0
					injected = true
					break
				}
			}
			if injected {
				continue
			}
			return text, nil
		}

		hadToolCalls = true
		emptyRetries = 0

		assistantMsg := resp.Choices[0].Message
		var content any
		if assistantMsg.Content != nil {
			content = *assistantMsg.Content
		}
		*messages = append(*messages, types.Message{
			Role:      "assistant",
			Content:   content,
			ToolCalls: assistantMsg.ToolCalls,
		})

		for _, call := range toolCalls {
			if r.Registry == nil {
				*messages = append(*messages, types.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    fmt.Sprintf("error: unknown tool %q", call.Function.Name),
				})
				continue
			}

			tool, ok := r.Registry.Get(call.Function.Name)
			if !ok {
				*messages = append(*messages, types.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    fmt.Sprintf("error: unknown tool %q", call.Function.Name),
				})
				continue
			}

			state.toolCalls++
			if call.Function.Name == "skill_manage" {
				state.usedSkillManage = true
			}

			rawArgs := json.RawMessage(call.Function.Arguments)
			if !json.Valid(rawArgs) {
				*messages = append(*messages, types.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    "error: invalid JSON arguments",
				})
				continue
			}

			hctx := tools.HookContext{AgentID: r.AgentID, AgentName: r.AgentName}
			if reason := r.Registry.RunBeforeHooks(hctx, call.Function.Name, rawArgs); reason != "" {
				*messages = append(*messages, types.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    fmt.Sprintf("error: blocked by hook: %s", reason),
				})
				continue
			}

			result, err := tool.Execute(ctx, rawArgs)
			r.Registry.RunAfterHooks(hctx, call.Function.Name, rawArgs, result, err)

			if err != nil {
				*messages = append(*messages, types.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    fmt.Sprintf("error: %v", err),
				})
				continue
			}

			*messages = append(*messages, types.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    result.Output,
			})
		}
	}
}

// maybeCompact checks the budget and, if triggered, replaces *messages
// with a compacted version in place. Returns true when compaction ran
// successfully so callers can reset per-turn state.
func (r *Runner) maybeCompact(ctx context.Context, messages *[]types.Message, emit channel.ReplyFunc, state *compactionState) bool {
	if !r.Compaction.Enabled || r.ContextSize <= 0 {
		return false
	}
	tokens := compaction.EstimateContextTokens(*messages, state.usage, state.usageIndex)
	if !compaction.ShouldCompact(tokens, r.ContextSize, r.Compaction) {
		return false
	}

	if emit != nil {
		_ = emit(compaction.FormatTriggerLine(tokens, r.ContextSize, r.Compaction) + "\n")
	}

	res, err := compaction.Compact(ctx, r.Client, *messages, state.summary, r.Compaction, tokens)
	if err != nil {
		if emit != nil {
			_ = emit(fmt.Sprintf("[compaction failed: %v]\n", err))
		}
		return false
	}
	if res.Summary == "" {
		return false
	}

	*messages = res.Messages
	state.summary = res.Summary
	state.usage = nil
	state.usageIndex = -1

	if emit != nil {
		after := compaction.EstimateContextTokens(*messages, nil, -1)
		_ = emit(fmt.Sprintf("[compacted: %d → %d tokens]\n", tokens, after))
	}
	return true
}

func fallbackNoResponseMessage(messages []types.Message, hadToolCalls bool) string {
	if !hadToolCalls {
		return "[no response]"
	}

	var recent []string
	for i := len(messages) - 1; i >= 0 && len(recent) < 3; i-- {
		msg := messages[i]
		if msg.Role != "tool" {
			continue
		}
		content, ok := msg.Content.(string)
		if !ok {
			continue
		}
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		recent = append([]string{content}, recent...)
	}

	if len(recent) == 0 {
		return "[no response after tool calls]"
	}

	return "[no response after tool calls]\nRecent tool results:\n- " + strings.Join(recent, "\n- ")
}

func (r *Runner) reportUsage(resp *types.ChatResponse, emit channel.ReplyFunc) {
	if resp == nil || resp.Usage == nil {
		return
	}
	for _, h := range r.UsageHooks {
		if h == nil {
			continue
		}
		h.OnUsage(plugin.AgentLifecycleContext{
			AgentID:   r.AgentID,
			AgentName: r.AgentName,
		}, resp.Usage, r.ContextSize)
	}
	if r.OnUsage != nil {
		r.OnUsage(resp.Usage)
		return
	}
	if emit == nil {
		return
	}
	u := resp.Usage
	var line string
	if r.ContextSize > 0 {
		remaining := r.ContextSize - u.TotalTokens
		pct := float64(u.TotalTokens) / float64(r.ContextSize) * 100
		line = fmt.Sprintf("[tokens: prompt=%d completion=%d total=%d / ctx=%d (%.1f%%) left=%d]\n",
			u.PromptTokens, u.CompletionTokens, u.TotalTokens, r.ContextSize, pct, remaining)
	} else {
		line = fmt.Sprintf("[tokens: prompt=%d completion=%d total=%d]\n",
			u.PromptTokens, u.CompletionTokens, u.TotalTokens)
	}
	_ = emit(line)
}

// loadCuratedFrom reads MEMORY.md from the given store. Returns "" on
// any error or missing file — memory is best-effort, never fatal.
func loadCuratedFrom(store *memory.Store) string {
	if store == nil {
		return ""
	}
	curated, err := store.LoadCurated()
	if err != nil {
		return ""
	}
	return curated
}

// DistillMemory runs a single non-interactive agent turn that asks the
// model to refresh MEMORY.md based on this session's work. Only the
// top-level runner (AgentID == "main") executes; subagents are no-ops.
// Intended to be called from the main entry point after the channel
// dispatcher exits.
func (r *Runner) DistillMemory(ctx context.Context) error {
	if r.MemoryStore == nil || r.AgentID != "main" {
		return nil
	}
	if r.Client == nil {
		return fmt.Errorf("client is required")
	}

	var toolSchemas []types.ChatRequestTool
	if r.Registry != nil {
		toolSchemas = r.Registry.Schemas()
	}

	messages := []types.Message{
		{Role: "system", Content: r.buildSystemPrompt()},
		{Role: "user", Content: channel.DistillInstruction},
	}

	state := &compactionState{usageIndex: -1}
	_, err := r.agentLoop(ctx, &messages, toolSchemas, nil, r.Client, state)
	return err
}
