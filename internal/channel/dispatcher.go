package channel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ReplyFunc sends text back through the originating channel/thread.
type ReplyFunc func(text string) error

// TurnHandler processes one turn for a conversation. Implementations
// can assume exclusive access to conv.Messages for the duration of the
// call; the dispatcher serializes turns per conversation.
type TurnHandler interface {
	HandleTurn(ctx context.Context, conv *Conversation, userContent string, reply ReplyFunc) error
}

// Dispatcher owns the inbound message bus. It spawns a listener per
// registered channel, consumes the shared bus sequentially, expands
// slash commands, routes each message to the right conversation, and
// wires replies back to the originating channel.
//
// Processing is currently sequential: one turn at a time, across all
// channels. This matches the project's single-LLM-backend assumption.
// Add per-conversation workers later if parallelism is needed.
type Dispatcher struct {
	Channels    *Registry
	Store       *Store
	Commands    *CommandExpander
	Handler     TurnHandler
	OnUserInput func()
	Logger      func(format string, args ...any)
}

// Run starts listeners for every registered channel and blocks until
// ctx is cancelled or all listeners exit.
func (d *Dispatcher) Run(ctx context.Context) error {
	inbound := make(chan Inbound, 16)

	listenerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// When the first listener returns, cancel the listener context so the
	// rest exit promptly. This gives us today's behavior: Ctrl-D on the
	// CLI ends the whole process, and lets the dispatcher exit cleanly
	// without blocking on long-lived listeners (e.g., scheduler).
	var wg sync.WaitGroup
	for _, ch := range d.Channels.All() {
		wg.Add(1)
		go func(c Channel) {
			defer wg.Done()
			defer cancel()
			err := c.Listen(listenerCtx, inbound)
			if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) && listenerCtx.Err() == nil {
				d.logf("channel %s listener error: %v", c.Name(), err)
			}
		}(ch)
	}

	go func() {
		wg.Wait()
		close(inbound)
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-inbound:
			if !ok {
				return nil
			}
			d.process(ctx, msg)
		}
	}
}

func (d *Dispatcher) process(ctx context.Context, in Inbound) {
	// Handle /new and /reset before command expansion: clear the
	// conversation. If the command carries a trailing body, fall through
	// and treat that body as the first user message of the fresh session.
	if matched, tail := ParseResetCommand(in.Content); matched {
		key := Key{Channel: in.Channel, Thread: in.Thread}
		d.Store.Get(key) // ensure provisioned
		d.Store.Reset(key)
		if tail == "" {
			d.replyText(ctx, in.Channel, in.Thread, "New conversation started.\n")
			return
		}
		in.Content = tail
	}

	content := in.Content
	if d.Commands != nil {
		expanded, err := d.Commands.Expand(in.Content)
		if err != nil {
			d.replyText(ctx, in.Channel, in.Thread, fmt.Sprintf("error: %v\n", err))
			return
		}
		content = expanded
	}

	if in.Kind == KindUser && d.OnUserInput != nil {
		d.OnUserInput()
	}

	conv := d.Store.Get(Key{Channel: in.Channel, Thread: in.Thread})

	// Propagate per-message model override; cleared after the turn.
	conv.ModelOverride = in.ModelOverride
	conv.Attachments = in.Attachments
	defer func() {
		conv.ModelOverride = ""
		conv.Attachments = nil
	}()

	// Snapshot message count before the turn so InterceptReply can prune it.
	preTurnLen := len(conv.Messages)

	var reply ReplyFunc
	if in.InterceptReply != nil {
		intercept := in.InterceptReply
		reply = func(text string) error {
			prune, err := intercept(ctx, text)
			if prune {
				conv.Messages = conv.Messages[:preTurnLen]
			}
			return err
		}
	} else {
		reply = d.replyFuncFor(ctx, in.Channel, in.Thread, in.MessageID)
	}

	if err := d.Handler.HandleTurn(ctx, conv, content, reply); err != nil {
		if in.InterceptReply != nil {
			// Don't route handler errors through the intercept; log instead.
			d.logf("heartbeat handler error: %v", err)
		} else {
			_ = reply(fmt.Sprintf("error: %v\n", err))
		}
	}

	if err := d.Store.Save(Key{Channel: in.Channel, Thread: in.Thread}); err != nil {
		d.logf("save conversation %s/%s: %v", in.Channel, in.Thread, err)
	}
}

func (d *Dispatcher) replyFuncFor(ctx context.Context, chName, thread, replyTo string) ReplyFunc {
	return func(text string) error {
		ch, ok := d.Channels.Lookup(chName)
		if !ok {
			d.logf("reply channel %q not found", chName)
			return nil
		}
		return ch.Send(ctx, Outbound{Channel: chName, Thread: thread, Content: text, ReplyTo: replyTo})
	}
}

func (d *Dispatcher) replyText(ctx context.Context, chName, thread, text string) error {
	ch, ok := d.Channels.Lookup(chName)
	if !ok {
		d.logf("reply channel %q not found", chName)
		return nil
	}
	return ch.Send(ctx, Outbound{Channel: chName, Thread: thread, Content: text})
}

func (d *Dispatcher) logf(format string, args ...any) {
	if d.Logger != nil {
		d.Logger(format, args...)
	}
}
