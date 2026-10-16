package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/channel"
	"github.com/s-ramirez/kroos-core/internal/schedule"
	"github.com/s-ramirez/kroos-core/internal/tasks"
	"github.com/s-ramirez/kroos-core/internal/types"
)

type echoAgent struct{}

func (echoAgent) RunConversation(_ context.Context, m []types.Message, _ []types.ChatRequestTool) (string, []types.Message, error) {
	return "ran", m, nil
}

func newTaskTool(t *testing.T) (*TaskTool, *schedule.Store, *tasks.Executor) {
	t.Helper()
	store, err := schedule.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exec := tasks.NewExecutor(context.Background(), t.TempDir(), func(tasks.Task) (tasks.AgentSpec, error) {
		return tasks.AgentSpec{Agent: echoAgent{}}, nil
	}, nil)
	return NewTaskTool(store, exec), store, exec
}

func runTask(t *testing.T, tool *TaskTool, ctx context.Context, p TaskParams) ToolResult {
	t.Helper()
	raw, _ := json.Marshal(p)
	res, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

func TestTaskAddDefaultsToCurrentChat(t *testing.T) {
	tool, store, _ := newTaskTool(t)
	ctx := channel.WithKey(context.Background(), channel.Key{Channel: "telegram", Thread: "42"})
	res := runTask(t, tool, ctx, TaskParams{Action: "add", Name: "flight", Prompt: "check", Every: "6h"})
	if res.IsError {
		t.Fatal(res.Output)
	}
	jobs := store.List()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	j := jobs[0]
	if j.Mode != schedule.ModeTask || j.Deliver.Target != "telegram" || j.Deliver.To != "42" || j.Schedule.Kind != schedule.KindEvery {
		t.Fatalf("unexpected job: %+v", j)
	}
}

func TestTaskAddRemindAndValidation(t *testing.T) {
	tool, store, _ := newTaskTool(t)
	ctx := context.Background()
	if res := runTask(t, tool, ctx, TaskParams{Action: "add", Name: "r", Prompt: "stretch", Mode: "remind", Delay: "15m"}); res.IsError {
		t.Fatal(res.Output)
	}
	if j := store.List()[0]; j.Mode != schedule.ModeRemind || j.Schedule.Kind != schedule.KindAt || j.Deliver.Target != "" {
		t.Fatalf("unexpected reminder: %+v", j)
	}
	bad := []TaskParams{
		{Action: "add", Prompt: "x", Delay: "1m"},                         // no name
		{Action: "add", Name: "n", Delay: "1m"},                           // no prompt
		{Action: "add", Name: "n", Prompt: "x"},                           // no schedule
		{Action: "add", Name: "n", Prompt: "x", Delay: "1m", Every: "1h"}, // two schedules
		{Action: "add", Name: "n", Prompt: "x", Cron: "bad"},
		{Action: "add", Name: "n", Prompt: "x", Every: "1h", Mode: "x"},
		{Action: "nope"},
	}
	for i, p := range bad {
		if res := runTask(t, tool, ctx, p); !res.IsError {
			t.Errorf("case %d: expected error, got %s", i, res.Output)
		}
	}
}

func TestTaskRunNowAndHistory(t *testing.T) {
	tool, store, exec := newTaskTool(t)
	ctx := context.Background()
	runTask(t, tool, ctx, TaskParams{Action: "add", Name: "flight", Prompt: "check", Every: "6h", DeliverTarget: "none"})
	id := store.List()[0].ID

	if res := runTask(t, tool, ctx, TaskParams{Action: "run_now", ID: id}); res.IsError {
		t.Fatal(res.Output)
	}
	exec.Wait()
	res := runTask(t, tool, ctx, TaskParams{Action: "history", ID: id})
	if res.IsError || !strings.Contains(res.Output, "ran") || !strings.Contains(res.Output, "ok") {
		t.Fatalf("unexpected history: %s", res.Output)
	}
	if res := runTask(t, tool, ctx, TaskParams{Action: "remove", ID: id}); res.IsError {
		t.Fatal(res.Output)
	}
	if res := runTask(t, tool, ctx, TaskParams{Action: "list"}); res.Output != "no scheduled tasks" {
		t.Fatalf("expected empty list, got %s", res.Output)
	}
}
