package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/s-ramirez/kroos-core/internal/channel"
	"github.com/s-ramirez/kroos-core/internal/llm"
	"github.com/s-ramirez/kroos-core/internal/plugin"
	"github.com/s-ramirez/kroos-core/internal/schedule"
	"github.com/s-ramirez/kroos-core/internal/tools"
	"github.com/s-ramirez/kroos-core/internal/types"
)

type stubTool struct {
	name   string
	output string
}

func (s stubTool) Definition() tools.ToolDefinition {
	return tools.ToolDefinition{
		Name:        s.name,
		Description: "stub tool",
		Parameters:  tools.JSONSchema{"type": "object"},
	}
}

func (s stubTool) Execute(ctx context.Context, params json.RawMessage) (tools.ToolResult, error) {
	_ = ctx
	_ = params
	return tools.ToolResult{Output: s.output}, nil
}

type blockingHook struct {
	blockedTool string
	reason      string
	blocked     bool
}

func (h *blockingHook) BeforeToolCall(_ tools.HookContext, name string, _ json.RawMessage) string {
	if name == h.blockedTool && !h.blocked {
		h.blocked = true
		return h.reason
	}
	return ""
}

func (h blockingHook) AfterToolCall(_ tools.HookContext, _ string, _ json.RawMessage, _ tools.ToolResult, _ error) {
}

type stubClient struct {
	responses []*types.ChatResponse
	errors    []error
	calls     int
}

type lifecycleRecorder struct {
	starts []plugin.AgentLifecycleContext
	ends   []struct {
		ctx       plugin.AgentLifecycleContext
		finalText string
		err       error
	}
}

func (r *lifecycleRecorder) OnAgentStart(ctx plugin.AgentLifecycleContext) {
	r.starts = append(r.starts, ctx)
}

func (r *lifecycleRecorder) OnAgentEnd(ctx plugin.AgentLifecycleContext, finalText string, err error) {
	r.ends = append(r.ends, struct {
		ctx       plugin.AgentLifecycleContext
		finalText string
		err       error
	}{
		ctx:       ctx,
		finalText: finalText,
		err:       err,
	})
}

func (s *stubClient) Chat(ctx context.Context, messages []types.Message, tools []types.ChatRequestTool) (*types.ChatResponse, error) {
	_ = ctx
	_ = messages
	_ = tools
	if s.calls < len(s.errors) && s.errors[s.calls] != nil {
		err := s.errors[s.calls]
		s.calls++
		return nil, err
	}
	if s.calls >= len(s.responses) {
		return nil, errors.New("unexpected chat call")
	}
	resp := s.responses[s.calls]
	s.calls++
	return resp, nil
}

func strPtr(value string) *string {
	return &value
}

// captureReply returns a ReplyFunc that appends each reply to *out and
// never errors. Used by tests that care about the exact text sent back
// through the dispatcher reply path.
func captureReply(out *[]string) channel.ReplyFunc {
	return func(text string) error {
		*out = append(*out, text)
		return nil
	}
}

func TestRunnerHandleTurnEmitsFinalText(t *testing.T) {
	client := &stubClient{
		responses: []*types.ChatResponse{
			{
				Choices: []types.Choice{{
					Message: types.ResponseMessage{
						Role:    "assistant",
						Content: strPtr("hi there"),
					},
				}},
			},
		},
	}

	runner := Runner{
		Client:   client,
		Registry: tools.NewRegistry(),
	}

	conv := &channel.Conversation{Key: channel.Key{Channel: "cli", Thread: "default"}}
	var replies []string
	if err := runner.HandleTurn(context.Background(), conv, "hello", captureReply(&replies)); err != nil {
		t.Fatalf("HandleTurn returned error: %v", err)
	}
	if len(replies) != 1 || replies[0] != "hi there\n" {
		t.Fatalf("unexpected replies: %#v", replies)
	}
	if len(conv.Messages) < 2 {
		t.Fatalf("expected system prompt + user + assistant in conv, got %d messages", len(conv.Messages))
	}
	if conv.Messages[0].Role != "system" {
		t.Fatalf("expected system prompt first, got %q", conv.Messages[0].Role)
	}
}

