package subagents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProfile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestLoadProfilesParsesBinding(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "ops.md", `---
name: ops
description: Ops triage
channel: discord
threads: [111, 222]
senders: any
tools: [web_search, memory]
---
Focus on ops.
`)
	profiles, diags, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(profiles) != 1 {
		t.Fatalf("want 1 profile, got %d", len(profiles))
	}
	p := profiles[0]
	if p.Name != "ops" || p.Channel != "discord" || p.Senders != SendersAny || p.Model != "" {
		t.Fatalf("unexpected profile: %+v", p)
	}
	if strings.Join(p.Threads, ",") != "111,222" {
		t.Fatalf("threads = %v", p.Threads)
	}
	if p.Prompt != "Focus on ops." {
		t.Fatalf("prompt = %q", p.Prompt)
	}
}

func TestLoadProfilesDefaultsSendersToOwners(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "p.md", "---\nchannel: discord\nthreads: [1]\ntools: [bash]\n---\nx\n")
	profiles, diags, _ := LoadProfiles(dir)
	if len(diags) != 0 || len(profiles) != 1 {
		t.Fatalf("profiles=%v diags=%v", profiles, diags)
	}
	if profiles[0].Senders != SendersOwners || profiles[0].Name != "p" {
		t.Fatalf("unexpected: %+v", profiles[0])
	}
}

func TestLoadProfilesRejectsUnsafeToolsForPublic(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "pub.md", "---\nchannel: discord\nthreads: [1]\nsenders: any\ntools: [web_search, bash, read]\n---\nx\n")
	profiles, diags, _ := LoadProfiles(dir)
	if len(profiles) != 0 {
		t.Fatalf("expected profile rejected, got %v", profiles)
	}
	joined := strings.Join(diags, "\n")
	if !strings.Contains(joined, `"bash"`) || !strings.Contains(joined, `"read"`) {
		t.Fatalf("diagnostics missing unsafe tools: %v", diags)
	}
}

func TestLoadProfilesRejectsWildcardForPublic(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "pub.md", "---\nchannel: discord\nthreads: [1]\nsenders: any\ntools: [*]\n---\nx\n")
	profiles, _, _ := LoadProfiles(dir)
	if len(profiles) != 0 {
		t.Fatalf("expected wildcard rejected for public profile")
	}
}

func TestLoadProfilesValidation(t *testing.T) {
	cases := map[string]string{
		"no channel":    "---\nthreads: [1]\ntools: [memory]\n---\nx\n",
		"no threads":    "---\nchannel: discord\ntools: [memory]\n---\nx\n",
		"bad senders":   "---\nchannel: discord\nthreads: [1]\nsenders: some\ntools: [memory]\n---\nx\n",
		"bad channel":   "---\nchannel: slack\nthreads: [g]\ntools: [memory]\n---\nx\n",
		"no prompt":     "---\nchannel: discord\nthreads: [1]\ntools: [memory]\n---\n",
		"reserved name": "---\nname: main\nchannel: discord\nthreads: [1]\ntools: [memory]\n---\nx\n",
	}
	for label, content := range cases {
		dir := t.TempDir()
		writeProfile(t, dir, "p.md", content)
		profiles, diags, err := LoadProfiles(dir)
		if err != nil {
			t.Fatalf("%s: err %v", label, err)
		}
		if len(profiles) != 0 || len(diags) == 0 {
			t.Errorf("%s: want rejection, got profiles=%v diags=%v", label, profiles, diags)
		}
	}
}

func TestLoadProfilesRejectsDuplicateThread(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "a.md", "---\nchannel: discord\nthreads: [1]\ntools: [memory]\n---\nx\n")
	writeProfile(t, dir, "b.md", "---\nchannel: discord\nthreads: [1]\ntools: [memory]\n---\nx\n")
	profiles, diags, _ := LoadProfiles(dir)
	if len(profiles) != 1 || profiles[0].Name != "a" || len(diags) != 1 {
		t.Fatalf("profiles=%v diags=%v", profiles, diags)
	}
}
