package skills

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestApplyLifecycle(t *testing.T) {
	root := t.TempDir()
	mk := func(name string) Skill {
		dir := filepath.Join(root, name)
		os.MkdirAll(dir, 0o755)
		p := filepath.Join(dir, "SKILL.md")
		os.WriteFile(p, []byte("---\nname: "+name+"\ndescription: d\n---\nbody\n"), 0o644)
		return Skill{Name: name, FilePath: p}
	}
	loaded := []Skill{mk("fresh"), mk("idle"), mk("ancient"), mk("pinned"), mk("handmade")}

	u := NewUsage(filepath.Join(root, ".usage.json"))
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	at := func(d time.Duration) { u.SetNow(func() time.Time { return now.Add(-d) }) }
	at(time.Hour)
	u.MarkCreated("fresh")
	at(40 * 24 * time.Hour)
	u.MarkCreated("idle")
	at(100 * 24 * time.Hour)
	u.MarkCreated("ancient")
	u.MarkCreated("pinned")
	u.Update("pinned", func(e *UsageEntry) { e.Pinned = true })
	u.Bump("handmade") // used, but not agent-created

	u.SetNow(func() time.Time { return now })
	res, err := u.ApplyLifecycle(loaded, filepath.Join(root, ".archive"), 30*24*time.Hour, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Staled) != 1 || res.Staled[0] != "idle" {
		t.Fatalf("staled = %v", res.Staled)
	}
	if len(res.Archived) != 1 || res.Archived[0] != "ancient" {
		t.Fatalf("archived = %v", res.Archived)
	}
	if _, err := os.Stat(filepath.Join(root, "ancient")); !os.IsNotExist(err) {
		t.Fatal("ancient should have moved")
	}
	for _, keep := range []string{"fresh", "pinned", "handmade"} {
		if _, err := os.Stat(filepath.Join(root, keep)); err != nil {
			t.Fatalf("%s should remain: %v", keep, err)
		}
	}

	// A use revives a stale skill.
	u.Bump("idle")
	if st := u.Load()["idle"].State; st != StateActive {
		t.Fatalf("expected active after use, got %s", st)
	}
}
