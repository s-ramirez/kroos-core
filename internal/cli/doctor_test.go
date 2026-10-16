package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/health"
	"github.com/s-ramirez/kroos-core/internal/tools"
)

func doctorFixture(t *testing.T) (DoctorConfig, *doctorGuard) {
	t.Helper()
	home, src := t.TempDir(), t.TempDir()
	cfg := DoctorConfig{Home: home, SourceDir: src, Monitor: health.NewMonitor("")}
	return cfg, newDoctorGuard(cfg)
}

func bashCall(g *doctorGuard, cmd, cwd string) string {
	raw, _ := json.Marshal(tools.BashParams{Command: cmd, Cwd: cwd})
	return g.BeforeToolCall(tools.HookContext{}, "bash", raw)
}

func TestDoctorGuardGitRules(t *testing.T) {
	cfg, g := doctorFixture(t)
	src, wt := cleanDir(cfg.SourceDir), cleanDir(cfg.WorktreeRoot())+"/fix-x"
	allowed := []string{
		"git -C " + src + " fetch origin",
		"git -C " + src + " worktree add " + wt + " -b doctor/fix-x origin/main",
		"git -C " + src + " log --oneline -5",
		"cd " + wt + " && git commit -am 'doctor: fix'",
		"git -C " + wt + " push -u origin doctor/fix-x",
		"gh pr create --title 'doctor: fix' --body x",
		"curl -sS -m 10 https://discord.com/api/v10/gateway",
	}
	for _, c := range allowed {
		if r := bashCall(g, c, ""); r != "" {
			t.Errorf("blocked %q: %s", c, r)
		}
	}
	if r := bashCall(g, "git commit -am fix", wt); r != "" {
		t.Errorf("commit in worktree cwd blocked: %s", r)
	}
	blocked := []string{
		"git push origin main",
		"git push -u origin doctor/x --force",
		"git push origin +doctor/x",
		"git push",
		"git -C " + src + " reset --hard origin/main",
		"git -C " + src + " commit -am hack",
		"gh pr merge 12 --squash",
	}
	for _, c := range blocked {
		if r := bashCall(g, c, ""); r == "" {
			t.Errorf("allowed %q", c)
		}
	}
	if r := bashCall(g, "git checkout -b x", src); r == "" {
		t.Error("checkout in live source cwd allowed")
	}
}

func TestDoctorGuardWrites(t *testing.T) {
	cfg, g := doctorFixture(t)
	write := func(path string) string {
		raw, _ := json.Marshal(map[string]string{"path": path, "content": "x"})
		return g.BeforeToolCall(tools.HookContext{}, "write", raw)
	}
	if write(filepath.Join(cfg.SourceDir, "main.go")) == "" {
		t.Error("write to live checkout allowed")
	}
	if write(filepath.Join(cfg.Home, ".env")) == "" || write(filepath.Join(cfg.Home, "auth.json")) == "" {
		t.Error("credential write allowed")
	}
	if r := write(filepath.Join(cfg.WorktreeRoot(), "fix", "main.go")); r != "" {
		t.Errorf("worktree write blocked: %s", r)
	}
	if r := write(filepath.Join(cfg.Home, "skills", "x", "SKILL.md")); r != "" {
		t.Errorf("home write blocked: %s", r)
	}
}

func TestDoctorPromptIncludesFailuresAndLog(t *testing.T) {
	cfg, _ := doctorFixture(t)
	cfg.Monitor.Report("channel:discord", errors.New("open gateway: 4004 authentication failed"))
	cfg.LogFile = filepath.Join(t.TempDir(), "agent.log")
	os.WriteFile(cfg.LogFile, []byte("boot\n[dispatcher] channel discord failed: 4004\n"), 0o600)
	cfg.Note = "discord stopped answering"
	spec := BuildDoctor(nil, nil, cfg)
	for _, want := range []string{"channel:discord", "4004 authentication failed", "[dispatcher] channel discord failed", "discord stopped answering", cfg.WorktreeRoot()} {
		if !strings.Contains(spec.Prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestDoctorProgressAndStepLogging(t *testing.T) {
	cfg, _ := doctorFixture(t)
	cfg.Progress = &DoctorProgress{}
	var logged []string
	cfg.Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	BuildDoctor(nil, []tools.Tool{tools.NewBashTool()}, cfg)
	if !strings.Contains(cfg.Progress.Status(), "0 steps") {
		t.Fatalf("status: %q", cfg.Progress.Status())
	}
	g := newDoctorGuard(cfg)
	g.progress, g.logf = cfg.Progress, cfg.Logf
	raw, _ := json.Marshal(map[string]string{"action": "set", "key": "discord.token", "value": "SECRETVALUE"})
	g.BeforeToolCall(tools.HookContext{}, "kroos_config", raw)
	bashCall(g, "git push origin main", "")
	st := cfg.Progress.Status()
	if !strings.Contains(st, "2 steps") || !strings.Contains(st, "(blocked)") {
		t.Fatalf("status: %q", st)
	}
	if strings.Contains(strings.Join(logged, "\n"), "SECRETVALUE") || len(logged) != 2 {
		t.Fatalf("logged: %v", logged)
	}
	cfg.Progress.Done()
	if cfg.Progress.Status() != "" {
		t.Fatal("status after done")
	}
}
