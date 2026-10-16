package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Editor is implemented by channels that can post a message and edit it
// later. The dispatcher uses it for a live tool-progress message per turn.
type Editor interface {
	SendEditable(ctx context.Context, thread, text, replyTo string) (id string, err error)
	Edit(ctx context.Context, thread, id, text string) error
}

type toolReporterKey struct{}

// ToolReporter receives each tool call the agent makes during a turn.
type ToolReporter func(name string, params json.RawMessage)

// WithToolReporter returns ctx carrying fn for ReportTool.
func WithToolReporter(ctx context.Context, fn ToolReporter) context.Context {
	return context.WithValue(ctx, toolReporterKey{}, fn)
}

// ReportTool tells the turn's reporter (if any) that a tool is running.
func ReportTool(ctx context.Context, name string, params json.RawMessage) {
	if fn, ok := ctx.Value(toolReporterKey{}).(ToolReporter); ok && fn != nil {
		fn(name, params)
	}
}

const (
	progressMinInterval = time.Second // Discord rate limits edits; batch within this window
	progressMaxChars    = 1900
	progressCmdChars    = 40
)

type progressEntry struct {
	group string // consecutive entries with the same group share one header
	line  string
	count int
}

// toolProgress renders a turn's tool calls into one message that is posted
// on the first call and edited as more arrive (throttled).
type toolProgress struct {
	ctx     context.Context
	ed      Editor
	thread  string
	replyTo string
	logf    func(string, ...any)

	mu        sync.Mutex
	entries   []progressEntry
	rendered  string // last text sent
	lastFlush time.Time
	timer     *time.Timer

	sendMu sync.Mutex // serializes network calls
	msgID  string
}

func newToolProgress(ctx context.Context, ed Editor, thread, replyTo string, logf func(string, ...any)) *toolProgress {
	return &toolProgress{ctx: ctx, ed: ed, thread: thread, replyTo: replyTo, logf: logf}
}

// Add records a tool call and schedules an update.
func (p *toolProgress) Add(name string, params json.RawMessage) {
	group, line := formatToolProgress(name, params)
	p.mu.Lock()
	if n := len(p.entries); n > 0 && p.entries[n-1].group == group && p.entries[n-1].line == line {
		p.entries[n-1].count++
	} else {
		p.entries = append(p.entries, progressEntry{group: group, line: line, count: 1})
	}
	wait := progressMinInterval - time.Since(p.lastFlush)
	if wait <= 0 {
		p.mu.Unlock()
		p.Flush()
		return
	}
	if p.timer == nil {
		p.timer = time.AfterFunc(wait, p.Flush)
	}
	p.mu.Unlock()
}

// Flush sends the current state now (first call posts, later calls edit).
func (p *toolProgress) Flush() {
	p.mu.Lock()
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	text := renderProgress(p.entries)
	if text == "" || text == p.rendered {
		p.mu.Unlock()
		return
	}
	p.rendered = text
	p.lastFlush = time.Now()
	p.mu.Unlock()

	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	var err error
	if p.msgID == "" {
		p.msgID, err = p.ed.SendEditable(p.ctx, p.thread, text, p.replyTo)
	} else {
		err = p.ed.Edit(p.ctx, p.thread, p.msgID, text)
	}
	if err != nil && p.logf != nil {
		p.logf("tool progress: %v", err)
	}
}

func renderProgress(entries []progressEntry) string {
	var blocks []string
	for i, e := range entries {
		var b strings.Builder
		if e.group != "" && (i == 0 || entries[i-1].group != e.group) {
			b.WriteString(e.group + "\n")
		}
		b.WriteString(e.line)
		if e.count > 1 {
			fmt.Fprintf(&b, " (×%d)", e.count)
		}
		blocks = append(blocks, b.String())
	}
	// Keep the newest lines within the message limit.
	for len(blocks) > 1 && len(strings.Join(blocks, "\n")) > progressMaxChars {
		blocks = blocks[1:]
		blocks[0] = "…\n" + strings.TrimPrefix(blocks[0], "…\n")
	}
	return strings.Join(blocks, "\n")
}

// formatToolProgress returns a Hermes-style line for a tool call and the
// group header it shares with neighbours (e.g. "💻 terminal").
func formatToolProgress(name string, params json.RawMessage) (group, line string) {
	var p map[string]any
	_ = json.Unmarshal(params, &p)
	str := func(k string) string { s, _ := p[k].(string); return strings.TrimSpace(s) }
	num := func(k string) int { f, _ := p[k].(float64); return int(f) }
	base := func(path string) string {
		if path == "" {
			return "."
		}
		return filepath.Base(path)
	}

	switch name {
	case "bash":
		cmd := strings.ReplaceAll(firstLine(str("command")), "```", "'''")
		return "💻 terminal", "```\n" + truncateRunes(cmd, progressCmdChars) + "\n```"
	case "read":
		line = "📖 Reading " + base(str("path"))
		if off, lim := num("offset"), num("limit"); lim > 0 {
			line += fmt.Sprintf(" L%d-%d", max(off, 1), max(off, 1)+lim-1)
		} else if off > 0 {
			line += fmt.Sprintf(" L%d-", off)
		}
		return "", line
	case "write":
		return "", "✍️ Writing " + base(str("path"))
	case "edit":
		return "", "✏️ Editing " + base(str("path"))
	case "grep":
		return "", fmt.Sprintf("🔎 grep %q", truncateRunes(str("pattern"), progressCmdChars))
	case "find":
		return "", fmt.Sprintf("🔎 find %q", truncateRunes(str("pattern"), progressCmdChars))
	case "ls":
		return "", "📂 ls " + base(str("path"))
	case "web_fetch":
		target := str("url")
		if u, err := url.Parse(target); err == nil && u.Host != "" {
			target = u.Host + u.Path
		}
		return "", "🌐 Fetching " + truncateRunes(target, progressCmdChars)
	case "web_search":
		return "", fmt.Sprintf("🔍 Searching %q", truncateRunes(str("query"), progressCmdChars))
	case "memory":
		return "", strings.TrimSpace("🧠 memory " + str("action") + " " + str("path"))
	case "send_file":
		return "", "📎 Sending " + base(str("path"))
	}
	// Generic: first string argument as a hint.
	hint := ""
	for _, k := range []string{"action", "query", "prompt", "task", "name", "id"} {
		if v := str(k); v != "" {
			hint = v
			break
		}
	}
	if hint == "" {
		return "", "⚙️ " + name
	}
	return "", fmt.Sprintf("⚙️ %s: %q", name, truncateRunes(firstLine(hint), progressCmdChars))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
