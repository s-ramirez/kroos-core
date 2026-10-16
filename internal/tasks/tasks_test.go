package tasks

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/schedule"
	"github.com/s-ramirez/kroos-core/internal/types"
)

type fakeAgent struct {
	reply string
	err   error
	block chan struct{}
	mu    sync.Mutex
	seen  [][]types.Message
}

func (a *fakeAgent) RunConversation(_ context.Context, msgs []types.Message, _ []types.ChatRequestTool) (string, []types.Message, error) {
	if a.block != nil {
		<-a.block
	}
	a.mu.Lock()
	a.seen = append(a.seen, msgs)
	a.mu.Unlock()
	return a.reply, msgs, a.err
}

type sink struct {
	mu   sync.Mutex
	sent []string
}

func (s *sink) deliver(_ context.Context, d schedule.Delivery, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, d.Target+":"+d.To+":"+text)
	return nil
}

func newTestExecutor(t *testing.T, agent *fakeAgent) (*Executor, *sink) {
	t.Helper()
	s := &sink{}
	e := NewExecutor(context.Background(), t.TempDir(), func(Task) (AgentSpec, error) {
		return AgentSpec{Agent: agent, System: "sys"}, nil
	}, s.deliver)
	return e, s
}

func TestExecutorDeliversAndRecordsHistory(t *testing.T) {
	agent := &fakeAgent{reply: "Flight is $350!"}
	e, s := newTestExecutor(t, agent)
	task := Task{ID: "job1", Name: "flight", Prompt: "check price", Deliver: schedule.Delivery{Target: "telegram", To: "42"}}

	if _, ok := e.Submit(task); !ok {
		t.Fatal("submit rejected")
	}
	e.Wait()

	if len(s.sent) != 1 || s.sent[0] != "telegram:42:Flight is $350!" {
		t.Fatalf("unexpected deliveries: %v", s.sent)
	}
	runs := e.History("job1", 0)
	if len(runs) != 1 || runs[0].Status != "ok" || !runs[0].Delivered {
		t.Fatalf("unexpected history: %+v", runs)
	}
	user := agent.seen[0][1].Content.(string)
	if !strings.Contains(user, "check price") || !strings.Contains(user, NothingToReport) || !strings.Contains(user, "state.md") {
		t.Fatalf("prompt not framed: %s", user)
	}

	// Second run sees the previous result; identical reply is deduped.
	e.Submit(task)
	e.Wait()
	if !strings.Contains(agent.seen[1][1].Content.(string), "Previous run") {
		t.Fatal("expected previous run in prompt")
	}
	if len(s.sent) != 1 {
		t.Fatalf("expected duplicate suppressed, got %v", s.sent)
	}
	if runs := e.History("job1", 1); runs[0].Status != "skipped" {
		t.Fatalf("expected skipped, got %+v", runs[0])
	}
}

func TestExecutorSuppressesNothingToReport(t *testing.T) {
	e, s := newTestExecutor(t, &fakeAgent{reply: " nothing_to_report. "})
	e.Submit(Task{ID: "j", Prompt: "p", Deliver: schedule.Delivery{Target: "cli"}})
	e.Wait()
	if len(s.sent) != 0 {
		t.Fatalf("expected no delivery, got %v", s.sent)
	}
}

func TestExecutorRecordsErrors(t *testing.T) {
	e, s := newTestExecutor(t, &fakeAgent{err: fmt.Errorf("boom")})
	e.Submit(Task{ID: "j", Prompt: "p", Deliver: schedule.Delivery{Target: "cli"}})
	e.Wait()
	runs := e.History("j", 0)
	if len(s.sent) != 0 || len(runs) != 1 || runs[0].Status != "error" || runs[0].Error != "boom" {
		t.Fatalf("unexpected: sent=%v runs=%+v", s.sent, runs)
	}
}

func TestExecutorSingleFlightPerID(t *testing.T) {
	agent := &fakeAgent{reply: "x", block: make(chan struct{})}
	e, _ := newTestExecutor(t, agent)
	if _, ok := e.Submit(Task{ID: "j", Prompt: "p"}); !ok {
		t.Fatal("first submit rejected")
	}
	if _, ok := e.Submit(Task{ID: "j", Prompt: "p"}); ok {
		t.Fatal("expected second submit rejected while running")
	}
	if _, ok := e.Submit(Task{ID: "other", Prompt: "p"}); !ok {
		t.Fatal("different id should run")
	}
	close(agent.block)
	e.Wait()
}

func TestExecutorReplaysContext(t *testing.T) {
	agent := &fakeAgent{reply: "done"}
	e, _ := newTestExecutor(t, agent)
	ctxMsgs := []types.Message{{Role: "system", Content: "main"}, {Role: "user", Content: "hi"}}
	e.Submit(Task{ID: "r", Prompt: "review", Context: ctxMsgs})
	e.Wait()
	got := agent.seen[0]
	if len(got) != 3 || got[0].Content != "main" || got[2].Content != "review" {
		t.Fatalf("unexpected messages: %+v", got)
	}
}
