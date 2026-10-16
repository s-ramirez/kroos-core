package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Lifecycle states for agent-created skills.
const (
	StateActive   = "active"
	StateStale    = "stale"
	StateArchived = "archived"
)

// CreatedByAgent marks skills authored via skill_manage. Only these are
// eligible for curation; hand-written skills are never touched.
const CreatedByAgent = "agent"

// UsageEntry is per-skill telemetry and provenance, kept in a sidecar
// file so SKILL.md stays user-authored content only.
type UsageEntry struct {
	UseCount  int        `json:"use_count"`
	LastUsed  *time.Time `json:"last_used,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	CreatedBy string     `json:"created_by,omitempty"`
	Pinned    bool       `json:"pinned,omitempty"`
	State     string     `json:"state,omitempty"`
}

// LastActivity is the later of LastUsed and CreatedAt (zero if neither).
func (u UsageEntry) LastActivity() time.Time {
	var t time.Time
	if u.CreatedAt != nil {
		t = *u.CreatedAt
	}
	if u.LastUsed != nil && u.LastUsed.After(t) {
		t = *u.LastUsed
	}
	return t
}

// Usage is the JSON sidecar store ($KROOS_HOME/skills/.usage.json). Writes
// are atomic (temp + rename) and serialized in-process.
type Usage struct {
	path string
	mu   sync.Mutex
	now  func() time.Time
}

func NewUsage(path string) *Usage { return &Usage{path: path, now: time.Now} }

// SetNow overrides the clock (tests).
func (u *Usage) SetNow(fn func() time.Time) { u.now = fn }

// Load returns all entries. Missing or corrupt files yield an empty map.
func (u *Usage) Load() map[string]UsageEntry {
	if u == nil {
		return map[string]UsageEntry{}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.load()
}

func (u *Usage) load() map[string]UsageEntry {
	out := map[string]UsageEntry{}
	if data, err := os.ReadFile(u.path); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

// Update applies fn to name's entry under the lock and persists it.
func (u *Usage) Update(name string, fn func(e *UsageEntry)) error {
	if u == nil || u.path == "" {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	all := u.load()
	e := all[name]
	fn(&e)
	all[name] = e
	return u.save(all)
}

// Delete drops name's entry.
func (u *Usage) Delete(name string) error {
	if u == nil || u.path == "" {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	all := u.load()
	delete(all, name)
	return u.save(all)
}

func (u *Usage) save(all map[string]UsageEntry) error {
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(u.path), 0o755); err != nil {
		return err
	}
	tmp := u.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, u.path)
}

// Bump records one use.
func (u *Usage) Bump(name string) error {
	if u == nil {
		return nil
	}
	now := u.now().UTC()
	return u.Update(name, func(e *UsageEntry) {
		e.UseCount++
		e.LastUsed = &now
		if e.State == StateStale {
			e.State = StateActive
		}
	})
}

// MarkCreated records agent provenance for a new skill.
func (u *Usage) MarkCreated(name string) error {
	if u == nil {
		return nil
	}
	now := u.now().UTC()
	return u.Update(name, func(e *UsageEntry) {
		e.CreatedAt = &now
		e.CreatedBy = CreatedByAgent
		e.State = StateActive
	})
}

// Archive moves dir into <archiveRoot>/<name>-<timestamp> and marks the
// entry archived. Archives are recoverable by moving the dir back.
func (u *Usage) Archive(name, dir, archiveRoot string) (string, error) {
	if err := os.MkdirAll(archiveRoot, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(archiveRoot, fmt.Sprintf("%s-%s", name, u.now().UTC().Format("20060102T150405")))
	if err := os.Rename(dir, dest); err != nil {
		return "", err
	}
	return dest, u.Update(name, func(e *UsageEntry) { e.State = StateArchived })
}

// LifecycleResult lists the transitions applied by ApplyLifecycle.
type LifecycleResult struct {
	Staled   []string
	Archived []string
}

// ApplyLifecycle transitions agent-created, unpinned skills by idle
// time: active → stale after staleAfter, stale → archived after
// archiveAfter. Skills with no recorded activity are left alone.
func (u *Usage) ApplyLifecycle(loaded []Skill, archiveRoot string, staleAfter, archiveAfter time.Duration) (LifecycleResult, error) {
	var res LifecycleResult
	entries := u.Load()
	now := u.now()
	for _, s := range loaded {
		e, ok := entries[s.Name]
		if !ok || e.CreatedBy != CreatedByAgent || e.Pinned {
			continue
		}
		last := e.LastActivity()
		if last.IsZero() {
			continue
		}
		idle := now.Sub(last)
		switch {
		case idle >= archiveAfter:
			if _, err := u.Archive(s.Name, filepath.Dir(s.FilePath), archiveRoot); err != nil {
				return res, fmt.Errorf("archive %s: %w", s.Name, err)
			}
			res.Archived = append(res.Archived, s.Name)
		case idle >= staleAfter && e.State != StateStale:
			if err := u.Update(s.Name, func(e *UsageEntry) { e.State = StateStale }); err != nil {
				return res, err
			}
			res.Staled = append(res.Staled, s.Name)
		}
	}
	return res, nil
}
