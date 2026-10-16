package channel

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// flakyChannel fails Listen the first `fails` times, then delivers one message.
type flakyChannel struct {
	*fakeChannel
	fails    int32
	attempts atomic.Int32
}

func (f *flakyChannel) Listen(ctx context.Context, out chan<- Inbound) error {
	if f.attempts.Add(1) <= f.fails {
		return errors.New("login failed")
	}
	return f.fakeChannel.Listen(ctx, out)
}

// terminalChannel ends immediately, like the CLI on Ctrl-D.
type terminalChannel struct{ *fakeChannel }

func (terminalChannel) Terminal() bool { return true }
func (terminalChannel) Listen(ctx context.Context, out chan<- Inbound) error {
	return nil
}

func fastBackoff(t *testing.T) {
	oldMin, oldMax := listenBackoffMin, listenBackoffMax
	listenBackoffMin, listenBackoffMax = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { listenBackoffMin, listenBackoffMax = oldMin, oldMax })
}

func TestFailingChannelDoesNotStopOthersAndIsRetried(t *testing.T) {
	fastBackoff(t)
	healthy := newFakeChannel("healthy", Inbound{Channel: "healthy", Thread: "t", Content: "hi", Kind: KindUser})
	flaky := &flakyChannel{fakeChannel: newFakeChannel("flaky", Inbound{Channel: "flaky", Thread: "t", Content: "back", Kind: KindUser}), fails: 3}
	reg := NewRegistry()
	reg.Register(healthy)
	reg.Register(flaky)
	d := &Dispatcher{Channels: reg, Store: NewStore(), Handler: &echoHandler{}}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	for _, ch := range []*fakeChannel{healthy, flaky.fakeChannel} {
		select {
		case <-ch.ready:
		case <-ctx.Done():
			t.Fatalf("%s never replied", ch.name)
		}
	}
	if got := flaky.attempts.Load(); got != 4 {
		t.Fatalf("flaky attempts = %d, want 4", got)
	}
	select {
	case err := <-done:
		t.Fatalf("dispatcher exited early: %v", err)
	default:
	}
	cancel()
	<-done
}

func TestTerminalChannelEndsDispatcher(t *testing.T) {
	reg := NewRegistry()
	reg.Register(terminalChannel{newFakeChannel("cli")})
	reg.Register(newFakeChannel("discord"))
	d := &Dispatcher{Channels: reg, Store: NewStore(), Handler: &echoHandler{}}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := d.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("dispatcher only stopped on timeout")
	}
}