func TestRunnerHandleTurnExecutesToolCalls(t *testing.T) {
	tempDir := t.TempDir()
	targetPath := tempDir + "/example.txt"
	args, _ := json.Marshal(map[string]any{
		"path":    targetPath,
		"content": "hello\nworld",
	})

	client := &stubClient{
		responses: []*types.ChatResponse{
			{
				Choices: []types.Choice{{
					Message: types.ResponseMessage{
						Role:    "assistant",
						Content: strPtr(""),
						ToolCalls: []types.ToolCall{
							{
								ID:   "call-1",
								Type: "function",
								Function: types.ToolCallFunction{
									Name:      "write",
									Arguments: string(args),
								},
							},
						},
					},
				}},
			},
			{
				Choices: []types.Choice{{
					Message: types.ResponseMessage{
						Role:    "assistant",
						Content: strPtr("done"),
					},
				}},
			},
		},
	}

	runner := Runner{
		Client: client,
		Registry: tools.NewRegistry(
			tools.NewWriteTool(tools.OSFS{}),
		),
	}

	conv := &channel.Conversation{Key: channel.Key{Channel: "cli", Thread: "default"}}
	var replies []string
	if err := runner.HandleTurn(context.Background(), conv, "write the file", captureReply(&replies)); err != nil {
		t.Fatalf("HandleTurn returned error: %v", err)
	}
	if len(replies) != 1 || replies[0] != "done\n" {
		t.Fatalf("unexpected replies: %#v", replies)
	}
}

func TestRunnerHandleTurnReusesExistingSystemPrompt(t *testing.T) {
	client := &stubClient{
		responses: []*types.ChatResponse{
			{Choices: []types.Choice{{Message: types.ResponseMessage{Role: "assistant", Content: strPtr("first")}}}},
			{Choices: []types.Choice{{Message: types.ResponseMessage{Role: "assistant", Content: strPtr("second")}}}},
		},
	}

	runner := Runner{Client: client, Registry: tools.NewRegistry()}
	conv := &channel.Conversation{Key: channel.Key{Channel: "cli", Thread: "default"}}
	var replies []string
	reply := captureReply(&replies)

	if err := runner.HandleTurn(context.Background(), conv, "one", reply); err != nil {
		t.Fatalf("first HandleTurn error: %v", err)
	}
	systemContent := conv.Messages[0].Content
	if err := runner.HandleTurn(context.Background(), conv, "two", reply); err != nil {
		t.Fatalf("second HandleTurn error: %v", err)
	}
	systemCount := 0
	for _, m := range conv.Messages {
		if m.Role == "system" {
			systemCount++
		}
	}
	if systemCount != 1 {
		t.Fatalf("expected 1 system message across turns, got %d", systemCount)
	}
	if conv.Messages[0].Content != systemContent {
		t.Fatalf("system prompt was rebuilt between turns")
	}
}

func TestRunnerRunConversationFiresAgentLifecycleHooksOnSuccess(t *testing.T) {
	recorder := &lifecycleRecorder{}
	client := &stubClient{
		responses: []*types.ChatResponse{
			{
				Choices: []types.Choice{{
					Message: types.ResponseMessage{
						Role:    "assistant",
						Content: strPtr("done"),
					},
				}},
			},
		},
	}

	runner := Runner{
		Client:          client,
		Registry:        tools.NewRegistry(),
		AgentID:         "main",
		AgentName:       "orchestrator",
		AgentStartHooks: []plugin.AgentStartHook{recorder},
		AgentEndHooks:   []plugin.AgentEndHook{recorder},
	}

	finalText, _, err := runner.RunConversation(context.Background(), []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
	}, nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if finalText != "done" {
		t.Fatalf("expected final text %q, got %q", "done", finalText)
	}
	if len(recorder.starts) != 1 {
		t.Fatalf("expected 1 start hook call, got %d", len(recorder.starts))
	}
	if len(recorder.ends) != 1 {
		t.Fatalf("expected 1 end hook call, got %d", len(recorder.ends))
	}
	if recorder.starts[0].AgentID != "main" || recorder.starts[0].AgentName != "orchestrator" {
		t.Fatalf("unexpected start context: %+v", recorder.starts[0])
	}
	if recorder.ends[0].ctx.AgentID != "main" || recorder.ends[0].ctx.AgentName != "orchestrator" {
		t.Fatalf("unexpected end context: %+v", recorder.ends[0].ctx)
	}
	if recorder.ends[0].finalText != "done" {
		t.Fatalf("expected final text in end hook, got %q", recorder.ends[0].finalText)
	}
	if recorder.ends[0].err != nil {
		t.Fatalf("expected nil error in end hook, got %v", recorder.ends[0].err)
	}
}

