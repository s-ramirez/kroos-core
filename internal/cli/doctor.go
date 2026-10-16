package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/s-ramirez/kroos-core/internal/health"
	"github.com/s-ramirez/kroos-core/internal/llm"
	"github.com/s-ramirez/kroos-core/internal/skills"
	"github.com/s-ramirez/kroos-core/internal/tasks"
	"github.com/s-ramirez/kroos-core/internal/tools"
)

// DoctorAgent is the task agent kind for self-healing investigations.
const DoctorAgent = "doctor"

// DoctorTimeout bounds one investigation.
const DoctorTimeout = 20 * time.Minute

// DoctorConfig is what the doctor needs to know about this instance.
type DoctorConfig struct {
	Home      string // $KROOS_HOME
	SourceDir string // kroos code checkout ("" = unknown: no code fixes)
	LogFile   string // process log to inspect ("" = none)
	Monitor   *health.Monitor
	Note      string // what the user asked, if anything
	Progress  *DoctorProgress
	Logf      func(format string, args ...any)
}

// DoctorProgress tracks the running investigation for /doctor status.
type DoctorProgress struct {
	mu      sync.Mutex
	running bool
	started time.Time
	steps   int
	last    string
}

func (p *DoctorProgress) start() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.running, p.started, p.steps, p.last = true, time.Now(), 0, ""
	p.mu.Unlock()
}

func (p *DoctorProgress) step(desc string) int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.steps++
	p.last = desc
	return p.steps
}

// Done marks the investigation finished.
func (p *DoctorProgress) Done() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.running = false
	p.mu.Unlock()
}

// Status describes the current run ("" when idle).
func (p *DoctorProgress) Status() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running {
		return ""
	}
	s := fmt.Sprintf("running for %s, %d steps", time.Since(p.started).Round(time.Second), p.steps)
	if p.last != "" {
		s += ", latest: " + p.last
	}
	return s
}

// WorktreeRoot is where the doctor makes code changes (never the live checkout).
func (c DoctorConfig) WorktreeRoot() string { return filepath.Join(c.Home, "workspace", "doctor") }

const doctorIntro = "You are kroos's doctor: you investigate and fix problems in this kroos instance."

// DoctorPrompt is the investigation contract.
const DoctorPrompt = `Investigate why kroos is unhealthy and fix it if you safely can.

Process:
1. kroos_health action=status, then read the log excerpt below (and the log file if needed).
2. Form a hypothesis and verify it (commands are killed after 2 minutes; never run anything that streams or waits forever, e.g. tail -f, log stream, curl without -m): kroos_config action=list, test connectivity with bash (curl -sS -m 10, nc -z, dig), check versions and recent git history in the source checkout (read-only).
3. Fix within your scope:
   - Config: change non-secret settings with kroos_config, then kroos_health action=restart_process.
   - Transient/network: retry with kroos_health action=restart once the cause is gone.
   - Credentials: you cannot set them. Say exactly which key is wrong/missing and how to set it (` + "`kroos init`" + ` or ` + "`kroos config set <key> <value>`" + `).
     Config and credentials are only read at startup, so channel retries will NOT pick up a new value: tell the user to reply /doctor
     afterwards so you can restart kroos (kroos_health action=restart_process), or to restart it themselves.
   - Code bug: fix it in a git worktree, never in the live checkout:
       git -C <source> fetch origin
       git -C <source> worktree add <worktrees>/<slug> -b doctor/<slug> origin/main
     then edit files only inside that worktree, add or update a test, run go build ./... and go test ./... there,
     commit, git push -u origin doctor/<slug>, and open a PR with gh pr create (title "doctor: ...", body with root cause, fix, test evidence).
     Never push to main, never force-push, never merge. If gh or git push isn't available, include the full diff in your report instead.
     Remove the worktree when done: git -C <source> worktree remove <worktrees>/<slug>.
4. Verify: kroos_health action=status (or a test request) shows the problem gone, or explain why it can't be verified yet.

Report (plain text, short, this is sent to the user's chat):
- Problem: what failed and the root cause (with evidence).
- Fixed: what you changed (config keys, restarts, PR link) - or "nothing".
- You need to: exact steps for anything you couldn't do, or "nothing".
Never include secret values.`

