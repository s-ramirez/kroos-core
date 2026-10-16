// Package sched wraps a schedule.Scheduler as a channel.Channel so
// scheduled events flow through the same dispatcher path as regular
// user messages.
package sched

import (
	"context"
	"fmt"
	"time"

	"github.com/s-ramirez/kroos-core/internal/channel"
	"github.com/s-ramirez/kroos-core/internal/heartbeat"
	"github.com/s-ramirez/kroos-core/internal/schedule"
)

// Name is the channel name used for self-identification in the registry.
// Note that Inbound messages produced by this adapter carry the *target*
// channel/thread, not this name — scheduled events are injected into an
// existing conversation (typically the CLI) rather than creating their
// own.
const Name = "sched"

// Channel wraps a schedule.Scheduler. Events fired by the scheduler are
// formatted as <scheduled_event> XML blocks and pushed onto the inbound
// bus with Kind=KindEvent, addressed to Target.
type Channel struct {
	Scheduler *schedule.Scheduler
	Target    channel.Key

	// RunTask, if set, receives mode=task events instead of the bus:
	// they run as isolated background tasks, not in Target's conversation.
	RunTask func(schedule.Event)
}

func (c *Channel) Name() string { return Name }

// Listen forwards scheduler events onto the bus until ctx is cancelled.
func (c *Channel) Listen(ctx context.Context, out chan<- channel.Inbound) error {
	if c.Scheduler == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	events := c.Scheduler.Events()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return nil
			}

			if h := ev.ActiveHours; h != nil && !heartbeat.IsWithinActiveHours(h.Start, h.End, h.Timezone, time.Now()) {
				continue // outside the job's active window
			}
			if ev.Mode == schedule.ModeTask && c.RunTask != nil {
				c.RunTask(ev)
				continue
			}

			msg := channel.Inbound{
				Channel:    c.Target.Channel,
				Thread:     c.Target.Thread,
				Sender:     "scheduler",
				Content:    FormatEvent(ev, time.Now()),
				Kind:       channel.KindEvent,
				ReceivedAt: time.Now(),
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case out <- msg:
			}
		}
	}
}

// Send is a no-op: scheduler has no notion of a "reply". The channel
// still implements Send to satisfy channel.Channel, but the dispatcher
// will never invoke it — replies are routed to Target.Channel, not here.
func (c *Channel) Send(ctx context.Context, msg channel.Outbound) error {
	_ = ctx
	_ = msg
	return nil
}

// FormatEvent wraps a fired job in the <scheduled_event> XML envelope
// the model is trained to recognize. Exported so tests can verify the
// format without reaching into unexported helpers.
func FormatEvent(ev schedule.Event, now time.Time) string {
	return fmt.Sprintf("<scheduled_event name=%q fired_at=%q>\n%s\n</scheduled_event>",
		ev.Name, now.UTC().Format(time.RFC3339), ev.Text)
}