func TestRunnerRunConversationFiresAgentLifecycleHooksOnError(t *testing.T) {
	recorder := &lifecycleRecorder{}
	client := &stubClient{
		errors: []error{errors.New("boom")},
	}

	runner := Runner{
		Client:          client,
		Registry:        tools.NewRegistry(),
		AgentID:         "subagent-1",
		AgentName:       "worker",
		AgentStartHooks: []plugin.AgentStartHook{recorder},
		AgentEndHooks:   []plugin.AgentEndHook{recorder},
	}

	finalText, _, err := runner.RunConversation(context.Background(), []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
	}, nil)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected boom error, got %v", err)
	}
	if finalText != "" {
		t.Fatalf("expected empty final text, got %q", finalText)
	}
	if len(recorder.starts) != 1 {
		t.Fatalf("expected 1 start hook call, got %d", len(recorder.starts))
	}
	if len(recorder.ends) != 1 {
		t.Fatalf("expected 1 end hook call, got %d", len(recorder.ends))
	}
	if recorder.ends[0].ctx.AgentID != "subagent-1" || recorder.ends[0].ctx.AgentName != "worker" {
		t.Fatalf("unexpected end context: %+v", recorder.ends[0].ctx)
	}
	if recorder.ends[0].finalText != "" {
		t.Fatalf("expected empty final text in end hook, got %q", recorder.ends[0].finalText)
	}
	if recorder.ends[0].err == nil || recorder.ends[0].err.Error() != "boom" {
		t.Fatalf("expected boom error in end hook, got %v", recorder.ends[0].err)
	}
}

func TestRunnerRunConversationReturnsFinalText(t *testing.T) {
	client := &stubClient{
		responses: []*types.ChatResponse{
			{
				Choices: []types.Choice{{
					Message: types.ResponseMessage{
						Role:    "assistant",
						Content: strPtr("conversation result"),
					},
				}},
			},
		},
	}

	runner := Runner{
		Client:   client,
		Registry: tools.NewRegistry(),
	}

	text, messages, err := runner.RunConversation(context.Background(), []types.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "hello"},
	}, nil)
	if err != nil {
		t.Fatalf("RunConversation returned error: %v", err)
	}
	if text != "conversation result" {
		t.Fatalf("unexpected final text: %q", text)
	}
	if got := messages[len(messages)-1].Content; got != "conversation result" {
		t.Fatalf("expected assistant message appended, got %#v", got)
	}
}

