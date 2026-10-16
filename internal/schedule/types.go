// Package schedule owns the persistent scheduled-task store and the
// background ticker that fires due jobs onto an Events channel. It is
// consumed by the runner (to inject scheduled events into the agent loop)
// and by the schedule tool (to let the model add/list/remove jobs).
package schedule

import (
	"encoding/json"
	"time"
)

// Kind discriminates schedule shapes.
type Kind string

const (
	KindAt    Kind = "at"
	KindEvery Kind = "every"
	KindCron  Kind = "cron"
)

// WakeMode controls how aggressively a fired event interrupts the runner.
// In v1 both modes behave identically; the field is recorded for forward
// compatibility with openclaw-style "now vs next-heartbeat" semantics.
const (
	WakeModeNow      = "now"
	WakeModeNextTurn = "next-turn"
)

// Schedule describes when a Job should fire. Exactly one of (At), (Every+Anchor),
// or (Cron) is meaningful, determined by Kind.
type Schedule struct {
	Kind Kind `json:"kind"`

	// At is the absolute fire time for kind=at jobs.
	At *time.Time `json:"at,omitempty"`

	// Every is the interval between fires for kind=every jobs.
	Every Duration `json:"every,omitempty"`

	// Anchor sets the phase of the every grid. The first tick is at anchor
	// (if anchor is in the future) or the next anchor + k*every after now.
	Anchor *time.Time `json:"anchor,omitempty"`

	// Cron is a 5-field standard cron expression (min hour dom month dow)
	// for kind=cron jobs. Parsed via robfig/cron/v3.
	Cron string `json:"cron,omitempty"`
}

// Job is a single scheduled task. Persisted jobs live in the Store; synthetic
// jobs (e.g. heartbeat) live in-memory on the Scheduler and are reconstructed
// from configuration at every startup.
type Job struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Enabled   bool       `json:"enabled"`
	Schedule  Schedule   `json:"schedule"`
	Text      string     `json:"text"`
	WakeMode  string     `json:"wakeMode"`
	CreatedAt time.Time  `json:"createdAt"`
	LastRunAt *time.Time `json:"lastRunAt,omitempty"`
	NextRunAt *time.Time `json:"nextRunAt,omitempty"`

	// Mode is ModeRemind (default: Text is injected into the main
	// conversation) or ModeTask (Text is a prompt run in an isolated
	// background agent whose reply goes to Deliver).
	Mode    string   `json:"mode,omitempty"`
	Deliver Delivery `json:"deliver,omitempty"`
	// Tools optionally restricts a task's tool allowlist; Model overrides
	// its LLM model. Both ignored for reminders.
	Tools []string `json:"tools,omitempty"`
	Model string   `json:"model,omitempty"`
	// ActiveHours, if set, drops fires outside the window.
	ActiveHours *ActiveHours `json:"activeHours,omitempty"`

	// Synthetic flags in-memory-only jobs (heartbeat). Never persisted.
	Synthetic bool `json:"-"`
}

// Event is what flows out of the Scheduler when a Job fires. The runner
// converts it into a <scheduled_event> user message before re-entering
// the agent loop.
type Event struct {
	JobID       string
	Name        string
	Text        string
	WakeMode    string
	Mode        string
	Deliver     Delivery
	Tools       []string
	Model       string
	ActiveHours *ActiveHours
}

// ActiveHours is an "HH:MM" 24h window (Start inclusive, End exclusive;
// wraps midnight when End < Start) in Timezone (IANA; empty = local).
type ActiveHours struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Timezone string `json:"timezone,omitempty"`
}

const (
	ModeRemind = "remind"
	ModeTask   = "task"
)

// Delivery says where a task's reply goes. Target is cli | imessage |
// discord | telegram | none; To is the chat/channel/handle for
// messaging targets.
type Delivery struct {
	Target string `json:"target,omitempty"`
	To     string `json:"to,omitempty"`
}

// Duration is a time.Duration that JSON-marshals as a human-readable string
// (e.g. "15m0s") instead of nanoseconds. This keeps jobs.json hand-editable.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Duration(d).String() + `"`), nil
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}
