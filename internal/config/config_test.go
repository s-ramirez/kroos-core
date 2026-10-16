package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPrecedence(t *testing.T) {
	dir := t.TempDir()
	proj, glob := filepath.Join(dir, "p.json"), filepath.Join(dir, "g.json")
	if err := Save(proj, map[string]string{"model": "proj"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(glob, map[string]string{"model": "glob", "heartbeat.interval": "15m", "KROOS_DISCORD_GUILD_TAG": "@k"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KROOS_MODEL", "")
	os.Unsetenv("KROOS_MODEL")
	t.Setenv("KROOS_HEARTBEAT_INTERVAL", "1m")
	t.Setenv("KROOS_DISCORD_GUILD_TAG", "")
	os.Unsetenv("KROOS_DISCORD_GUILD_TAG")

	if err := Apply(proj, glob); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("KROOS_MODEL"); got != "proj" {
		t.Errorf("model = %q, want proj", got)
	}
	if got := os.Getenv("KROOS_HEARTBEAT_INTERVAL"); got != "1m" {
		t.Errorf("env should win, got %q", got)
	}
	if got := os.Getenv("KROOS_DISCORD_GUILD_TAG"); got != "@k" {
		t.Errorf("env-name key = %q", got)
	}
}

func TestKeysUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range Keys {
		if seen[k.Name] || seen[k.Env] {
			t.Errorf("duplicate %s/%s", k.Name, k.Env)
		}
		seen[k.Name], seen[k.Env] = true, true
	}
}
