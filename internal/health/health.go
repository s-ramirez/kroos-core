// Package health tracks failing components (channels, providers) so kroos
// can tell the user and let the doctor investigate. Components are named
// "<kind>:<name>", e.g. "channel:discord", "provider:chatgpt".
package health

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// DefaultThreshold is how many consecutive failures open an incident.
const DefaultThreshold = 3

// Incident is a component that keeps failing.
type Incident struct {
	Component string    `json:"component"`
	Error     string    `json:"error"`
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// Monitor records failures and fires OnIncident once per incident, when a
// component reaches Threshold consecutive failures. A success resolves it.
// All methods are safe on a nil *Monitor (no-ops).
type Monitor struct {
	Threshold  int
	OnIncident func(Incident)

	path           string
	mu             sync.Mutex
	failures       map[string]*Incident // consecutive failures per component
	open           map[string]bool      // reached threshold and notified
	restarters     map[string]func()
	restartProcess bool
}

// NewMonitor persists open incidents at path (empty = memory only).
func NewMonitor(path string) *Monitor {
	return &Monitor{
		Threshold:  DefaultThreshold,
		path:       path,
		failures:   map[string]*Incident{},
		open:       map[string]bool{},
		restarters: map[string]func(){},
	}
}

// Report records one failure of component.
func (m *Monitor) Report(component string, err error) {
	if m == nil || err == nil {
		return
	}
	now := time.Now().UTC()
	m.mu.Lock()
	inc, ok := m.failures[component]
	if !ok {
		inc = &Incident{Component: component, FirstSeen: now}
		m.failures[component] = inc
	}
	inc.Count++
	inc.LastSeen = now
	inc.Error = err.Error()
	fire := !m.open[component] && inc.Count >= max(m.Threshold, 1)
	if fire {
		m.open[component] = true
	}
	snapshot := *inc
	m.mu.Unlock()

	m.save()
	if fire && m.OnIncident != nil {
		m.OnIncident(snapshot)
	}
}

// Resolve clears component's failures. It reports whether an open incident closed.
func (m *Monitor) Resolve(component string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	_, had := m.failures[component]
	wasOpen := m.open[component]
	delete(m.failures, component)
	delete(m.open, component)
	m.mu.Unlock()
	if had {
		m.save()
	}
	return wasOpen
}

// Failing lists components with any recorded failures, worst first.
func (m *Monitor) Failing() []Incident {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	out := make([]Incident, 0, len(m.failures))
	for _, inc := range m.failures {
		out = append(out, *inc)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Component < out[j].Component
	})
	return out
}

// IsOpen reports whether component has an open incident.
func (m *Monitor) IsOpen(component string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.open[component]
}

// SetRestart registers how to restart component now (e.g. retry a listener).
func (m *Monitor) SetRestart(component string, fn func()) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.restarters[component] = fn
	m.mu.Unlock()
}

// Restartable lists components that can be restarted in-process.
func (m *Monitor) Restartable() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	out := make([]string, 0, len(m.restarters))
	for c := range m.restarters {
		out = append(out, c)
	}
	m.mu.Unlock()
	sort.Strings(out)
	return out
}

// Restart triggers component's restart hook.
func (m *Monitor) Restart(component string) error {
	if m == nil {
		return errors.New("health monitor not available")
	}
	m.mu.Lock()
	fn, ok := m.restarters[component]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%s cannot be restarted in-process (restartable: %v)", component, m.Restartable())
	}
	fn()
	return nil
}

// RequestProcessRestart asks for a full process restart once the current
// doctor run has delivered its report (needed for config changes).
func (m *Monitor) RequestProcessRestart() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.restartProcess = true
	m.mu.Unlock()
}

// TakeProcessRestart reports and clears a pending process-restart request.
func (m *Monitor) TakeProcessRestart() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.restartProcess
	m.restartProcess = false
	return r
}

// save writes current failures so a restart (or the doctor) can see history.
func (m *Monitor) save() {
	if m.path == "" {
		return
	}
	data, err := json.MarshalIndent(m.Failing(), "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return
	}
	tmp := m.path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, m.path)
	}
}
