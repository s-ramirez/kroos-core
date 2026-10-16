package tools

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/s-ramirez/kroos-core/internal/config"
	"github.com/s-ramirez/kroos-core/internal/health"
)

func runTool(t *testing.T, tool Tool, params any) ToolResult {
	t.Helper()
	raw, _ := json.Marshal(params)
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestKroosConfigToolSetsButNeverSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	config.Save(path, map[string]string{"discord.token": "supersecrettoken", "discord.guild_tag": "@old"})
	tool := NewKroosConfigTool(path)

	list := runTool(t, tool, KroosConfigParams{Action: "list"})
	if strings.Contains(list.Output, "supersecrettoken") || !strings.Contains(list.Output, "discord.guild_tag = @old") {
		t.Fatalf("list: %s", list.Output)
	}
	if res := runTool(t, tool, KroosConfigParams{Action: "set", Key: "discord.guild_tag", Value: "@kroos"}); res.IsError {
		t.Fatalf("set: %s", res.Output)
	}
	if res := runTool(t, tool, KroosConfigParams{Action: "set", Key: "discord.token", Value: "x"}); !res.IsError {
		t.Fatal("secret set allowed")
	}
	cfg, _ := config.Load(path)
	if cfg["discord.guild_tag"] != "@kroos" || cfg["discord.token"] != "supersecrettoken" {
		t.Fatalf("config: %v", cfg)
	}
}

func TestKroosHealthTool(t *testing.T) {
	m := health.NewMonitor("")
	m.Report("channel:telegram", errors.New("getMe: 401"))
	kicked := false
	m.SetRestart("channel:telegram", func() { kicked = true })

	tool := NewKroosHealthTool(m, false)
	if st := runTool(t, tool, KroosHealthParams{Action: "status"}); !strings.Contains(st.Output, "getMe: 401") {
		t.Fatalf("status: %s", st.Output)
	}
	if res := runTool(t, tool, KroosHealthParams{Action: "restart", Component: "channel:telegram"}); res.IsError || !kicked {
		t.Fatalf("restart: %s", res.Output)
	}
	if res := runTool(t, tool, KroosHealthParams{Action: "restart_process"}); !res.IsError {
		t.Fatal("process restart allowed outside a service")
	}
	if res := runTool(t, NewKroosHealthTool(m, true), KroosHealthParams{Action: "restart_process"}); res.IsError || !m.TakeProcessRestart() {
		t.Fatalf("service restart: %s", res.Output)
	}
}

func TestBashDefaultTimeoutKillsHangingCommand(t *testing.T) {
	tool := NewBashToolWithTimeout(200 * time.Millisecond)
	start := time.Now()
	res := runTool(t, tool, BashParams{Command: "sleep 30"})
	if time.Since(start) > 5*time.Second {
		t.Fatalf("not killed (took %s): %s", time.Since(start), res.Output)
	}
}