func TestRunnerRunConversationRetriesEmptyResponseAfterToolCalls(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"path": "main.go"})
	client := &stubClient{
		responses: []*types.ChatResponse{
			{
				Choices: []types.Choice{{
					Message: types.ResponseMessage{
						Role:    "assistant",
						Content: strPtr(""),
						ToolCalls: []types.ToolCall{
							{
								ID:   "call-1",
								Type: "function",
								Function: types.ToolCallFunction{
									Name:      "read",
									Arguments: string(args),
								},
							},
						},
					},
				}},
			},
			{
				Choices: []types.Choice{{
					Message: types.ResponseMessage{
						Role:    "assistant",
						Content: strPtr(""),
					},
				}},
			},
			{
				Choices: []types.Choice{{
					Message: types.ResponseMessage{
						Role:    "assistant",
						Content: strPtr("Final answer"),
					},
				}},
			},
		},
	}

	runner := Runner{
		Client: client,
		Registry: tools.NewRegistry(
			stubTool{name: "read", output: "file contents"},
		),
	}

	text, messages, err := runner.RunConversation(context.Background(), []types.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "inspect"},
	}, nil)
	if err != nil {
		t.Fatalf("RunConversation returned error: %v", err)
	}
	if text != "Final answer" {
		t.Fatalf("unexpected final text: %q", text)
	}
	if len(messages) < 2 {
		t.Fatalf("expected appended recovery messages")
	}
	foundRecoveryPrompt := false
	for _, msg := range messages {
		if msg.Role == "user" && msg.Content == emptyResponseRecoveryPrompt {
			foundRecoveryPrompt = true
			break
		}
	}
	if !foundRecoveryPrompt {
		t.Fatalf("expected recovery prompt to be injected, got %#v", messages)
	}
}

func TestRunnerRecoversFromEmptyAfterBlockedToolCall(t *testing.T) {
	findArgs, _ := json.Marshal(map[string]any{"pattern": "*.go"})
	newListArgs, _ := json.Marshal(map[string]any{"action": "new-list", "text": "Find Go files"})
	addArgs, _ := json.Marshal(map[string]any{"action": "add", "text": "Find all Go files"})
	toggleArgs, _ := json.Marshal(map[string]any{"action": "toggle", "id": 1})

	client := &stubClient{
		responses: []*types.ChatResponse{
			{
				Choices: []types.Choice{{
					Message: types.ResponseMessage{
						Role:    "assistant",
						Content: strPtr(""),
						ToolCalls: []types.ToolCall{{
							ID:   "call-1",
							Type: "function",
							Function: types.ToolCallFunction{
								Name:      "find",
								Arguments: string(findArgs),
							},
						}},
					},
				}},
			},
			{
				Choices: []types.Choice{{
					Message: types.ResponseMessage{
						Role:    "assistant",
						Content: strPtr(""),
					},
				}},
			},
			{
				Choices: []types.Choice{{
					Message: types.ResponseMessage{
						Role:    "assistant",
						Content: strPtr(""),
						ToolCalls: []types.ToolCall{{
							ID:   "call-2",
							Type: "function",
							Function: types.ToolCallFunction{
								Name:      "tilldone",
								Arguments: string(newListArgs),
							},
						}},
					},
				}},
			},
			{
				Choices: []types.Choice{{
					Message: types.ResponseMessage{
						Role:    "assistant",
						Content: strPtr(""),
					},
				}},
			},
			{
				Choices: []types.Choice{{
					Message: types.ResponseMessage{
						Role:    "assistant",
						Content: strPtr(""),
						ToolCalls: []types.ToolCall{
							{
								ID:   "call-3",
								Type: "function",
								Function: types.ToolCallFunction{
									Name:      "tilldone",
									Arguments: string(addArgs),
								},
							},
							{
								ID:   "call-4",
								Type: "function",
								Function: types.ToolCallFunction{
									Name:      "tilldone",
									Arguments: string(toggleArgs),
								},
							},
							{
								ID:   "call-5",
								Type: "function",
								Function: types.ToolCallFunction{
									Name:      "find",
									Arguments: string(findArgs),
								},
							},
						},
					},
				}},
			},
			{
				Choices: []types.Choice{{
					Message: types.ResponseMessage{
						Role:    "assistant",
						Content: strPtr("done"),
					},
				}},
			},
		},
	}

	registry := tools.NewRegistry(
		stubTool{name: "find", output: "cmd/kroos/main.go"},
		stubTool{name: "tilldone", output: "ok"},
	)
	registry.AddHook(&blockingHook{blockedTool: "find", reason: "No tasks defined."})

	runner := Runner{
		Client:   client,
		Registry: registry,
	}

	_, messages, err := runner.RunConversation(context.Background(), []types.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "Find all the Go files in this repo"},
	}, registry.Schemas())
	if err != nil {
		t.Fatalf("RunConversation returned error: %v", err)
	}

	var recoveryPrompts []string
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}
		content, ok := msg.Content.(string)
		if !ok {
			continue
		}
		if content == emptyResponseRecoveryPrompt {
			recoveryPrompts = append(recoveryPrompts, content)
		}
		if content == "Present the tool results to the user. Do not call any tools." {
			t.Fatal("unexpected legacy recovery prompt in conversation")
		}
	}
	if len(recoveryPrompts) != 2 {
		t.Fatalf("expected 2 recovery prompts, got %d", len(recoveryPrompts))
	}
	finalText, ok := messages[len(messages)-1].Content.(string)
	if !ok {
		t.Fatalf("expected final assistant message to be a string, got %#v", messages[len(messages)-1].Content)
	}
	if finalText != "done" {
		t.Fatalf("expected recovered final answer, got %q", finalText)
	}
}

