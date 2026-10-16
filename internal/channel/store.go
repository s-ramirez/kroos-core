package channel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/s-ramirez/kroos-core/internal/memory"
	"github.com/s-ramirez/kroos-core/internal/types"
)

// Provisioner allocates per-conversation resources (workspace dir,
// memory store) on first sighting of a Key. Returning empty values
// leaves the Conversation with no per-thread isolation — the runner
// falls back to the process cwd and its default memory store.
type Provisioner interface {
	Provision(Key) (workspace string, mem *memory.Store, err error)
}

// Binding is one persisted record in the bindings registry.
type Binding struct {
	Channel      string `json:"channel"`
	Thread       string `json:"thread"`
	HistoryFile  string `json:"history_file,omitempty"`
	MemoryDir    string `json:"memory_dir,omitempty"`
	WorkspaceDir string `json:"workspace_dir,omitempty"`
}

// Conversation holds the message history for a single (channel, thread)
// pair. The dispatcher serializes access per conversation, so handlers
// can assume exclusive access to Messages while HandleTurn runs.
type Conversation struct {
	Key      Key
	Messages []types.Message

	// ModelOverride, if non-empty, is set by the dispatcher from the
	// inbound message and cleared after the turn.
	ModelOverride string

	// Attachments, if non-empty, are the current turn's inbound media.
	// Set by the dispatcher from the inbound message and cleared after
	// the turn.
	Attachments []Attachment

	// Compaction state scoped to this conversation.
	LastSummary    string
	LastUsage      *types.Usage
	LastUsageIndex int // -1 means "no authoritative prefix yet"

	// Workspace, if non-empty, is the working directory the runner
	// chdirs to for the duration of each turn. Empty means process cwd.
	Workspace string

	// MemoryStore, if non-nil, overrides the runner's default memory
	// store for this conversation. Empty means use the runner default.
	MemoryStore *memory.Store
}

// persistedConversation is the JSON shape written to disk.
type persistedConversation struct {
	Key            Key             `json:"key"`
	Messages       []types.Message `json:"messages"`
	LastSummary    string          `json:"last_summary,omitempty"`
	LastUsage      *types.Usage    `json:"last_usage,omitempty"`
	LastUsageIndex int             `json:"last_usage_index"`
}

// Store manages in-memory conversations plus optional on-disk
// persistence and per-key provisioning. A zero-value or NewStore()
// value works as an ephemeral in-memory map (backwards compatible).
type Store struct {
	mu          sync.Mutex
	convs       map[Key]*Conversation
	baseDir     string
	provisioner Provisioner
	bindings    map[Key]Binding
}

// NewStore returns an ephemeral in-memory store.
func NewStore() *Store {
	return &Store{convs: make(map[Key]*Conversation)}
}

// NewPersistentStore persists conversation history under baseDir and
// consults provisioner (may be nil) to allocate per-conversation
// workspace / memory on first sighting of each Key.
func NewPersistentStore(baseDir string, p Provisioner) *Store {
	s := &Store{
		convs:       make(map[Key]*Conversation),
		baseDir:     baseDir,
		provisioner: p,
		bindings:    make(map[Key]Binding),
	}
	s.loadBindings()
	return s
}

// Get returns the conversation for key, creating it on first access.
// New conversations are hydrated from disk if persistence is enabled
// and their provisioned workspace / memory resources are resolved.
func (s *Store) Get(key Key) *Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if conv, ok := s.convs[key]; ok {
		return conv
	}
	conv := &Conversation{Key: key, LastUsageIndex: -1}
	s.loadHistory(conv)
	s.provision(conv)
	s.convs[key] = conv
	return conv
}

// Save persists the conversation history to disk. No-op for ephemeral
// stores (baseDir == "").
func (s *Store) Save(key Key) error {
	if s.baseDir == "" {
		return nil
	}
	s.mu.Lock()
	conv, ok := s.convs[key]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	path := s.historyPath(key)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(persistedConversation{
		Key:            conv.Key,
		Messages:       conv.Messages,
		LastSummary:    conv.LastSummary,
		LastUsage:      conv.LastUsage,
		LastUsageIndex: conv.LastUsageIndex,
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Reset clears both in-memory and persisted history for key. The
// provisioned workspace / memory dir are left intact.
func (s *Store) Reset(key Key) {
	s.mu.Lock()
	if conv, ok := s.convs[key]; ok {
		conv.Messages = nil
		conv.LastSummary = ""
		conv.LastUsage = nil
		conv.LastUsageIndex = -1
	}
	base := s.baseDir
	s.mu.Unlock()
	if base == "" {
		return
	}
	if path := s.historyPath(key); path != "" {
		_ = os.Remove(path)
	}
}

func (s *Store) loadHistory(conv *Conversation) {
	if s.baseDir == "" {
		return
	}
	path := s.historyPath(conv.Key)
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var p persistedConversation
	if err := json.Unmarshal(data, &p); err != nil {
		return
	}
	conv.Messages = p.Messages
	conv.LastSummary = p.LastSummary
	conv.LastUsage = p.LastUsage
	conv.LastUsageIndex = p.LastUsageIndex
	if len(p.Messages) == 0 && conv.LastUsageIndex == 0 {
		conv.LastUsageIndex = -1
	}
}

func (s *Store) provision(conv *Conversation) {
	if s.provisioner == nil {
		return
	}
	ws, mem, err := s.provisioner.Provision(conv.Key)
	if err != nil {
		return
	}
	conv.Workspace = ws
	conv.MemoryStore = mem
	if s.bindings == nil {
		return
	}
	b := Binding{
		Channel:      conv.Key.Channel,
		Thread:       conv.Key.Thread,
		HistoryFile:  s.historyPath(conv.Key),
		WorkspaceDir: ws,
	}
	if mem != nil {
		b.MemoryDir = mem.Base()
	}
	if existing, ok := s.bindings[conv.Key]; ok && existing == b {
		return
	}
	s.bindings[conv.Key] = b
	_ = s.saveBindings()
}

func (s *Store) historyPath(key Key) string {
	if s.baseDir == "" {
		return ""
	}
	thread := sanitizeSegment(key.Thread)
	if thread == "" {
		return ""
	}
	return filepath.Join(s.baseDir, "conversations", sanitizeSegment(key.Channel), thread+".json")
}

func (s *Store) bindingsPath() string {
	return filepath.Join(s.baseDir, "bindings.json")
}

func (s *Store) loadBindings() {
	data, err := os.ReadFile(s.bindingsPath())
	if err != nil {
		return
	}
	var list []Binding
	if err := json.Unmarshal(data, &list); err != nil {
		return
	}
	for _, b := range list {
		s.bindings[Key{Channel: b.Channel, Thread: b.Thread}] = b
	}
}

func (s *Store) saveBindings() error {
	path := s.bindingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	list := make([]Binding, 0, len(s.bindings))
	for _, b := range s.bindings {
		list = append(list, b)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// sanitizeSegment produces a path-safe token. Characters outside
// [A-Za-z0-9_.-] become '_'. Empty input returns "".
func sanitizeSegment(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
