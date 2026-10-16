package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/config"
	"github.com/s-ramirez/kroos-core/internal/llm"
)

func testWizard(t *testing.T, input string) (*wizard, *strings.Builder, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	var out strings.Builder
	w, err := newWizard(strings.NewReader(input), &out, path)
	if err != nil {
		t.Fatal(err)
	}
	w.authPath = filepath.Join(dir, "auth.json")
	w.listModels = func(ctx context.Context, p *llm.Providers, provider string) ([]string, error) {
		return []string{"qwen", "gemma"}, nil
	}
	w.login = func(ctx context.Context, path string, _ io.Writer) error {
		return llmSaveFakeLogin(path)
	}
	return w, &out, path
}

// llmSaveFakeLogin writes a minimal token store so LoadChatGPTAuth succeeds.
func llmSaveFakeLogin(path string) error {
	return os.WriteFile(path, []byte(`{"chatgpt":{"access_token":"a","refresh_token":"r"}}`), 0o600)
}

func lines(l ...string) string { return strings.Join(l, "\n") + "\n" }

func TestWizardConfiguresProvidersAndChannel(t *testing.T) {
	w, out, path := testWizard(t, lines(
		"1",                      // Providers
		"2",                      // oMLX
		"http://studio:8000",     // url
		"secret-key",             // api key
		"3",                      // model: gemma (1=auto, 2=qwen, 3=gemma)
		"1",                      // ChatGPT
		"",                       // log in now? Y
		"1",                      // model: qwen
		"3",                      // default & fallback
		"2",                      // default provider: omlx
		"n",                      // fallback off
		"4",                      // back
		"2",                      // Channels
		"1",                      // Discord
		"",                       // enable? Y
		"tok", "123", "", "", "", // token, owners, tag, home, allow-all
		"4", // back
		"5", // done
	))
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(path)
	want := map[string]string{
		"omlx.url": "http://studio:8000", "omlx.api_key": "secret-key", "omlx.model": "gemma",
		"chatgpt.model": "qwen", "provider": "omlx", "fallback": "false",
		"discord.token": "tok", "discord.owner_ids": "123",
	}
	for k, v := range want {
		if cfg[k] != v {
			t.Errorf("%s = %q, want %q", k, cfg[k], v)
		}
	}
	if strings.Contains(out.String(), "secret-key") {
		t.Error("secret echoed")
	}
}

func TestWizardIsIdempotent(t *testing.T) {
	w, _, path := testWizard(t, lines("1", "2", "http://studio:8000", "k", "2", "4", "5"))
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	before, _ := config.Load(path)

	// Re-run: walk the same screens pressing Enter everywhere.
	w2, err := newWizard(strings.NewReader(lines("1", "2", "", "", "", "4", "5")), io.Discard, path)
	if err != nil {
		t.Fatal(err)
	}
	w2.authPath = w.authPath
	w2.listModels = w.listModels
	if err := w2.run(); err != nil {
		t.Fatal(err)
	}
	after, _ := config.Load(path)
	if len(before) != len(after) {
		t.Fatalf("config changed: %v -> %v", before, after)
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed: %q -> %q", k, v, after[k])
		}
	}
}

func TestWizardClearAndDisable(t *testing.T) {
	w, _, path := testWizard(t, lines(
		"4", "8420", "", "", // Extras: web UI port
		"2", "2", "", "tok", "", "", "", "", "", // Telegram: enable, token
		"2", "n", "4", // Telegram again: disable (keeps token)
		"4", "-", "", "", // Extras: clear port
		"5",
	))
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(path)
	if cfg["telegram.token"] != "tok" || cfg["telegram.enabled"] != "false" {
		t.Fatalf("telegram: %v", cfg)
	}
	if _, ok := cfg["webui.port"]; ok {
		t.Fatalf("webui.port not cleared: %v", cfg)
	}
}

func TestWizardEOFSavesAndExits(t *testing.T) {
	w, _, path := testWizard(t, lines("1", "2", "http://x:1"))
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(path)
	if cfg["omlx.url"] != "http://x:1" {
		t.Fatalf("not saved before EOF: %v", cfg)
	}
}