func TestRunnerFallbackNoResponseIncludesToolResults(t *testing.T) {
	msg := fallbackNoResponseMessage([]types.Message{
		{Role: "system", Content: "system"},
		{Role: "tool", Content: "error: blocked by hook: No tasks defined."},
		{Role: "tool", Content: "New list: \"Find Go files\""},
	}, true)

	if !strings.Contains(msg, "[no response after tool calls]") {
		t.Fatalf("expected tool-call fallback header, got %q", msg)
	}
	if !strings.Contains(msg, "error: blocked by hook: No tasks defined.") {
		t.Fatalf("expected blocked tool result in fallback, got %q", msg)
	}
	if !strings.Contains(msg, "New list: \"Find Go files\"") {
		t.Fatalf("expected successful tool result in fallback, got %q", msg)
	}
}

func TestRunnerSkipsRecoveryRetryAfterSuccessfulToolResult(t *testing.T) {
	findArgs, _ := json.Marshal(map[string]any{"pattern": "*.go"})

	client := &stubClient{
		responses: []*types.ChatResponse{
			{
				Choices: []types.Choice{{
					Message: types.ResponseMessage{
						Role:    "assistant",
						Content: strPtr(""),
						ToolCalls: []types.ToolCall{{
							ID:   "call-1",
							Type: "function",
							Function: types.ToolCallFunction{
								Name:      "find",
								Arguments: string(findArgs),
							},
						}},
					},
				}},
			},
			{
				Choices: []types.Choice{{
					Message: types.ResponseMessage{
						Role:    "assistant",
						Content: strPtr(""),
					},
				}},
			},
			{
				Choices: []types.Choice{{
					Message: types.ResponseMessage{
						Role:    "assistant",
						Content: strPtr("Final answer based on tool output:\ncmd/kroos/main.go\ninternal/cli/runner.go"),
					},
				}},
			},
		},
	}

	registry := tools.NewRegistry(
		stubTool{name: "find", output: "cmd/kroos/main.go\ninternal/cli/runner.go"},
	)

	runner := Runner{
		Client:   client,
		Registry: registry,
	}

	text, messages, err := runner.RunConversation(context.Background(), []types.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "Find all the Go files in this project"},
	}, registry.Schemas())
	if err != nil {
		t.Fatalf("RunConversation returned error: %v", err)
	}
	if client.calls != 3 {
		t.Fatalf("expected 3 chat calls, got %d", client.calls)
	}
	if !strings.Contains(text, "Final answer based on tool output") {
		t.Fatalf("expected recovered final answer, got %q", text)
	}
	if !strings.Contains(text, "cmd/kroos/main.go") {
		t.Fatalf("expected tool output in fallback text, got %q", text)
	}
	foundRecoveryPrompt := false
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}
		content, ok := msg.Content.(string)
		if ok && content == emptyResponseRecoveryPrompt {
			foundRecoveryPrompt = true
		}
	}
	if !foundRecoveryPrompt {
		t.Fatal("expected recovery prompt after empty response following successful tool output")
	}
}

