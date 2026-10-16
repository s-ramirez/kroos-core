package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/s-ramirez/kroos-core/internal/channel"
	"github.com/s-ramirez/kroos-core/internal/health"
)

func gitRepo(t *testing.T) (dir string, revs []string) {
	t.Helper()
	dir = t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	for _, msg := range []string{"first", "Add slash commands", "Fix typing"} {
		run("commit", "-q", "--allow-empty", "-m", msg)
		revs = append(revs, run("rev-parse", "HEAD"))
	}
	return dir, revs
}

func TestCheckDeploy(t *testing.T) {
	src, revs := gitRepo(t)
	state := filepath.Join(t.TempDir(), "state", "version")

	if msg := checkDeploy(state, src, revs[0]); msg != "" {
		t.Fatalf("first run should be silent: %q", msg)
	}
	if msg := checkDeploy(state, src, revs[0]); msg != "" {
		t.Fatalf("same version should be silent: %q", msg)
	}
	msg := checkDeploy(state, src, revs[2])
	for _, want := range []string{"🚀 kroos updated to " + shortRev(revs[2]), "Add slash commands", "Fix typing"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "first") {
		t.Errorf("message includes already-deployed commit:\n%s", msg)
	}
	if got := checkDeploy(state, "", revs[1]); !strings.Contains(got, "(from "+shortRev(revs[2])+")") {
		t.Errorf("no-source message: %q", got)
	}
	if checkDeploy(state, src, "") != "" {
		t.Error("unknown revision should be silent")
	}
}

func TestAnnounceDeployRetriesUntilChannelReady(t *testing.T) {
	old := deployRetry
	deployRetry = 10 * time.Millisecond
	t.Cleanup(func() { deployRetry = old })
	var calls atomic.Int32
	done := make(chan struct{})
	notify := func(ctx context.Context, target, text string) error {
		if calls.Add(1) < 2 {
			return errors.New("discord: session not initialized")
		}
		if target != "discord" || !strings.Contains(text, "updated") {
			t.Errorf("target=%s text=%q", target, text)
		}
		close(done)
		return nil
	}
	announceDeploy(context.Background(), "discord", "🚀 kroos updated to abc", notify, func(string, ...any) {})
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("never delivered")
	}
}

func TestHomeNotifierAutoAndExplicit(t *testing.T) {
	discord := newHomeChannel("discord", "d-home")
	telegram := newHomeChannel("telegram", "")
	reg := channel.NewRegistry()
	reg.Register(discord)
	reg.Register(telegram)
	notify := homeNotifier(func() *channel.Registry { return reg }, health.NewMonitor(""))

	if err := notify(context.Background(), "auto", "hi"); err != nil || len(discord.sent) != 1 || discord.sent[0].Thread != "d-home" {
		t.Fatalf("auto: %v %+v", err, discord.sent)
	}
	if err := notify(context.Background(), "telegram", "hi"); err == nil || !strings.Contains(err.Error(), "no home channel") {
		t.Fatalf("telegram without home: %v", err)
	}
}