// BuildDoctor assembles the doctor run: an isolated runner with diagnostic
// and repair tools behind a guard hook, and a prompt with current health.
func BuildDoctor(client llm.ChatClient, pool []tools.Tool, cfg DoctorConfig) tasks.AgentSpec {
	reg := tools.NewRegistry(pool...)
	guard := newDoctorGuard(cfg)
	guard.progress, guard.logf = cfg.Progress, cfg.Logf
	reg.AddHook(guard)
	cfg.Progress.start()
	r := &Runner{
		Client:    client,
		Registry:  reg,
		Skills:    skills.NewRegistry(nil),
		AgentID:   DoctorAgent,
		AgentName: DoctorAgent,
		Prompt:    doctorIntro,
	}
	return tasks.AgentSpec{
		Agent:   r,
		System:  r.SystemPrompt(),
		Schemas: reg.Schemas(),
		Prompt:  DoctorPrompt + "\n\n" + describeDoctorContext(cfg),
	}
}

func describeDoctorContext(cfg DoctorConfig) string {
	var b strings.Builder
	b.WriteString("<instance>\n")
	fmt.Fprintf(&b, "KROOS_HOME: %s\n", cfg.Home)
	if cfg.SourceDir != "" {
		fmt.Fprintf(&b, "source checkout (read-only; auto-deploys from origin/main): %s\n", cfg.SourceDir)
		fmt.Fprintf(&b, "worktrees dir: %s\n", cfg.WorktreeRoot())
	} else {
		b.WriteString("source checkout: unknown (KROOS_SOURCE_DIR not set) - report code bugs with a proposed diff\n")
	}
	if cfg.LogFile != "" {
		fmt.Fprintf(&b, "log file: %s\n", cfg.LogFile)
	}
	b.WriteString("</instance>\n\n<failing>\n")
	failing := cfg.Monitor.Failing()
	if len(failing) == 0 {
		b.WriteString("none currently recorded\n")
	}
	for _, inc := range failing {
		fmt.Fprintf(&b, "%s: %d consecutive failures since %s; last error: %s\n",
			inc.Component, inc.Count, inc.FirstSeen.Format(time.RFC3339), inc.Error)
	}
	b.WriteString("</failing>\n")
	if tail := tailFile(cfg.LogFile, 80); tail != "" {
		b.WriteString("\n<log_tail>\n" + tail + "\n</log_tail>\n")
	}
	if note := strings.TrimSpace(cfg.Note); note != "" {
		b.WriteString("\nUser's note: " + note + "\n")
	}
	return b.String()
}

// tailFile returns the last n lines of path ("" on any error).
func tailFile(path string, n int) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const maxRead = 64 << 10
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	off := max(st.Size()-maxRead, 0)
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // drop partial first line
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// doctorGuard enforces the doctor's scope (best effort; the prompt is the
// primary contract): code changes only in worktrees, pushes only to
// doctor/* branches, never merge, never write credentials.
type doctorGuard struct {
	source    string
	worktrees string
	creds     []string
	progress  *DoctorProgress
	logf      func(format string, args ...any)
}

func newDoctorGuard(cfg DoctorConfig) *doctorGuard {
	return &doctorGuard{
		source:    cleanDir(cfg.SourceDir),
		worktrees: cleanDir(cfg.WorktreeRoot()),
		creds:     []string{cleanDir(filepath.Join(cfg.Home, "auth.json")), cleanDir(filepath.Join(cfg.Home, ".env"))},
	}
}

