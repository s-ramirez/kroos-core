package skills

import "sync"

// Registry is a concurrency-safe container for the loaded skill set.
// It exists so runtime tools (skill_manage) can hot-swap the visible
// skills without reaching into every consumer.
//
// Snapshot returns a defensive copy suitable for iteration; Replace
// atomically swaps the backing slice.
type Registry struct {
	mu      sync.RWMutex
	skills  []Skill
	version uint64
}

func NewRegistry(initial []Skill) *Registry {
	cp := make([]Skill, len(initial))
	copy(cp, initial)
	return &Registry{skills: cp}
}

// Snapshot returns a copy of the current skill set. Nil-safe: a nil
// receiver yields a nil slice so callers don't need to guard.
func (r *Registry) Snapshot() []Skill {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Skill, len(r.skills))
	copy(out, r.skills)
	return out
}

// Replace atomically swaps the backing skill set.
func (r *Registry) Replace(next []Skill) {
	if r == nil {
		return
	}
	cp := make([]Skill, len(next))
	copy(cp, next)
	r.mu.Lock()
	r.skills = cp
	r.version++
	r.mu.Unlock()
}

// Version increments on every Replace so consumers (the runner's cached
// system prompt) can detect that the skill set changed.
func (r *Registry) Version() uint64 {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.version
}

// Find returns the skill with the given name.
func (r *Registry) Find(name string) (Skill, bool) {
	for _, s := range r.Snapshot() {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}
