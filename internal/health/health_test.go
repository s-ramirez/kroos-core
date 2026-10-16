package health

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMonitorFiresOncePerIncident(t *testing.T) {
	m := NewMonitor(filepath.Join(t.TempDir(), "health.json"))
	var fired []Incident
	m.OnIncident = func(i Incident) { fired = append(fired, i) }

	for i := 0; i < 5; i++ {
		m.Report("channel:discord", errors.New("401 unauthorized"))
	}
	if len(fired) != 1 || fired[0].Count != DefaultThreshold || !m.IsOpen("channel:discord") {
		t.Fatalf("fired = %+v", fired)
	}
	if !m.Resolve("channel:discord") || m.IsOpen("channel:discord") || len(m.Failing()) != 0 {
		t.Fatal("resolve did not clear incident")
	}
	// Reopening fires again.
	for i := 0; i < DefaultThreshold; i++ {
		m.Report("channel:discord", errors.New("again"))
	}
	if len(fired) != 2 {
		t.Fatalf("reopen fired %d times", len(fired))
	}
}

func TestMonitorBelowThresholdDoesNotFire(t *testing.T) {
	m := NewMonitor("")
	m.OnIncident = func(Incident) { t.Fatal("should not fire") }
	m.Report("provider:omlx", errors.New("timeout"))
	m.Report("provider:omlx", errors.New("timeout"))
	if m.Resolve("provider:omlx") {
		t.Fatal("no open incident expected")
	}
}

func TestMonitorPersistsAndRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health.json")
	m := NewMonitor(path)
	m.Report("channel:telegram", errors.New("getMe: 404"))
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "getMe: 404") {
		t.Fatalf("persisted: %s %v", data, err)
	}

	kicked := false
	m.SetRestart("channel:telegram", func() { kicked = true })
	if err := m.Restart("channel:telegram"); err != nil || !kicked {
		t.Fatalf("restart: %v kicked=%v", err, kicked)
	}
	if err := m.Restart("channel:nope"); err == nil {
		t.Fatal("want error for unknown component")
	}

	m.RequestProcessRestart()
	if !m.TakeProcessRestart() || m.TakeProcessRestart() {
		t.Fatal("process restart flag should be taken once")
	}
}

func TestNilMonitorIsSafe(t *testing.T) {
	var m *Monitor
	m.Report("x", errors.New("y"))
	m.Resolve("x")
	m.SetRestart("x", func() {})
	_ = m.Failing()
	_ = m.IsOpen("x")
	if err := m.Restart("x"); err == nil {
		t.Fatal("want error")
	}
}