var (
	gitPushRE     = regexp.MustCompile(`\bgit\b[^;&|\n]*\bpush\b([^;&|\n]*)`)
	forceRE       = regexp.MustCompile(`(^|\s)(-f|--force\S*|--mirror|--delete|-d)(\s|$)|\s\+\S`)
	protectedRE   = regexp.MustCompile(`(^|[\s:/])(main|master|HEAD)(\s|$)`)
	ghMergeRE     = regexp.MustCompile(`\bgh\b[^;&|\n]*\bpr\b[^;&|\n]*\bmerge\b|\bgh\b[^;&|\n]*\bapi\b[^;&|\n]*/merge\b`)
	gitMutationRE = regexp.MustCompile(`\bgit\b[^;&|\n]*\b(commit|checkout|switch|reset|merge|rebase|clean|stash|pull|am|apply|cherry-pick|restore|rm|mv|add|tag)\b`)
)

func (g *doctorGuard) BeforeToolCall(hctx tools.HookContext, name string, params json.RawMessage) string {
	reason := g.check(name, params)
	desc := describeStep(name, params)
	if reason != "" {
		desc += " (blocked)"
	}
	n := g.progress.step(desc)
	if g.logf != nil {
		g.logf("[doctor] step %d: %s", n, desc)
	}
	return reason
}

// describeStep is a one-line, secret-free summary of a tool call.
func describeStep(name string, params json.RawMessage) string {
	var p map[string]any
	_ = json.Unmarshal(params, &p)
	detail := ""
	for _, k := range []string{"command", "action", "path", "url", "query"} {
		if v, ok := p[k].(string); ok && v != "" {
			detail = v
			break
		}
	}
	if c, ok := p["component"].(string); ok && c != "" {
		detail += " " + c
	}
	if k, ok := p["key"].(string); ok && k != "" {
		detail += " " + k // never the value
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) > 120 {
		detail = detail[:117] + "..."
	}
	return strings.TrimSpace(name + " " + detail)
}

func (g *doctorGuard) check(name string, params json.RawMessage) string {
	switch name {
	case "bash":
		var p tools.BashParams
		if json.Unmarshal(params, &p) != nil {
			return ""
		}
		return g.checkCommand(p.Command, p.Cwd)
	case "write", "edit":
		var p struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(params, &p) != nil {
			return ""
		}
		return g.checkWrite(p.Path)
	}
	return ""
}

func (g *doctorGuard) AfterToolCall(tools.HookContext, string, json.RawMessage, tools.ToolResult, error) {
}

func (g *doctorGuard) checkCommand(cmd, cwd string) string {
	if ghMergeRE.MatchString(cmd) {
		return "doctor may open PRs but never merge them"
	}
	for _, m := range gitPushRE.FindAllStringSubmatch(cmd, -1) {
		args := m[1]
		if forceRE.MatchString(args) {
			return "doctor never force-pushes or deletes remote refs"
		}
		if protectedRE.MatchString(args) || !strings.Contains(args, "doctor/") {
			return "doctor may only push doctor/* branches (git push -u origin doctor/<slug>)"
		}
	}
	if g.source != "" && gitMutationRE.MatchString(cmd) && !strings.Contains(cmd, "worktree") {
		inSource := strings.Contains(cmd, g.source) || within(cleanDir(cwd), g.source)
		inWorktree := strings.Contains(cmd, g.worktrees) || within(cleanDir(cwd), g.worktrees)
		if inSource && !inWorktree {
			return "the source checkout is live (auto-deploy); make changes in a worktree under " + g.worktrees
		}
	}
	return ""
}

func (g *doctorGuard) checkWrite(path string) string {
	abs := cleanDir(path)
	for _, c := range g.creds {
		if abs == c {
			return "doctor cannot write credentials; ask the user to set them"
		}
	}
	if g.source != "" && within(abs, g.source) && !within(abs, g.worktrees) {
		return "the source checkout is live (auto-deploy); edit files in a worktree under " + g.worktrees
	}
	return ""
}

func cleanDir(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	// Resolve symlinks on the longest existing prefix so not-yet-created
	// files compare correctly (e.g. macOS /var -> /private/var).
	p = filepath.Clean(p)
	rest := ""
	for dir := p; ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		if parent := filepath.Dir(dir); parent == dir {
			return p
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

func within(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
