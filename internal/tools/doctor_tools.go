package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/s-ramirez/kroos-core/internal/config"
	"github.com/s-ramirez/kroos-core/internal/health"
)

// KroosConfigTool lets the doctor inspect and change kroos's own settings in
// $KROOS_HOME/config.json. Credentials (secret keys) are read-only: values
// are masked and set is refused, since the doctor can't know them.
type KroosConfigTool struct{ path string }

var kroosConfigDefinition = ToolDefinition{
	Name:        "kroos_config",
	Description: "Inspect or change kroos's own configuration ($KROOS_HOME/config.json). Secrets are masked and cannot be set.",
	Guidelines: []string{
		"Use kroos_config action=list to see every setting, its value (secrets masked) and where it comes from before changing anything.",
		"Config changes take effect after a process restart: call kroos_health action=restart_process after setting values.",
		"Never try to set credentials (tokens, passwords, API keys); tell the user exactly which key to set with `kroos init` or `kroos config set`.",
	},
	Parameters: JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"list", "get", "set", "unset"}},
			"key":    map[string]any{"type": "string", "description": "Dotted config key, e.g. discord.guild_tag (get/set/unset)."},
			"value":  map[string]any{"type": "string", "description": "New value (set)."},
		},
		"required": []string{"action"},
	},
}

// KroosConfigParams is the parameter struct for kroos_config.
type KroosConfigParams struct {
	Action string `json:"action"`
	Key    string `json:"key"`
	Value  string `json:"value"`
}

func NewKroosConfigTool(path string) *KroosConfigTool { return &KroosConfigTool{path: path} }

func (t *KroosConfigTool) Definition() ToolDefinition { return kroosConfigDefinition }

func (t *KroosConfigTool) Execute(_ context.Context, raw json.RawMessage) (ToolResult, error) {
	var p KroosConfigParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return errorResult("invalid JSON arguments"), nil
	}
	file, err := config.Load(t.path)
	if err != nil {
		return errorResultf("read config: %v", err), nil
	}
	if p.Action == "list" {
		var b strings.Builder
		for _, k := range config.Keys {
			v, src := describeConfigValue(k, file)
			if v == "" {
				continue
			}
			fmt.Fprintf(&b, "%s = %s (%s)\n", k.Name, v, src)
		}
		if b.Len() == 0 {
			return ToolResult{Output: "no settings configured"}, nil
		}
		return ToolResult{Output: b.String()}, nil
	}

	k, ok := config.Lookup(p.Key)
	if !ok {
		return errorResultf("unknown key %q (see action=list)", p.Key), nil
	}
	switch p.Action {
	case "get":
		v, src := describeConfigValue(k, file)
		if v == "" {
			return ToolResult{Output: k.Name + " is not set"}, nil
		}
		return ToolResult{Output: fmt.Sprintf("%s = %s (%s)", k.Name, v, src)}, nil
	case "set", "unset":
		if k.Secret {
			return errorResultf("%s is a credential; ask the user to set it with `kroos init` or `kroos config set %s <value>`", k.Name, k.Name), nil
		}
		if p.Action == "set" {
			if p.Value == "" {
				return errorResult("value is required (use unset to clear)"), nil
			}
			file[k.Name] = p.Value
		} else {
			delete(file, k.Name)
		}
		if err := config.Save(t.path, file); err != nil {
			return errorResultf("write config: %v", err), nil
		}
		msg := fmt.Sprintf("%s %s in %s; restart the process to apply", k.Name, map[string]string{"set": "set", "unset": "cleared"}[p.Action], t.path)
		if envVal, ok := os.LookupEnv(k.Env); ok && envVal != file[k.Name] {
			msg += fmt.Sprintf(". Warning: %s is also set in the environment/.env and takes precedence", k.Env)
		}
		return ToolResult{Output: msg}, nil
	}
	return errorResultf("unknown action %q", p.Action), nil
}

// describeConfigValue returns the effective value (masked if secret) and its source.
func describeConfigValue(k config.Key, file map[string]string) (string, string) {
	v, src := "", ""
	if fv, ok := file[k.Name]; ok {
		v, src = fv, "config.json"
	}
	if ev, ok := os.LookupEnv(k.Env); ok && ev != v {
		v, src = ev, "env/.env"
	}
	if v == "" && k.Default != "" {
		v, src = k.Default, "default"
	}
	if k.Secret && v != "" {
		v = config.Mask(v)
	}
	return v, src
}

// KroosHealthTool exposes failing components and restarts to the doctor.
type KroosHealthTool struct {
	monitor *health.Monitor
	service bool // running under a supervisor (launchd) that restarts the process
}

var kroosHealthDefinition = ToolDefinition{
	Name:        "kroos_health",
	Description: "Show kroos's failing components (channels, providers) and restart a component or the whole process.",
	Guidelines: []string{
		"Start every investigation with kroos_health action=status.",
		"After fixing a channel's cause, use action=restart with its component (e.g. channel:discord) to retry immediately.",
		"Use action=restart_process only after config changes; it restarts kroos after your report is delivered.",
	},
	Parameters: JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"action":    map[string]any{"type": "string", "enum": []string{"status", "restart", "restart_process"}},
			"component": map[string]any{"type": "string", "description": "Component to restart, e.g. channel:telegram (restart)."},
		},
		"required": []string{"action"},
	},
}

// KroosHealthParams is the parameter struct for kroos_health.
type KroosHealthParams struct {
	Action    string `json:"action"`
	Component string `json:"component"`
}

func NewKroosHealthTool(m *health.Monitor, service bool) *KroosHealthTool {
	return &KroosHealthTool{monitor: m, service: service}
}

func (t *KroosHealthTool) Definition() ToolDefinition { return kroosHealthDefinition }

func (t *KroosHealthTool) Execute(_ context.Context, raw json.RawMessage) (ToolResult, error) {
	var p KroosHealthParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return errorResult("invalid JSON arguments"), nil
	}
	switch p.Action {
	case "status":
		var b strings.Builder
		failing := t.monitor.Failing()
		if len(failing) == 0 {
			b.WriteString("all components healthy\n")
		}
		for _, inc := range failing {
			state := "failing"
			if t.monitor.IsOpen(inc.Component) {
				state = "incident"
			}
			fmt.Fprintf(&b, "%s [%s] %d failures since %s, last %s: %s\n", inc.Component, state, inc.Count,
				inc.FirstSeen.Format(time.RFC3339), inc.LastSeen.Format(time.RFC3339), inc.Error)
		}
		fmt.Fprintf(&b, "restartable: %s\n", strings.Join(t.monitor.Restartable(), ", "))
		fmt.Fprintf(&b, "process restart available: %v\n", t.service)
		return ToolResult{Output: b.String()}, nil
	case "restart":
		if err := t.monitor.Restart(p.Component); err != nil {
			return errorResult(err.Error()), nil
		}
		return ToolResult{Output: p.Component + " restarting now; check status again in a few seconds"}, nil
	case "restart_process":
		if !t.service {
			return errorResult("not running as a service (no supervisor to restart kroos); tell the user to restart it"), nil
		}
		t.monitor.RequestProcessRestart()
		return ToolResult{Output: "kroos will restart after your report is delivered"}, nil
	}
	return errorResultf("unknown action %q", p.Action), nil
}
