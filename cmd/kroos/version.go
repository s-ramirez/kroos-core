package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/s-ramirez/kroos-core/internal/channel"
	clichannel "github.com/s-ramirez/kroos-core/internal/channel/cli"
	"github.com/s-ramirez/kroos-core/internal/health"
)

// buildRevision is the git commit this binary was built from ("" if unknown).
func buildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
}

func shortRev(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// deployMessage describes an update from old to rev, listing the commits in
// between when the source checkout is available.
func deployMessage(src, old, rev string) string {
	msg := "🚀 kroos updated to " + shortRev(rev)
	if src == "" {
		return msg + " (from " + shortRev(old) + ")"
	}
	out, err := exec.Command("git", "-C", src, "log", "--no-decorate", "--format=%h %s", "-n", "15", old+".."+rev).Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if err != nil || len(lines) == 0 || lines[0] == "" {
		return msg + " (from " + shortRev(old) + ")"
	}
	var b strings.Builder
	b.WriteString(msg + ":\n")
	for _, l := range lines {
		b.WriteString("• " + l + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// checkDeploy records rev in statePath and returns the announcement to send,
// or "" (first run, unchanged, or unknown revision).
func checkDeploy(statePath, src, rev string) string {
	if rev == "" {
		return ""
	}
	data, _ := os.ReadFile(statePath)
	old := strings.TrimSpace(string(data))
	if old == rev {
		return ""
	}
	_ = os.MkdirAll(filepath.Dir(statePath), 0o700)
	_ = os.WriteFile(statePath, []byte(rev+"\n"), 0o600)
	if old == "" {
		return ""
	}
	return deployMessage(src, old, rev)
}

// deployRetry is the wait between delivery attempts while channels connect.
var deployRetry = 5 * time.Second

// announceDeploy sends text via notify, retrying while channels connect.
func announceDeploy(ctx context.Context, target string, text string, notify func(ctx context.Context, target, text string) error, logf func(string, ...any)) {
	if text == "" || target == "none" {
		return
	}
	go func() {
		deadline := time.Now().Add(2 * time.Minute)
		for {
			err := notify(ctx, target, text)
			if err == nil {
				return
			}
			if ctx.Err() != nil || time.Now().After(deadline) {
				logf("deploy notification not sent: %v", err)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(deployRetry):
			}
		}
	}()
}

// homeNotifier sends text to a channel's home thread. target "auto" tries
// notifyOrder, skipping channels with open incidents; a channel name sends
// only there. The CLI is the last resort for "auto".
func homeNotifier(channels func() *channel.Registry, monitor *health.Monitor) func(ctx context.Context, target, text string) error {
	return func(ctx context.Context, target, text string) error {
		reg := channels()
		if reg == nil {
			return errors.New("channels not ready")
		}
		order := []string{target}
		if target == "" || target == "auto" {
			order = notifyOrder
		}
		var errs []string
		for _, name := range order {
			if monitor.IsOpen("channel:" + name) {
				continue
			}
			ch, ok := reg.Lookup(name)
			if !ok {
				continue
			}
			h, ok := ch.(channel.Homer)
			if !ok || h.Home() == "" {
				errs = append(errs, name+": no home channel configured")
				continue
			}
			c, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := ch.Send(c, channel.Outbound{Channel: name, Thread: h.Home(), Content: text})
			cancel()
			if err == nil {
				return nil
			}
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
		if target == "" || target == "auto" {
			if ch, ok := reg.Lookup(clichannel.Name); ok {
				return ch.Send(ctx, channel.Outbound{Channel: clichannel.Name, Thread: clichannel.DefaultThread, Content: text + "\n"})
			}
		}
		if len(errs) == 0 {
			return fmt.Errorf("%s not available", target)
		}
		return errors.New(strings.Join(errs, "; "))
	}
}
