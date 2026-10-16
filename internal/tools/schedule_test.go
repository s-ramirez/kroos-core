package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/s-ramirez/kroos-core/internal/schedule"
)

func newScheduleTool(t *testing.T) (*ScheduleTool, *schedule.Store) {
	t.Helper()
	store, err := schedule.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewScheduleTool(store), store
}

func runTool(t *testing.T, tool *ScheduleTool, params ScheduleParams) ToolResult {
	t.Helper()
	raw, _ := json.Marshal(params)
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("tool returned error: %v", err)
	}
	return res
}

func TestScheduleAddAt(t *testing.T) {
	tool, store := newScheduleTool(t)
	at := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)

	res := runTool(t, tool, ScheduleParams{
		Action: "add",
		Name:   "deploy reminder",
		Kind:   "at",
		At:     at,
		Text:   "check on the staging deploy",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "scheduled") || !strings.Contains(res.Output, "deploy reminder") {
		t.Fatalf("unexpected output: %s", res.Output)
	}

	jobs := store.List()
	if len(jobs) != 1 || jobs[0].Schedule.Kind != schedule.KindAt {
		t.Fatalf("expected 1 at job, got %+v", jobs)
	}
	if jobs[0].WakeMode != schedule.WakeModeNow {
		t.Fatalf("expected default WakeMode=now, got %q", jobs[0].WakeMode)
	}
}