func TestBuildScheduledTasksSectionEmpty(t *testing.T) {
	if got := buildScheduledTasksSection(nil); got != "" {
		t.Fatalf("expected empty string for nil jobs, got %q", got)
	}
	disabled := []schedule.Job{{Name: "x", Enabled: false}}
	if got := buildScheduledTasksSection(disabled); got != "" {
		t.Fatalf("expected empty string when no enabled jobs, got %q", got)
	}
}

func TestBuildScheduledTasksSectionRendersEnabledJobs(t *testing.T) {
	at := time.Date(2026, 4, 13, 11, 0, 0, 0, time.UTC)
	jobs := []schedule.Job{
		{
			ID:        "job1",
			Name:      "standup",
			Enabled:   true,
			Schedule:  schedule.Schedule{Kind: schedule.KindAt, At: &at},
			Text:      "daily standup reminder",
			NextRunAt: &at,
		},
		{ID: "job2", Name: "ignored", Enabled: false},
	}
	got := buildScheduledTasksSection(jobs)

	for _, want := range []string{
		"<scheduled_tasks>",
		"</scheduled_tasks>",
		"<id>job1</id>",
		"<name>standup</name>",
		"at 2026-04-13T11:00:00Z",
		"daily standup reminder",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in section, got %q", want, got)
		}
	}
	if strings.Contains(got, "job2") {
		t.Fatalf("disabled job should be omitted: %q", got)
	}
}

func TestComposePluginPromptsIncludesScheduledTasks(t *testing.T) {
	dir := t.TempDir()
	store, err := schedule.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(time.Hour).UTC()
	if _, err := store.Add(schedule.Job{
		Name:     "deploy",
		Enabled:  true,
		Schedule: schedule.Schedule{Kind: schedule.KindAt, At: &at},
		Text:     "ship it",
	}); err != nil {
		t.Fatal(err)
	}

	runner := Runner{
		PluginPrompts: []string{"# Plugin\nplugin body"},
		Scheduler:     schedule.NewScheduler(store, 4),
	}

	sections := runner.composePluginPrompts()
	if len(sections) != 2 {
		t.Fatalf("expected 2 sections (plugin + scheduled), got %d: %+v", len(sections), sections)
	}
	if !strings.Contains(sections[0], "plugin body") {
		t.Fatalf("expected first section to be plugin prompt, got %q", sections[0])
	}
	if !strings.Contains(sections[1], "<scheduled_tasks>") || !strings.Contains(sections[1], "deploy") {
		t.Fatalf("expected scheduled_tasks section with job, got %q", sections[1])
	}
}

func TestComposePluginPromptsWithoutSchedulerReturnsPluginOnly(t *testing.T) {
	runner := Runner{PluginPrompts: []string{"only"}}
	sections := runner.composePluginPrompts()
	if len(sections) != 1 || sections[0] != "only" {
		t.Fatalf("unexpected sections: %+v", sections)
	}
}

func TestRunnerHandleTurnUsesConversationModelOverride(t *testing.T) {
	def := &stubClient{}
	override := &stubClient{responses: []*types.ChatResponse{{
		Choices: []types.Choice{{Message: types.ResponseMessage{Role: "assistant", Content: strPtr("from override")}}},
	}}}
	var gotProv, gotModel string
	runner := Runner{
		Client:   def,
		Registry: tools.NewRegistry(),
		ClientFor: func(p, m string) (llm.ChatClient, error) {
			gotProv, gotModel = p, m
			return override, nil
		},
	}
	conv := &channel.Conversation{Key: channel.Key{Channel: "cli", Thread: "default"}, Provider: "omlx", Model: "gemma"}
	var replies []string
	if err := runner.HandleTurn(context.Background(), conv, "hi", captureReply(&replies)); err != nil {
		t.Fatal(err)
	}
	if def.calls != 0 || override.calls != 1 || gotProv != "omlx" || gotModel != "gemma" {
		t.Fatalf("def=%d override=%d prov=%q model=%q", def.calls, override.calls, gotProv, gotModel)
	}
}
