package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/s-ramirez/kroos-core/internal/channel"
	clichannel "github.com/s-ramirez/kroos-core/internal/channel/cli"
	"github.com/s-ramirez/kroos-core/internal/channel/sched"
	"github.com/s-ramirez/kroos-core/internal/cli"
	"github.com/s-ramirez/kroos-core/internal/health"
	"github.com/s-ramirez/kroos-core/internal/schedule"
	"github.com/s-ramirez/kroos-core/internal/tasks"
)

// notifyOrder is where incident alerts go first (a working channel's home).
var notifyOrder = []string{"discord", "telegram", "imessage"}

// incidentNotifier posts an incident to the first healthy channel with a
// home thread, falling back to the CLI (stdout/log).
func incidentNotifier(channels func() *channel.Registry, monitor *health.Monitor, logf func(string, ...any)) func(health.Incident) {
	return func(inc health.Incident) {
		text := fmt.Sprintf("⚠️ kroos: %s is failing (%d attempts): %s\nReply /doctor to let me investigate and fix it.",
			inc.Component, inc.Count, inc.Error)
		logf("incident: %s: %s", inc.Component, inc.Error)
		reg := channels()
		if reg == nil {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for _, name := range notifyOrder {
				if monitor.IsOpen("channel:" + name) {
					continue
				}
				ch, ok := reg.Lookup(name)
				if !ok {
					continue
				}
				h, ok := ch.(channel.Homer)
				if !ok || h.Home() == "" {
					continue
				}
				if err := ch.Send(ctx, channel.Outbound{Channel: name, Thread: h.Home(), Content: text}); err == nil {
					return
				}
			}
			if ch, ok := reg.Lookup(clichannel.Name); ok {
				_ = ch.Send(ctx, channel.Outbound{Channel: clichannel.Name, Thread: clichannel.DefaultThread, Content: text + "\n"})
			}
		}()
	}
}

// doctorCommand is the /doctor slash command: starts an investigation that
// reports back to the conversation it was asked from.
func doctorCommand(exec *tasks.Executor, monitor *health.Monitor, progress *cli.DoctorProgress) channel.SlashCommand {
	return func(_ context.Context, conv *channel.Conversation, args string) string {
		if exec == nil {
			return "doctor unavailable: background tasks are disabled\n"
		}
		if args == "status" {
			if st := progress.Status(); st != "" {
				return "🩺 Doctor " + st + "\n"
			}
			return "🩺 The doctor is idle. Reply /doctor to start an investigation.\n"
		}
		target, to := conv.Key.Channel, conv.Key.Thread
		if target == sched.Name {
			target, to = clichannel.Name, clichannel.DefaultThread
		}
		_, ok := exec.Submit(tasks.Task{
			ID:      cli.DoctorAgent,
			Name:    cli.DoctorAgent,
			Agent:   cli.DoctorAgent,
			Prompt:  args,
			Deliver: schedule.Delivery{Target: target, To: to},
			Timeout: cli.DoctorTimeout,
		})
		if !ok {
			return "🩺 The doctor is already investigating (" + progress.Status() + "); the report will arrive where it was requested.\n"
		}
		what := "kroos"
		if failing := monitor.Failing(); len(failing) > 0 {
			var names []string
			for _, inc := range failing {
				names = append(names, inc.Component)
			}
			what = strings.Join(names, ", ")
		}
		return fmt.Sprintf("🩺 Investigating %s. I'll report back here when done (at most %s). /doctor status shows progress.\n", what, cli.DoctorTimeout)
	}
}

// doctorAfterRun reports a failed doctor run and performs a requested
// process restart (launchd's KeepAlive brings kroos back up).
func doctorAfterRun(monitor *health.Monitor, progress *cli.DoctorProgress, deliver tasks.Deliverer, service bool, logf func(string, ...any)) func(tasks.Task, tasks.Run) {
	return func(t tasks.Task, r tasks.Run) {
		if t.Agent != cli.DoctorAgent {
			return
		}
		progress.Done()
		if r.Status == "error" && deliver != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = deliver(ctx, t.Deliver, "🩺 The doctor run failed: "+r.Error)
			cancel()
		}
		if monitor.TakeProcessRestart() && service {
			logf("doctor requested a restart; exiting so the supervisor restarts kroos")
			time.Sleep(time.Second) // let the report flush
			os.Exit(0)
		}
	}
}

// sourceDir is the kroos code checkout the doctor may fix through PRs:
// KROOS_SOURCE_DIR, else the checkout containing this binary (bin/kroos).
func sourceDir() string {
	if d := os.Getenv("KROOS_SOURCE_DIR"); d != "" {
		return d
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	repo := filepath.Dir(filepath.Dir(exe))
	data, err := os.ReadFile(filepath.Join(repo, "go.mod"))
	if err != nil || !strings.Contains(string(data), "module github.com/s-ramirez/kroos-core") {
		return ""
	}
	return repo
}
