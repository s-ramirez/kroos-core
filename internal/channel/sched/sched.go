// Package sched wraps a schedule.Scheduler as a channel.Channel so
// scheduled events flow through the same dispatcher path as regular
// user messages.
package sched

import (
	"context"
	"fmt"
	"sync"
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

// HeartbeatDelivery configures how the heartbeat job's replies are
// handled. When set on a Channel, all heartbeat events gain:
//
//   - Active-hours gating: ticks outside the configured window are
//     dropped before they reach the dispatcher.
//   - HEARTBEAT_OK suppression: if the model returns only the sentinel
//     token the turn is pruned from conversation history and nothing is
//     delivered.
//   - Outbound routing: non-empty replies are sent to the configured
//     target channel instead of (or instead of only) the CLI.
//   - Duplicate suppression: identical replies within 24 h are silently
//     dropped on non-CLI targets.
type HeartbeatDelivery struct {
	// Target controls where non-empty, non-token replies are delivered.
	//   "cli"      – delivered via CLISend (default / legacy behavior)
	//   "none"     – silently discarded; agent still runs
	//   any other  – delivered via OutboundSend (e.g. "imessage", "discord")
	Target string

	// ActiveStart and ActiveEnd are "HH:MM" strings (24-hour) that define
	// the window during which heartbeat ticks are allowed to fire. An empty
	// string for either disables the gate (always active).
	ActiveStart string
	ActiveEnd   string
	// Timezone is an IANA location name for active-hours comparisons.
	// Empty means local system time.
	Timezone string

	// Model, if non-empty, overrides the LLM model used for heartbeat turns.
	Model string

	// CLISend delivers the reply to the terminal. Called when Target=="cli".
	// Typically wraps cliChannel.Send.
	CLISend func(ctx context.Context, content string) error

	// OutboundSend delivers the reply to an external channel. Called when
	// Target is neither "cli" nor "none".
	OutboundSend func(ctx context.Context, content string) error

	mu       sync.Mutex
	lastText string
	lastAt   time.Time
}

// intercept is the InterceptReply function attached to heartbeat Inbound
// messages. It performs HEARTBEAT_OK detection, dedup, and routing.
func (h *HeartbeatDelivery) intercept(ctx context.Context, content string) (pruneLastTurn bool, err error) {
	// HEARTBEAT_OK → prune this turn from conversation history.
	if heartbeat.IsOnlyToken(content) {
		return true, nil
	}

	// Deduplicate identical replies within 24 h (non-CLI targets only).
	target := h.Target
	if target != "cli" && target != "" {
		h.mu.Lock()
		isDup := content == h.lastText && !h.lastAt.IsZero() && time.Since(h.lastAt) < 24*time.Hour
		h.mu.Unlock()
		if isDup {
			return false, nil
		}
	}

	// Route the reply.
	switch target {
	case "", "cli":
		if h.CLISend != nil {
			err = h.CLISend(ctx, content)
		}
	case "none":
		// intentionally silent
	default: // "imessage", "discord", etc.
		if h.OutboundSend != nil {
			err = h.OutboundSend(ctx, content)
		}
	}

	// Record last delivered text for future dedup (non-CLI targets only).
	if err == nil && target != "cli" && target != "" {
		h.mu.Lock()
		h.lastText = content
		h.lastAt = time.Now()
		h.mu.Unlock()
	}
	return false, err
}

// Channel wraps a schedule.Scheduler. Events fired by the scheduler are
// formatted as <scheduled_event> XML blocks and pushed onto the inbound
// bus with Kind=KindEvent, addressed to Target.
type Channel struct {
	Scheduler *schedule.Scheduler
	Target    channel.Key

	// Heartbeat, if non-nil, enables the enhanced heartbeat delivery
	// pipeline (active-hours gating, HEARTBEAT_OK suppression, outbound
	// routing, dedup) for events named "heartbeat".
	Heartbeat *HeartbeatDelivery

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

			if ev.Mode == schedule.ModeTask && c.RunTask != nil {
				c.RunTask(ev)
				continue
			}

			// Heartbeat-specific pre-flight: active-hours gate.
			if (ev.Name == "heartbeat") && c.Heartbeat != nil {
				hb := c.Heartbeat
				if !heartbeat.IsWithinActiveHours(hb.ActiveStart, hb.ActiveEnd, hb.Timezone, time.Now()) {
					// Outside active window — skip this tick silently.
					continue
				}
			}

			msg := channel.Inbound{
				Channel:    c.Target.Channel,
				Thread:     c.Target.Thread,
				Sender:     "scheduler",
				Content:    FormatEvent(ev, time.Now()),
				Kind:       channel.KindEvent,
				ReceivedAt: time.Now(),
			}

			// Attach the intercept hook and optional model override for heartbeat and meal reminder events.
			if (ev.Name == "heartbeat") && c.Heartbeat != nil {
				msg.InterceptReply = c.Heartbeat.intercept
				msg.ModelOverride = c.Heartbeat.Model
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
