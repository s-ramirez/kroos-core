package sched

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/s-ramirez/kroos-core/internal/channel"
	"github.com/s-ramirez/kroos-core/internal/schedule"
)

func TestFormatEventEmitsScheduledEventXML(t *testing.T) {
	ev := schedule.Event{
		JobID: "abc123",
		Name:  "deploy reminder",
		Text:  "check on the staging deploy",
	}
	now := time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)
	got := FormatEvent(ev, now)

	if !strings.Contains(got, `<scheduled_event name="deploy reminder" fired_at="2026-04-13T10:00:00Z">`) {
		t.Fatalf("expected opening tag with attributes, got %q", got)
	}
	if !strings.Contains(got, "check on the staging deploy") {
		t.Fatalf("expected text payload, got %q", got)
	}
	if !strings.HasSuffix(got, "</scheduled_event>") {
		t.Fatalf("expected closing tag, got %q", got)
	}
}

func TestFormatEventEmitsMealReminderXML(t *testing.T) {
	ev := schedule.Event{
		JobID: "meal123",
		Name:  "meal-breakfast",
		Text:  "Meal check-in",
	}
	now := time.Date(2026, 4, 13, 9, 0, 0, 0, time.UTC)
	got := FormatEvent(ev, now)

	if !strings.Contains(got, `<scheduled_event name="meal-breakfast" fired_at="2026-04-13T09:00:00Z">`) {
		t.Fatalf("expected opening tag with meal reminder name, got %q", got)
	}
	if !strings.Contains(got, "Meal check-in") {
		t.Fatalf("expected text payload, got %q", got)
	}
}

func TestListenRoutesTasksAndGatesActiveHours(t *testing.T) {
	store, err := schedule.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := schedule.NewScheduler(store, 4)
	now := time.Now()
	past := now.Add(-time.Minute)
	// Closed window: start == end never matches.
	s.Inject(schedule.Job{Name: "gated", Enabled: true, Mode: schedule.ModeTask, Text: "x",
		Schedule:    schedule.Schedule{Kind: schedule.KindAt, At: &past},
		ActiveHours: &schedule.ActiveHours{Start: "10:00", End: "10:00"}})
	s.Inject(schedule.Job{Name: "hb", Enabled: true, Mode: schedule.ModeTask, Text: "check",
		Schedule: schedule.Schedule{Kind: schedule.KindAt, At: &past}})

	got := make(chan schedule.Event, 4)
	c := &Channel{Scheduler: s, RunTask: func(ev schedule.Event) { got <- ev }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()
	go c.Listen(ctx, make(chan channel.Inbound, 4))

	select {
	case ev := <-got:
		if ev.Name != "hb" {
			t.Fatalf("expected hb routed to RunTask, got %q", ev.Name)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("task event not routed")
	}
	select {
	case ev := <-got:
		t.Fatalf("gated event should be dropped, got %q", ev.Name)
	case <-time.After(1500 * time.Millisecond):
	}
}
