package heartbeat

import (
	"fmt"
	"io"
	"time"

	"github.com/s-ramirez/kroos-core/internal/schedule"
)

// Config captures the runtime-configurable bits of the heartbeat loop.
// A Config with Interval == 0 means the heartbeat is disabled.
type Config struct {
	Interval time.Duration // cadence; 0 disables the heartbeat
	Target   string        // "cli" | "imessage" | "discord" | "telegram" | "none"
	To       string        // recipient handle/channel id for non-CLI targets
	Model    string        // model override for heartbeat turns; "" = default
	Prompt   string        // user-visible text sent each tick
	// ActiveStart, ActiveEnd, Timezone all gate when the heartbeat fires.
	// Empty values disable the corresponding gate.
	ActiveStart string
	ActiveEnd   string
	Timezone    string
}

// DefaultPrompt is the nudge sent every heartbeat tick.
const DefaultPrompt = "Heartbeat tick: if there is nothing actionable, reply HEARTBEAT_OK."

// ConfigFromEnv reads KROOS_HEARTBEAT_* and returns a Config.
// Returns (Config{}, nil) when the heartbeat is disabled (interval unset).
// An error is returned when the interval is present but malformed.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	interval := getenv("KROOS_HEARTBEAT_INTERVAL")
	if interval == "" {
		return Config{}, nil
	}
	d, err := time.ParseDuration(interval)
	if err != nil || d <= 0 {
		return Config{}, fmt.Errorf("invalid KROOS_HEARTBEAT_INTERVAL %q", interval)
	}
	target := getenv("KROOS_HEARTBEAT_TARGET")
	if target == "" {
		target = "cli"
	}
	prompt := getenv("KROOS_HEARTBEAT_PROMPT")
	if prompt == "" {
		prompt = DefaultPrompt
	}
	return Config{
		Interval:    d,
		Target:      target,
		To:          getenv("KROOS_HEARTBEAT_TO"),
		Model:       getenv("KROOS_HEARTBEAT_MODEL"),
		Prompt:      prompt,
		ActiveStart: getenv("KROOS_HEARTBEAT_ACTIVE_HOURS_START"),
		ActiveEnd:   getenv("KROOS_HEARTBEAT_ACTIVE_HOURS_END"),
		Timezone:    getenv("KROOS_HEARTBEAT_TIMEZONE"),
	}, nil
}

// Register inserts the heartbeat job into the scheduler. It is a no-op
// when cfg.Interval == 0. The scheduler must already be constructed.
func Register(scheduler *schedule.Scheduler, cfg Config, warn io.Writer) {
	if cfg.Interval == 0 || scheduler == nil {
		return
	}
	now := time.Now()
	scheduler.Inject(schedule.Job{
		Name:    "heartbeat",
		Enabled: true,
		Schedule: schedule.Schedule{
			Kind:   schedule.KindEvery,
			Every:  schedule.Duration(cfg.Interval),
			Anchor: &now,
		},
		Text: cfg.Prompt,
	})
	if warn != nil {
		fmt.Fprintf(warn, "heartbeat enabled (every %s, target=%s)\n", cfg.Interval, cfg.Target)
	}
}
