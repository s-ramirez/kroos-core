package channel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/s-ramirez/kroos-core/internal/types"
)

// fakeChannel is a test double that lets tests drive inbound messages
// and inspect outbound ones.
type fakeChannel struct {
	name    string
	inbound []Inbound

	mu    sync.Mutex
	sent  []Outbound
	ready chan struct{} // closed when Send has been called at least once
}

func newFakeChannel(name string, inbound ...Inbound) *fakeChannel {
	return &fakeChannel{
		name:    name,
		inbound: inbound,
		ready:   make(chan struct{}),
	}
}

func (f *fakeChannel) Name() string { return f.name }

func (f *fakeChannel) Listen(ctx context.Context, out chan<- Inbound) error {
	for _, msg := range f.inbound {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- msg:
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeChannel) Send(ctx context.Context, msg Outbound) error {
	_ = ctx
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
	select {
	case <-f.ready:
	default:
		close(f.ready)
	}
	return nil
}

func (f *fakeChannel) sends() []Outbound {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Outbound, len(f.sent))
	copy(out, f.sent)
	return out
}

// echoHandler replies with "echo: <content>" and appends the user
// message to the conversation.
type echoHandler struct {
	mu    sync.Mutex
	turns int
}

func (h *echoHandler) HandleTurn(ctx context.Context, conv *Conversation, userContent string, reply ReplyFunc) error {
	h.mu.Lock()
	h.turns++
	h.mu.Unlock()
	return reply("echo: " + userContent + "\n")
}

func (h *echoHandler) turnCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.turns
}

func TestDispatcherRoutesInboundToHandlerAndReplyBack(t *testing.T) {
	ch := newFakeChannel("cli",
		Inbound{Channel: "cli", Thread: "default", Content: "hello", Kind: KindUser},
	)
	reg := NewRegistry()
	reg.Register(ch)

	handler := &echoHandler{}
	d := &Dispatcher{
		Channels: reg,
		Store:    NewStore(),
		Handler:  handler,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = d.Run(ctx)
		close(done)
	}()

	select {
	case <-ch.ready:
	case <-ctx.Done():
		t.Fatal("timeout waiting for reply")
	}

	cancel()
	<-done

	sent := ch.sends()
	if len(sent) != 1 {
		t.Fatalf("expected 1 outbound, got %d", len(sent))
	}
	if sent[0].Content != "echo: hello\n" {
		t.Fatalf("unexpected outbound content: %q", sent[0].Content)
	}
	if sent[0].Channel != "cli" || sent[0].Thread != "default" {
		t.Fatalf("unexpected outbound routing: %+v", sent[0])
	}
}

func TestDispatcherIsolatesConversationsByKey(t *testing.T) {
	ch := newFakeChannel("cli",
		Inbound{Channel: "cli", Thread: "alice", Content: "one", Kind: KindUser},
		Inbound{Channel: "cli", Thread: "bob", Content: "two", Kind: KindUser},
		Inbound{Channel: "cli", Thread: "alice", Content: "three", Kind: KindUser},
	)
	reg := NewRegistry()
	reg.Register(ch)

	store := NewStore()
	handler := &echoHandler{}
	d := &Dispatcher{Channels: reg, Store: store, Handler: handler}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = d.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && handler.turnCount() < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if handler.turnCount() != 3 {
		t.Fatalf("expected 3 handled turns, got %d", handler.turnCount())
	}

	alice := store.Get(Key{Channel: "cli", Thread: "alice"})
	bob := store.Get(Key{Channel: "cli", Thread: "bob"})
	if len(alice.Messages) != 0 || len(bob.Messages) != 0 {
		// echoHandler does not touch conv.Messages, so both should be
		// untouched. This verifies conversations are distinct instances.
	}
	if alice == bob {
		t.Fatal("expected distinct Conversation pointers for different threads")
	}
}

