package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	CuratedFilename = "MEMORY.md"
	SessionsDir     = "sessions"
	VaultDir        = "vault"
	CuratedMaxBytes = 3000
)

// VaultSubdirs are the fixed folders created under vault/ on first write,
// matching the article's Obsidian layout.
var VaultSubdirs = []string{"Memory", "Knowledge", "Journal", "Notes"}

// Store owns a $KROOS_HOME/memory directory. All operations are sandboxed to base.
// The layout is created lazily on first write.
type Store struct {
	base    string
	ensured bool
}

func NewStore(base string) *Store {
	return &Store{base: base}
}

func (s *Store) Base() string { return s.base }

// Ensure creates the memory layout (sessions/, vault/<subdirs>) if missing.
func (s *Store) Ensure() error {
	if s.ensured {
		return nil
	}
	dirs := []string{s.base, filepath.Join(s.base, SessionsDir)}
	for _, sub := range VaultSubdirs {
		dirs = append(dirs, filepath.Join(s.base, VaultDir, sub))
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	s.ensured = true
	return nil
}

// LoadCurated reads MEMORY.md. Missing file → empty string, no error.
// Content is truncated with a warning if it exceeds CuratedMaxBytes.
func (s *Store) LoadCurated() (string, error) {
	data, err := os.ReadFile(filepath.Join(s.base, CuratedFilename))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	str := string(data)
	if len(str) > CuratedMaxBytes {
		str = str[:CuratedMaxBytes] + "\n\n[truncated: MEMORY.md exceeds " + fmt.Sprintf("%d", CuratedMaxBytes) + " byte budget]"
	}
	return str, nil
}

// Read returns the raw bytes of a sandboxed file.
func (s *Store) Read(rel string) ([]byte, error) {
	abs, err := SandboxPath(s.base, rel)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(abs)
}

// Write writes data to rel. If appendMode is true, bytes are appended;
// otherwise the file is overwritten. Parent dirs and the base layout are
// created on demand.
func (s *Store) Write(rel string, data []byte, appendMode bool) error {
	abs, err := SandboxPath(s.base, rel)
	if err != nil {
		return err
	}
	if err := s.Ensure(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	if appendMode {
		f, err := os.OpenFile(abs, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.Write(data)
		return err
	}
	return os.WriteFile(abs, data, 0o644)
}

// SearchHit is a single match from Search.
type SearchHit struct {
	Path    string
	Line    int
	Snippet string
}

// Search does a case-insensitive literal substring scan across all markdown
// files in the store. Returns up to max hits.
func (s *Store) Search(query string, max int) ([]SearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if max <= 0 {
		max = 20
	}
	needle := strings.ToLower(query)

	var hits []SearchHit
	walkErr := filepath.Walk(s.base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		rel, _ := filepath.Rel(s.base, path)
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(strings.ToLower(line), needle) {
				hits = append(hits, SearchHit{
					Path:    rel,
					Line:    i + 1,
					Snippet: strings.TrimSpace(line),
				})
				if len(hits) >= max {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return nil, walkErr
	}
	return hits, nil
}

// ListEntry describes a single directory entry returned by List.
type ListEntry struct {
	Name  string
	IsDir bool
	Size  int64
	Mtime time.Time
}

// List returns the contents of a directory under the store, sorted by name.
// Missing directory returns (nil, nil) to match the "empty store" convention.
func (s *Store) List(rel string) ([]ListEntry, error) {
	target := s.base
	if rel != "" && rel != "." {
		abs, err := SandboxPath(s.base, rel)
		if err != nil {
			return nil, err
		}
		target = abs
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]ListEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, ListEntry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  info.Size(),
			Mtime: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
