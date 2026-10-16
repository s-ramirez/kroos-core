package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/s-ramirez/kroos-core/internal/channel"
	"github.com/s-ramirez/kroos-core/internal/health"
	"github.com/s-ramirez/kroos-core/internal/schedule"
	"github.com/s-ramirez/kroos-core/internal/tasks"
	"github.com/s-ramirez/kroos-core/internal/types"
)

type homeChannel struct {
	name, home string
	mu         sync.Mutex
	sent       []channel.Outbound
	got        chan struct{}
}

func newHomeChannel(name, home string) *homeChannel {
	return &homeChannel{name: name, home: home, got: make(chan struct{}, 4)}
}
func (c *homeChannel) Name() string { return c.name }
func (c *homeChannel) Home() string { return c.home }
func (c *homeChannel) Listen(ctx context.Context, _ chan<- channel.Inbound) error {
	<-ctx.Done()
	return ctx.Err()
}
func (c *homeChannel) Send(_ context.Context, m channel.Outbound) error {
	c.mu.Lock()
	c.sent = append(c.sent, m)
	c.mu.Unlock()
	c.got <- struct{}{}
	return nil
}

func TestIncidentNotifierSkipsFailingChannel(t *testing.T) {
	discord := newHomeChannel("discord", "d-home")
	telegram := newHomeChannel("telegram", "t-home")
	reg := channel.NewRegistry()
	reg.Register(discord)
	reg.Register(telegram)
	m := health.NewMonitor("")
	m.OnIncident = incidentNotifier(func() *channel.Registry { return reg }, m, func(string, ...any) {})

	for i := 0; i < health.DefaultThreshold; i++ {
		m.Report("channel:discord", errors.New("4004 authentication failed"))
	}
	select {
	case <-telegram.got:
	case <-time.After(2 * time.Second):
		t.Fatal("no notification")
	}
	msg := telegram.sent[0]
	if msg.Thread != "t-home" || !strings.Contains(msg.Content, "channel:discord") || !strings.Contains(msg.Content, "/doctor") {
		t.Fatalf("notification: %+v", msg)
	}
	if len(discord.sent) != 0 {
		t.Fatal("notified through the failing channel")
	}
}

type staticAgent string

func (s staticAgent) RunConversation(context.Context, []types.Message, []types.ChatRequestTool) (string, []types.Message, error) {
	return string(s), nil, nil
}

func TestDoctorCommandRunsAndReportsToConversation(t *testing.T) {
	release := make(chan struct{})
	var gotNote string
	delivered := make(chan schedule.Delivery, 1)
	exec := tasks.NewExecutor(context.Background(), t.TempDir(),
		func(task tasks.Task) (tasks.AgentSpec, error) {
			gotNote = task.Prompt
			<-release
			return tasks.AgentSpec{Agent: staticAgent("Problem: x\nFixed: y\nYou need to: nothing")}, nil
		},
		func(_ context.Context, d schedule.Delivery, text string) error {
			delivered <- d
			return nil
		})
	m := health.NewMonitor("")
	cmd := doctorCommand(exec, m, nil)
	conv := &channel.Conversation{Key: channel.Key{Channel: "telegram", Thread: "42"}}

	if out := cmd(context.Background(), conv, "discord is down"); !strings.Contains(out, "Investigating") {
		t.Fatalf("first: %q", out)
	}
	if out := cmd(context.Background(), conv, ""); !strings.Contains(out, "already") {
		t.Fatalf("second: %q", out)
	}
	close(release)
	select {
	case d := <-delivered:
		if d.Target != "telegram" || d.To != "42" || gotNote != "discord is down" {
			t.Fatalf("delivery %+v note %q", d, gotNote)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no report delivered")
	}
	exec.Wait()
}