func TestScheduleAddEvery(t *testing.T) {
	tool, store := newScheduleTool(t)

	res := runTool(t, tool, ScheduleParams{
		Action: "add",
		Name:   "ping",
		Kind:   "every",
		Every:  "15m",
		Text:   "check the queue",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}

	jobs := store.List()
	if len(jobs) != 1 || jobs[0].Schedule.Kind != schedule.KindEvery {
		t.Fatalf("expected 1 every job, got %+v", jobs)
	}
	if time.Duration(jobs[0].Schedule.Every) != 15*time.Minute {
		t.Fatalf("expected every=15m, got %v", time.Duration(jobs[0].Schedule.Every))
	}
	if jobs[0].Schedule.Anchor == nil {
		t.Fatal("expected anchor to default to now")
	}
}

func TestScheduleAddEveryWithAnchor(t *testing.T) {
	tool, store := newScheduleTool(t)
	anchor := time.Date(2026, 4, 13, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)

	res := runTool(t, tool, ScheduleParams{
		Action: "add",
		Name:   "standup",
		Kind:   "every",
		Every:  "24h",
		Anchor: anchor,
		Text:   "daily standup",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}

	jobs := store.List()
	if jobs[0].Schedule.Anchor.Format(time.RFC3339) != anchor {
		t.Fatalf("expected anchor=%s, got %v", anchor, jobs[0].Schedule.Anchor)
	}
}

func TestScheduleListEmpty(t *testing.T) {
	tool, _ := newScheduleTool(t)
	res := runTool(t, tool, ScheduleParams{Action: "list"})
	if res.IsError || res.Output != "no scheduled jobs" {
		t.Fatalf("unexpected: %+v", res)
	}
}

func TestScheduleListReturnsJobs(t *testing.T) {
	tool, _ := newScheduleTool(t)
	at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	runTool(t, tool, ScheduleParams{Action: "add", Name: "a", Kind: "at", At: at, Text: "alpha"})
	runTool(t, tool, ScheduleParams{Action: "add", Name: "b", Kind: "every", Every: "30m", Text: "beta"})

	res := runTool(t, tool, ScheduleParams{Action: "list"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "alpha") || !strings.Contains(res.Output, "beta") {
		t.Fatalf("expected both jobs in output: %s", res.Output)
	}
	if !strings.Contains(res.Output, "every 30m") {
		t.Fatalf("expected every schedule descriptor: %s", res.Output)
	}
}

func TestScheduleRemove(t *testing.T) {
	tool, store := newScheduleTool(t)
	at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	add := runTool(t, tool, ScheduleParams{Action: "add", Name: "x", Kind: "at", At: at, Text: "x"})
	id := store.List()[0].ID
	if !strings.Contains(add.Output, id) {
		t.Fatalf("expected add output to contain id %s, got %s", id, add.Output)
	}

	res := runTool(t, tool, ScheduleParams{Action: "remove", ID: id})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if jobs := store.List(); len(jobs) != 0 {
		t.Fatalf("expected empty after remove, got %+v", jobs)
	}
}

func TestScheduleRemoveNotFound(t *testing.T) {
	tool, _ := newScheduleTool(t)
	res := runTool(t, tool, ScheduleParams{Action: "remove", ID: "deadbeef"})
	if !res.IsError {
		t.Fatalf("expected error, got %s", res.Output)
	}
}

func TestScheduleWakeCreatesAtJob(t *testing.T) {
	tool, store := newScheduleTool(t)
	res := runTool(t, tool, ScheduleParams{
		Action: "wake",
		Delay:  "15m",
		Text:   "remind me",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "wake in 15m") {
		t.Fatalf("expected wake verb in output: %s", res.Output)
	}

	jobs := store.List()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %+v", jobs)
	}
	if jobs[0].Schedule.Kind != schedule.KindAt {
		t.Fatalf("expected wake to create an at job, got %s", jobs[0].Schedule.Kind)
	}
	if jobs[0].Name != "wake" {
		t.Fatalf("expected default name 'wake', got %q", jobs[0].Name)
	}
	now := store.Now()
	if jobs[0].Schedule.At.Before(now.Add(14*time.Minute)) || jobs[0].Schedule.At.After(now.Add(16*time.Minute)) {
		t.Fatalf("expected at ~ now+15m, got %v (now=%v)", jobs[0].Schedule.At, now)
	}
}

func TestScheduleInvalidAction(t *testing.T) {
	tool, _ := newScheduleTool(t)
	res := runTool(t, tool, ScheduleParams{Action: "frobnicate"})
	if !res.IsError {
		t.Fatalf("expected error, got %s", res.Output)
	}
}

func TestScheduleAddMissingName(t *testing.T) {
	tool, _ := newScheduleTool(t)
	at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	res := runTool(t, tool, ScheduleParams{Action: "add", Kind: "at", At: at, Text: "x"})
	if !res.IsError {
		t.Fatal("expected error for missing name")
	}
}

func TestScheduleAddMissingText(t *testing.T) {
	tool, _ := newScheduleTool(t)
	at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	res := runTool(t, tool, ScheduleParams{Action: "add", Name: "x", Kind: "at", At: at})
	if !res.IsError {
		t.Fatal("expected error for missing text")
	}
}

func TestScheduleAddMissingKind(t *testing.T) {
	tool, _ := newScheduleTool(t)
	res := runTool(t, tool, ScheduleParams{Action: "add", Name: "x", Text: "y"})
	if !res.IsError {
		t.Fatal("expected error for missing kind")
	}
}

func TestScheduleAddInvalidAt(t *testing.T) {
	tool, _ := newScheduleTool(t)
	res := runTool(t, tool, ScheduleParams{Action: "add", Name: "x", Kind: "at", At: "tomorrow", Text: "y"})
	if !res.IsError {
		t.Fatal("expected error for invalid at")
	}
}

func TestScheduleAddInvalidEvery(t *testing.T) {
	tool, _ := newScheduleTool(t)
	res := runTool(t, tool, ScheduleParams{Action: "add", Name: "x", Kind: "every", Every: "soon", Text: "y"})
	if !res.IsError {
		t.Fatal("expected error for invalid every")
	}
}

func TestScheduleAddCron(t *testing.T) {
	tool, store := newScheduleTool(t)

	res := runTool(t, tool, ScheduleParams{
		Action: "add",
		Name:   "standup",
		Kind:   "cron",
		Cron:   "0 9 * * 1-5",
		Text:   "weekday standup",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}

	jobs := store.List()
	if len(jobs) != 1 || jobs[0].Schedule.Kind != schedule.KindCron {
		t.Fatalf("expected 1 cron job, got %+v", jobs)
	}
	if jobs[0].Schedule.Cron != "0 9 * * 1-5" {
		t.Fatalf("expected cron=%q, got %q", "0 9 * * 1-5", jobs[0].Schedule.Cron)
	}
	if jobs[0].NextRunAt == nil {
		t.Fatal("expected NextRunAt to be computed for cron job")
	}
}

func TestScheduleAddCronMissingExpression(t *testing.T) {
	tool, _ := newScheduleTool(t)
	res := runTool(t, tool, ScheduleParams{Action: "add", Name: "x", Kind: "cron", Text: "y"})
	if !res.IsError {
		t.Fatal("expected error for missing cron")
	}
}

func TestScheduleAddCronInvalid(t *testing.T) {
	tool, _ := newScheduleTool(t)
	res := runTool(t, tool, ScheduleParams{Action: "add", Name: "x", Kind: "cron", Cron: "not a cron", Text: "y"})
	if !res.IsError {
		t.Fatal("expected error for invalid cron")
	}
}

func TestScheduleListShowsCronDescriptor(t *testing.T) {
	tool, _ := newScheduleTool(t)
	runTool(t, tool, ScheduleParams{
		Action: "add", Name: "standup", Kind: "cron", Cron: "0 9 * * 1-5", Text: "standup",
	})

	res := runTool(t, tool, ScheduleParams{Action: "list"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "cron 0 9 * * 1-5") {
		t.Fatalf("expected cron descriptor in output: %s", res.Output)
	}
}

func TestScheduleAddInvalidWakeMode(t *testing.T) {
	tool, _ := newScheduleTool(t)
	at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	res := runTool(t, tool, ScheduleParams{Action: "add", Name: "x", Kind: "at", At: at, Text: "y", WakeMode: "loud"})
	if !res.IsError {
		t.Fatal("expected error for invalid wakeMode")
	}
}

func TestScheduleWakeMissingDelay(t *testing.T) {
	tool, _ := newScheduleTool(t)
	res := runTool(t, tool, ScheduleParams{Action: "wake", Text: "x"})
	if !res.IsError {
		t.Fatal("expected error for missing delay")
	}
}

func TestScheduleWakeMissingText(t *testing.T) {
	tool, _ := newScheduleTool(t)
	res := runTool(t, tool, ScheduleParams{Action: "wake", Delay: "5m"})
	if !res.IsError {
		t.Fatal("expected error for missing text")
	}
}

func TestScheduleAddCronAtSpecificLocalTimes(t *testing.T) {
	tool, store := newScheduleTool(t)

	cases := []struct {
		name string
		cron string
	}{
		{name: "breakfast", cron: "0 9 * * *"},
		{name: "lunch", cron: "0 14 * * *"},
		{name: "dinner", cron: "0 20 * * *"},
	}

	for _, tc := range cases {
		res := runTool(t, tool, ScheduleParams{
			Action: "add",
			Name:   tc.name,
			Kind:   "cron",
			Cron:   tc.cron,
			Text:   "meal check",
		})
		if res.IsError {
			t.Fatalf("unexpected error for %s: %s", tc.name, res.Output)
		}
	}

	jobs := store.List()
	if len(jobs) != 3 {
		t.Fatalf("expected 3 cron jobs, got %+v", jobs)
	}
	for _, job := range jobs {
		if job.Schedule.Kind != schedule.KindCron {
			t.Fatalf("expected cron job, got %+v", job)
		}
		if job.NextRunAt == nil {
			t.Fatalf("expected NextRunAt for %s", job.Name)
		}
	}
}

func TestScheduleEmptyAction(t *testing.T) {
	tool, _ := newScheduleTool(t)
	res := runTool(t, tool, ScheduleParams{})
	if !res.IsError {
		t.Fatal("expected error for empty action")
	}
}
