package schedule

import (
	"context"
	"sync"
	"time"
)

// Scheduler runs a background ticker that fires due jobs onto an Events
// channel. It coordinates persisted jobs (via Store) and synthetic in-memory
// jobs injected at startup (e.g. heartbeat).
//
// The runner reads from Events() and converts each event into a
// <scheduled_event> user message before re-entering the agent loop.
type Scheduler struct {
	store *Store

	events chan Event
	stopCh chan struct{}
	doneCh chan struct{}

	mu        sync.Mutex
	synthetic []Job

	nowFn        func() time.Time
	tickInterval time.Duration
}

// NewScheduler creates a scheduler over store. bufferSize controls the Events
// channel buffer (default 16 if <= 0).
func NewScheduler(store *Store, bufferSize int) *Scheduler {
	if bufferSize <= 0 {
		bufferSize = 16
	}
	return &Scheduler{
		store:        store,
		events:       make(chan Event, bufferSize),
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
		nowFn:        time.Now,
		tickInterval: time.Second,
	}
}

// Events returns the channel to receive scheduled-job firings.
func (s *Scheduler) Events() <-chan Event { return s.events }

// Store returns the underlying persistent store. Used by the runner to list
// user-managed jobs for the system prompt; synthetic (heartbeat) jobs are not
// included.
func (s *Scheduler) Store() *Store { return s.store }

// Inject adds a synthetic (non-persisted) job. Used for heartbeat.
// CreatedAt and NextRunAt are populated from the current time if unset.
func (s *Scheduler) Inject(job Job) {
	job.Synthetic = true
	if job.ID == "" {
		job.ID = newID()
	}
	now := s.nowFn()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	if job.WakeMode == "" {
		job.WakeMode = WakeModeNow
	}
	if next, ok := NextRunAt(job, now); ok {
		job.NextRunAt = &next
	}
	s.mu.Lock()
	s.synthetic = append(s.synthetic, job)
	s.mu.Unlock()
}

// Synthetic returns a copy of the in-memory built-in jobs (heartbeat,
// curator). Nil-safe.
func (s *Scheduler) Synthetic() []Job {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, len(s.synthetic))
	copy(out, s.synthetic)
	return out
}

// Start launches the ticker goroutine. It runs until ctx is cancelled or
// Stop is called.
func (s *Scheduler) Start(ctx context.Context) {
	go s.run(ctx)
}

// Stop signals the scheduler to exit and blocks until the goroutine returns.
// Safe to call multiple times.
func (s *Scheduler) Stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
	<-s.doneCh
}

func (s *Scheduler) run(ctx context.Context) {
	defer close(s.doneCh)
	ticker := time.NewTicker(s.tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

// tick checks all persisted and synthetic jobs and fires those whose
// NextRunAt has elapsed. If the events channel is full, fire is a no-op
// for that job and the next tick will retry — the scheduler self-throttles
// to consumer pace.
func (s *Scheduler) tick() {
	now := s.nowFn()

	for _, job := range s.store.List() {
		if !job.Enabled || job.NextRunAt == nil {
			continue
		}
		if job.NextRunAt.After(now) {
			continue
		}
		if s.fire(job) {
			_ = s.store.MarkRun(job.ID, now)
		}
	}

	s.mu.Lock()
	for i := range s.synthetic {
		j := &s.synthetic[i]
		if !j.Enabled || j.NextRunAt == nil {
			continue
		}
		if j.NextRunAt.After(now) {
			continue
		}
		if !s.fire(*j) {
			continue
		}
		t := now
		j.LastRunAt = &t
		if next, ok := NextRunAt(*j, now); ok {
			j.NextRunAt = &next
		} else {
			j.NextRunAt = nil
		}
	}
	s.mu.Unlock()
}

func (s *Scheduler) fire(job Job) bool {
	ev := Event{
		JobID:    job.ID,
		Name:     job.Name,
		Text:     job.Text,
		WakeMode: job.WakeMode,
		Mode:     job.Mode,
		Deliver:  job.Deliver,
		Tools:    job.Tools,
		Model:    job.Model,
		Agent:    job.Agent,

		ActiveHours: job.ActiveHours,
	}
	select {
	case s.events <- ev:
		return true
	default:
		return false
	}
}
