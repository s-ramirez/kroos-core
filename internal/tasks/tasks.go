// Package tasks runs background agent tasks: isolated runs (fresh
// context, own tools/model) whose reply is delivered to a channel, or
// suppressed when the agent replies NothingToReport. Scheduled jobs in
// mode=task, the task tool's run_now, and internal triggers such as the
// skill review all go through one Executor, which keeps a JSONL run
// history and a per-task state file the agent can use between runs.
package tasks

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/s-ramirez/kroos-core/internal/schedule"
	"github.com/s-ramirez/kroos-core/internal/types"
)

// NothingToReport is the reply token that suppresses delivery.
const NothingToReport = "NOTHING_TO_REPORT"

const (
	DefaultTimeout   = 10 * time.Minute
	historyFile      = "runs.jsonl"
	maxSummaryChars  = 2000
	dedupWindow      = 24 * time.Hour
	defaultAgentKind = "background"
)

// Task is one background run request.
type Task struct {
	// ID groups runs of the same task (a schedule job ID, or a fixed name
	// for internal tasks). Only one run per ID is in flight at a time.
	ID     string
	Name   string
	Prompt string
	// Agent selects the factory profile: "" = background, or an internal
	// kind such as "skill-review".
	Agent   string
	Tools   []string
	Model   string
	Deliver schedule.Delivery
	Timeout time.Duration

	// Context, if set, is replayed before Prompt (e.g. a conversation
	// snapshot). Schemas, if set, override the agent's tool schemas so the
	// request can share the snapshot's prompt-cache prefix.
	Context []types.Message
	Schemas []types.ChatRequestTool
}

// Agent runs a conversation to completion. *cli.Runner satisfies it.
type Agent interface {
	RunConversation(ctx context.Context, messages []types.Message, toolSchemas []types.ChatRequestTool) (string, []types.Message, error)
}

// AgentSpec is what a Factory builds for one run.
type AgentSpec struct {
	Agent   Agent
	System  string
	Schemas []types.ChatRequestTool
}

// Factory builds the isolated agent for a task.
type Factory func(t Task) (AgentSpec, error)

// Deliverer sends text to a delivery target.
type Deliverer func(ctx context.Context, d schedule.Delivery, text string) error

// Run is one history record.
type Run struct {
	RunID      string    `json:"run_id"`
	TaskID     string    `json:"task_id"`
	Name       string    `json:"name"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Status     string    `json:"status"` // ok | error | skipped
	Delivered  bool      `json:"delivered"`
	Summary    string    `json:"summary,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// Executor runs tasks in the background.
type Executor struct {
	Dir     string // .kroos/tasks: history + per-task state dirs
	Factory Factory
	Deliver Deliverer
	Log     func(format string, args ...any)

	ctx     context.Context
	mu      sync.Mutex
	running map[string]bool
	last    map[string]delivered
	wg      sync.WaitGroup
	seq     int
}

type delivered struct {
	text string
	at   time.Time
}

// NewExecutor returns an executor whose runs are cancelled with ctx.
func NewExecutor(ctx context.Context, dir string, factory Factory, deliver Deliverer) *Executor {
	return &Executor{Dir: dir, Factory: factory, Deliver: deliver, ctx: ctx,
		running: map[string]bool{}, last: map[string]delivered{}}
}

// Submit starts t in the background. It returns false when a run with the
// same ID is already in flight (the new request is dropped).
func (e *Executor) Submit(t Task) (runID string, ok bool) {
	if t.ID == "" {
		t.ID = t.Name
	}
	e.mu.Lock()
	if e.running[t.ID] {
		e.mu.Unlock()
		return "", false
	}
	e.running[t.ID] = true
	e.seq++
	runID = fmt.Sprintf("%s-%d", time.Now().UTC().Format("20060102T150405"), e.seq)
	e.mu.Unlock()

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer func() {
			e.mu.Lock()
			delete(e.running, t.ID)
			e.mu.Unlock()
		}()
		e.run(runID, t)
	}()
	return runID, true
}

// Wait blocks until all in-flight runs finish (tests, shutdown).
func (e *Executor) Wait() { e.wg.Wait() }

// FromEvent converts a fired mode=task schedule event into a Task.
func FromEvent(ev schedule.Event) Task {
	return Task{ID: ev.JobID, Name: ev.Name, Prompt: ev.Text, Tools: ev.Tools, Model: ev.Model, Deliver: ev.Deliver}
}

