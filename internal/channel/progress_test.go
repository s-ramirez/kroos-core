package channel

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func TestFormatToolProgress(t *testing.T) {
	cases := []struct {
		name   string
		params any
		group  string
		line   string
	}{
		{"bash", map[string]any{"command": "hermes --profile palworld-friends gateway status"}, "💻 terminal", "```\nhermes --profile palworld-friends gatewa...\n```"},
		{"read", map[string]any{"path": "/x/agent.log", "offset": 1560, "limit": 60}, "", "📖 Reading agent.log L1560-1619"},
		{"read", map[string]any{"path": "/x/notes.md"}, "", "📖 Reading notes.md"},
		{"edit", map[string]any{"path": "/x/main.go"}, "", "✏️ Editing main.go"},
		{"web_fetch", map[string]any{"url": "https://example.com/a/b?q=1"}, "", "🌐 Fetching example.com/a/b"},
		{"task", map[string]any{"action": "list"}, "", `⚙️ task: "list"`},
	}
	for _, c := range cases {
		g, l := formatToolProgress(c.name, raw(c.params))
		if g != c.group || l != c.line {
			t.Errorf("%s: got (%q, %q) want (%q, %q)", c.name, g, l, c.group, c.line)
		}
	}
}

func TestRenderProgressGroupsAndCounts(t *testing.T) {
	entries := []progressEntry{
		{line: `⚙️ process: "wait"`, count: 1},
		{group: "💻 terminal", line: "```\na\n```", count: 1},
		{group: "💻 terminal", line: "```\nb\n```", count: 2},
		{line: "📖 Reading x L1-2", count: 1},
	}
	want := "⚙️ process: \"wait\"\n💻 terminal\n```\na\n```\n```\nb\n``` (×2)\n📖 Reading x L1-2"
	if got := renderProgress(entries); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	var many []progressEntry
	for i := 0; i < 200; i++ {
		many = append(many, progressEntry{line: strings.Repeat("x", 30), count: 1})
	}
	if out := renderProgress(many); len(out) > progressMaxChars || !strings.HasPrefix(out, "…") {
		t.Fatalf("truncation: len=%d", len(out))
	}
}

type fakeEditor struct {
	mu    sync.Mutex
	posts []string
	edits []string
}

func (f *fakeEditor) SendEditable(_ context.Context, _, text, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, text)
	return "m1", nil
}

func (f *fakeEditor) Edit(_ context.Context, _, id, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, text)
	return nil
}

func TestToolProgressPostsOnceThenEdits(t *testing.T) {
	ed := &fakeEditor{}
	p := newToolProgress(context.Background(), ed, "t", "", nil)
	p.Add("bash", raw(map[string]string{"command": "ls"}))
	p.Add("bash", raw(map[string]string{"command": "ls"}))
	p.Add("read", raw(map[string]string{"path": "a.go"}))
	p.Flush()
	ed.mu.Lock()
	defer ed.mu.Unlock()
	if len(ed.posts) != 1 {
		t.Fatalf("posts = %v", ed.posts)
	}
	final := ed.edits[len(ed.edits)-1]
	if !strings.Contains(final, "``` (×2)") || !strings.Contains(final, "📖 Reading a.go") {
		t.Fatalf("final = %q", final)
	}
	if len(ed.edits) > 2 {
		t.Fatalf("not throttled: %d edits", len(ed.edits))
	}
}

// editorChannel is a fakeChannel that also supports edits.
type editorChannel struct {
	*fakeChannel
	fakeEditor
}

// toolHandler runs two tools, then replies.
type toolHandler struct{}

func (toolHandler) HandleTurn(ctx context.Context, _ *Conversation, _ string, reply ReplyFunc) error {
	ReportTool(ctx, "bash", raw(map[string]string{"command": "uptime"}))
	ReportTool(ctx, "read", raw(map[string]string{"path": "/var/log/x.log"}))
	return reply("all good\n")
}

func TestDispatcherSendsProgressBeforeAnswer(t *testing.T) {
	ch := &editorChannel{fakeChannel: newFakeChannel("discord", Inbound{Channel: "discord", Thread: "t", Content: "hi", Kind: KindUser})}
	reg := NewRegistry()
	reg.Register(ch)
	d := &Dispatcher{Channels: reg, Store: NewStore(), Handler: toolHandler{}, ToolProgress: true}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go d.Run(ctx)
	select {
	case <-ch.ready:
	case <-ctx.Done():
		t.Fatal("no answer")
	}
	ch.fakeEditor.mu.Lock()
	defer ch.fakeEditor.mu.Unlock()
	if len(ch.fakeEditor.posts) != 1 {
		t.Fatalf("progress not posted before the answer: %v", ch.fakeEditor.posts)
	}
	all := append(ch.fakeEditor.posts, ch.fakeEditor.edits...)
	last := all[len(all)-1]
	if !strings.Contains(last, "uptime") || !strings.Contains(last, "x.log") {
		t.Fatalf("progress = %q", last)
	}
	if s := ch.sends(); len(s) != 1 || s[0].Content != "all good\n" {
		t.Fatalf("answer = %+v", s)
	}
}

type typingChannel struct {
	*fakeChannel
	mu     sync.Mutex
	events []string
}

func (c *typingChannel) StartTyping(context.Context, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, "start")
	return nil
}

func (c *typingChannel) StopTyping(context.Context, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, "stop")
	return nil
}

// slowHandler works for a while (several refresh periods) before replying.
type slowHandler struct{ d time.Duration }

func (h slowHandler) HandleTurn(ctx context.Context, _ *Conversation, _ string, reply ReplyFunc) error {
	time.Sleep(h.d)
	return reply("done\n")
}

func TestDispatcherKeepsTypingForTheWholeTurn(t *testing.T) {
	old := typingRefresh
	typingRefresh = 20 * time.Millisecond
	t.Cleanup(func() { typingRefresh = old })

	ch := &typingChannel{fakeChannel: newFakeChannel("imessage", Inbound{Channel: "imessage", Thread: "t", Content: "hi", Kind: KindUser})}
	reg := NewRegistry()
	reg.Register(ch)
	d := &Dispatcher{Channels: reg, Store: NewStore(), Handler: slowHandler{100 * time.Millisecond}, ToolProgress: true}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go d.Run(ctx)
	<-ch.ready
	time.Sleep(50 * time.Millisecond) // let the deferred stop run

	ch.mu.Lock()
	defer ch.mu.Unlock()
	starts := strings.Count(strings.Join(ch.events, ","), "start")
	if starts < 3 || ch.events[len(ch.events)-1] != "stop" {
		t.Fatalf("events = %v (want refreshed starts, then stop)", ch.events)
	}
}
