package sched

import (
	"strings"
	"testing"
	"time"

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