func (e *Executor) run(runID string, t Task) {
	rec := Run{RunID: runID, TaskID: t.ID, Name: t.Name, StartedAt: time.Now().UTC()}
	defer func() {
		rec.FinishedAt = time.Now().UTC()
		if err := e.appendHistory(rec); err != nil {
			e.logf("history append failed: %v", err)
		}
	}()

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	parent := e.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	if t.Agent == "" {
		t.Agent = defaultAgentKind
	}
	spec, err := e.Factory(t)
	if err != nil {
		rec.Status, rec.Error = "error", err.Error()
		e.logf("task %q: %v", t.Name, err)
		return
	}
	schemas := spec.Schemas
	if t.Schemas != nil {
		schemas = t.Schemas
	}

	var messages []types.Message
	if len(t.Context) > 0 {
		messages = append(messages, t.Context...)
		messages = append(messages, types.Message{Role: "user", Content: t.Prompt})
	} else {
		messages = []types.Message{
			{Role: "system", Content: spec.System},
			{Role: "user", Content: e.framePrompt(t)},
		}
	}

	text, _, err := spec.Agent.RunConversation(ctx, messages, schemas)
	rec.Summary = truncate(strings.TrimSpace(text), maxSummaryChars)
	if err != nil {
		rec.Status, rec.Error = "error", err.Error()
		e.logf("task %q failed: %v", t.Name, err)
		return
	}
	rec.Status = "ok"

	if IsNothingToReport(text) || t.Deliver.Target == "" || t.Deliver.Target == "none" || e.Deliver == nil {
		return
	}
	if e.isDuplicate(t.ID, text) {
		rec.Status = "skipped"
		return
	}
	if err := e.Deliver(ctx, t.Deliver, text); err != nil {
		rec.Error = "delivery: " + err.Error()
		e.logf("task %q delivery failed: %v", t.Name, err)
		return
	}
	rec.Delivered = true
	e.mu.Lock()
	e.last[t.ID] = delivered{text: text, at: time.Now()}
	e.mu.Unlock()
}

// framePrompt wraps the task prompt with the unattended-run contract,
// the state file path, and the previous run's result.
func (e *Executor) framePrompt(t Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<background_task name=%q id=%q fired_at=%q>\n", t.Name, t.ID, time.Now().Format(time.RFC3339))
	b.WriteString("You are running unattended; no user is watching this conversation.\n")
	switch t.Deliver.Target {
	case "", "none":
		b.WriteString("Your final reply is recorded in the task history only.\n")
	default:
		fmt.Fprintf(&b, "Your final reply is sent to the user via %s, so write it as a message to them.\n", t.Deliver.Target)
	}
	fmt.Fprintf(&b, "If there is nothing worth telling the user, reply with exactly %s and nothing else.\n", NothingToReport)
	if dir := e.stateDir(t.ID); dir != "" {
		fmt.Fprintf(&b, "Persist anything you need next run (last seen values, thresholds already alerted) in %s (create it if missing).\n", filepath.Join(dir, "state.md"))
	}
	if prev, ok := e.lastRun(t.ID); ok {
		fmt.Fprintf(&b, "Previous run (%s, %s): %s\n", prev.FinishedAt.Format(time.RFC3339), prev.Status, firstNonEmpty(prev.Summary, prev.Error))
	}
	b.WriteString("\nTask:\n")
	b.WriteString(t.Prompt)
	b.WriteString("\n</background_task>")
	return b.String()
}

func (e *Executor) isDuplicate(id, text string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	d, ok := e.last[id]
	return ok && d.text == text && time.Since(d.at) < dedupWindow
}

func (e *Executor) stateDir(id string) string {
	if e.Dir == "" || id == "" {
		return ""
	}
	return filepath.Join(e.Dir, safeName(id))
}

func (e *Executor) appendHistory(r Run) error {
	if e.Dir == "" {
		return nil
	}
	if err := os.MkdirAll(e.Dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(e.Dir, historyFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

// History returns up to n most recent runs, newest first. An empty
// taskID matches every task.
func (e *Executor) History(taskID string, n int) []Run {
	if e.Dir == "" {
		return nil
	}
	f, err := os.Open(filepath.Join(e.Dir, historyFile))
	if err != nil {
		return nil
	}
	defer f.Close()
	var all []Run
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var r Run
		if json.Unmarshal(sc.Bytes(), &r) == nil && (taskID == "" || r.TaskID == taskID) {
			all = append(all, r)
		}
	}
	var out []Run
	for i := len(all) - 1; i >= 0 && (n <= 0 || len(out) < n); i-- {
		out = append(out, all[i])
	}
	return out
}

func (e *Executor) lastRun(id string) (Run, bool) {
	runs := e.History(id, 1)
	if len(runs) == 0 {
		return Run{}, false
	}
	return runs[0], true
}

func (e *Executor) logf(format string, args ...any) {
	if e.Log != nil {
		e.Log(format, args...)
	}
}

// nothingRE also accepts the legacy heartbeat token.
var nothingRE = regexp.MustCompile(`(?i)NOTHING_TO_REPORT|HEARTBEAT_OK`)

// IsNothingToReport reports whether text is empty or only the token
// (or the legacy HEARTBEAT_OK).
func IsNothingToReport(text string) bool {
	s := nothingRE.ReplaceAllString(strings.TrimSpace(text), "")
	return strings.TrimFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) == ""
}

var unsafeRE = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func safeName(s string) string { return unsafeRE.ReplaceAllString(s, "_") }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// FromJob converts a persisted mode=task job into a Task (run_now).
func FromJob(j schedule.Job) Task {
	return Task{ID: j.ID, Name: j.Name, Prompt: j.Text, Tools: j.Tools, Model: j.Model, Deliver: j.Deliver}
}