func TestDispatcherRunsCommandExpansion(t *testing.T) {
	ch := newFakeChannel("cli",
		Inbound{Channel: "cli", Thread: "default", Content: "/memory:distill", Kind: KindUser},
	)
	reg := NewRegistry()
	reg.Register(ch)

	var received string
	var mu sync.Mutex
	d := &Dispatcher{
		Channels: reg,
		Store:    NewStore(),
		Commands: &CommandExpander{},
		Handler: handlerFunc(func(ctx context.Context, conv *Conversation, content string, reply ReplyFunc) error {
			mu.Lock()
			received = content
			mu.Unlock()
			return reply("ok\n")
		}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = d.Run(ctx)
		close(done)
	}()

	select {
	case <-ch.ready:
	case <-ctx.Done():
		t.Fatal("timeout waiting for reply")
	}

	cancel()
	<-done

	mu.Lock()
	got := received
	mu.Unlock()
	if got != DistillInstruction {
		t.Fatalf("expected command expansion to replace /memory:distill with distill instruction, got %q", got)
	}
}

func TestDispatcherOnUserInputFiresOnlyForKindUser(t *testing.T) {
	ch := newFakeChannel("cli",
		Inbound{Channel: "cli", Thread: "default", Content: "hi", Kind: KindUser},
		Inbound{Channel: "cli", Thread: "default", Content: "<event/>", Kind: KindEvent},
	)
	reg := NewRegistry()
	reg.Register(ch)

	var inputs int
	var mu sync.Mutex
	handler := &echoHandler{}

	d := &Dispatcher{
		Channels: reg,
		Store:    NewStore(),
		Handler:  handler,
		OnUserInput: func() {
			mu.Lock()
			inputs++
			mu.Unlock()
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = d.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && handler.turnCount() < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	mu.Lock()
	got := inputs
	mu.Unlock()
	if got != 1 {
		t.Fatalf("expected OnUserInput to fire exactly once (KindUser only), got %d", got)
	}
}

// handlerFunc adapts a bare function into a TurnHandler.
type handlerFunc func(ctx context.Context, conv *Conversation, content string, reply ReplyFunc) error

func (f handlerFunc) HandleTurn(ctx context.Context, conv *Conversation, content string, reply ReplyFunc) error {
	return f(ctx, conv, content, reply)
}

func TestDispatcherResetCommandClearsAndDispatchesTail(t *testing.T) {
	ch := newFakeChannel("cli",
		Inbound{Channel: "cli", Thread: "t", Content: "/new hello again", Kind: KindUser},
	)
	reg := NewRegistry()
	reg.Register(ch)

	store := NewStore()
	preExisting := store.Get(Key{Channel: "cli", Thread: "t"})
	preExisting.Messages = append(preExisting.Messages,
		types.Message{Role: "user", Content: "old"},
		types.Message{Role: "assistant", Content: "older"},
	)

	var received string
	var mu sync.Mutex
	d := &Dispatcher{
		Channels: reg,
		Store:    store,
		Handler: handlerFunc(func(ctx context.Context, conv *Conversation, content string, reply ReplyFunc) error {
			mu.Lock()
			received = content
			mu.Unlock()
			return reply("ok\n")
		}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = d.Run(ctx)
		close(done)
	}()

	select {
	case <-ch.ready:
	case <-ctx.Done():
		t.Fatal("timeout waiting for reply")
	}
	cancel()
	<-done

	mu.Lock()
	got := received
	mu.Unlock()
	if got != "hello again" {
		t.Fatalf("expected tail dispatched as content, got %q", got)
	}
	if len(preExisting.Messages) != 0 {
		t.Fatalf("expected conversation cleared, got %d messages", len(preExisting.Messages))
	}
}

func TestDispatcherBareResetCommandAcknowledges(t *testing.T) {
	ch := newFakeChannel("cli",
		Inbound{Channel: "cli", Thread: "t", Content: "/reset", Kind: KindUser},
	)
	reg := NewRegistry()
	reg.Register(ch)

	handler := &echoHandler{}
	d := &Dispatcher{Channels: reg, Store: NewStore(), Handler: handler}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = d.Run(ctx)
		close(done)
	}()

	select {
	case <-ch.ready:
	case <-ctx.Done():
		t.Fatal("timeout")
	}
	cancel()
	<-done

	if handler.turnCount() != 0 {
		t.Fatalf("expected handler not invoked for bare /reset, got %d turns", handler.turnCount())
	}
	sent := ch.sends()
	if len(sent) != 1 || sent[0].Content != "New conversation started.\n" {
		t.Fatalf("unexpected reply: %+v", sent)
	}
}

func TestDispatcherHandlerErrorEmittedAsReply(t *testing.T) {
	ch := newFakeChannel("cli",
		Inbound{Channel: "cli", Thread: "default", Content: "hello", Kind: KindUser},
	)
	reg := NewRegistry()
	reg.Register(ch)

	d := &Dispatcher{
		Channels: reg,
		Store:    NewStore(),
		Handler: handlerFunc(func(ctx context.Context, conv *Conversation, content string, reply ReplyFunc) error {
			return errors.New("boom")
		}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = d.Run(ctx)
		close(done)
	}()

	select {
	case <-ch.ready:
	case <-ctx.Done():
		t.Fatal("timeout waiting for error reply")
	}
	cancel()
	<-done

	sent := ch.sends()
	if len(sent) != 1 {
		t.Fatalf("expected 1 outbound, got %d", len(sent))
	}
	if sent[0].Content != "error: boom\n" {
		t.Fatalf("unexpected error content: %q", sent[0].Content)
	}
}

func TestDispatcherPassesAttachmentsAndReplyTo(t *testing.T) {
	att := Attachment{Path: "/tmp/x.png", MimeType: "image/png", Name: "x.png"}
	ch := newFakeChannel("discord",
		Inbound{Channel: "discord", Thread: "c1", Content: "look", MessageID: "m9", Attachments: []Attachment{att}, Kind: KindUser},
	)
	reg := NewRegistry()
	reg.Register(ch)

	var gotAtts []Attachment
	conv := (*Conversation)(nil)
	d := &Dispatcher{
		Channels: reg,
		Store:    NewStore(),
		Handler: handlerFunc(func(ctx context.Context, c *Conversation, content string, reply ReplyFunc) error {
			gotAtts, conv = c.Attachments, c
			return reply("ok")
		}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	select {
	case <-ch.ready:
	case <-ctx.Done():
		t.Fatal("timeout")
	}
	cancel()
	<-done

	if len(gotAtts) != 1 || gotAtts[0] != att {
		t.Errorf("attachments = %+v", gotAtts)
	}
	if conv.Attachments != nil {
		t.Error("attachments should be cleared after the turn")
	}
	if sent := ch.sends(); len(sent) != 1 || sent[0].ReplyTo != "m9" {
		t.Errorf("sent = %+v", sent)
	}
}
