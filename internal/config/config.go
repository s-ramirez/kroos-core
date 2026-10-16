// Package config maps dotted configuration keys (e.g. "heartbeat.interval")
// to the KROOS_* environment variables the runtime reads, and persists them
// in JSON config files. Precedence, highest first: CLI flag, process env,
// .env, project config (.kroos/config.json), global config (~/.kroos/config.json).
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Key describes one configurable setting.
type Key struct {
	Name    string // dotted key, also the CLI flag name
	Env     string // environment variable the runtime reads
	Default string // documented default ("" = unset)
	Desc    string
	Secret  bool // masked in show/get
}

// Keys is the registry of every configurable setting.
var Keys = []Key{
	{"model", "KROOS_MODEL", "gpt-5.4", "Model name", false},
	{"base_url", "KROOS_BASE_URL", "https://api.openai.com", "OpenAI-compatible API endpoint", false},
	{"api_key", "KROOS_API_KEY", "", "API key for the LLM endpoint", true},
	{"llm.timeout", "KROOS_LLM_TIMEOUT", "5m", "LLM request timeout", false},
	{"llm.subagent_timeout", "KROOS_SUBAGENT_LLM_TIMEOUT", "", "Subagent LLM timeout (default: llm.timeout)", false},
	{"log_dir", "KROOS_LOG_DIR", "", "Directory for LLM request/response logs", false},
	{"webui.port", "KROOS_WEBUI_PORT", "", "Enable web UI plugin on this port", false},
	{"attachment_dir", "KROOS_ATTACHMENT_DIR", "", "Where channels save inbound media (default $TMPDIR/kroos-attachments)", false},
	{"image_input", "KROOS_IMAGE_INPUT", "true", "Inline image attachments as vision input", false},
	{"web_search.brave_api_key", "KROOS_BRAVE_API_KEY", "", "Brave Search API key", true},

	{"heartbeat.interval", "KROOS_HEARTBEAT_INTERVAL", "", "Heartbeat interval (Go duration); empty disables", false},
	{"heartbeat.target", "KROOS_HEARTBEAT_TARGET", "cli", "cli | imessage | discord | telegram | none", false},
	{"heartbeat.to", "KROOS_HEARTBEAT_TO", "", "Recipient handle/channel ID for non-CLI targets", false},
	{"heartbeat.model", "KROOS_HEARTBEAT_MODEL", "", "Model override for heartbeat turns", false},
	{"heartbeat.prompt", "KROOS_HEARTBEAT_PROMPT", "", "Custom heartbeat prompt", false},
	{"heartbeat.active_hours_start", "KROOS_HEARTBEAT_ACTIVE_HOURS_START", "", "HH:MM active window start", false},
	{"heartbeat.active_hours_end", "KROOS_HEARTBEAT_ACTIVE_HOURS_END", "", "HH:MM active window end", false},
	{"heartbeat.timezone", "KROOS_HEARTBEAT_TIMEZONE", "", "IANA timezone (empty = local)", false},

	{"imessage.enabled", "KROOS_IMESSAGE_ENABLED", "true", "Enable iMessage channel when creds set", false},
	{"imessage.server_url", "KROOS_IMESSAGE_SERVER_URL", "", "BlueBubbles server URL", false},
	{"imessage.password", "KROOS_IMESSAGE_PASSWORD", "", "BlueBubbles password", true},
	{"imessage.webhook_host", "KROOS_IMESSAGE_WEBHOOK_HOST", "127.0.0.1", "Webhook bind host", false},
	{"imessage.webhook_port", "KROOS_IMESSAGE_WEBHOOK_PORT", "8421", "Webhook bind port", false},
	{"imessage.webhook_path", "KROOS_IMESSAGE_WEBHOOK_PATH", "/bluebubbles", "Webhook path", false},
	{"imessage.webhook_url", "KROOS_IMESSAGE_WEBHOOK_URL", "", "URL BlueBubbles calls (default derived)", false},
	{"imessage.register_webhook", "KROOS_IMESSAGE_REGISTER_WEBHOOK", "true", "Register webhook with BlueBubbles", false},
	{"imessage.owner_handles", "KROOS_IMESSAGE_OWNER_HANDLES", "", "Comma-separated owner handles", false},
	{"imessage.group_tag", "KROOS_IMESSAGE_GROUP_TAG", "", "Tag required in group chats", false},
	{"imessage.read_receipts", "KROOS_IMESSAGE_READ_RECEIPTS", "true", "Read receipts + typing indicator", false},

	{"discord.enabled", "KROOS_DISCORD_ENABLED", "true", "Enable Discord channel when token set", false},
	{"discord.token", "KROOS_DISCORD_TOKEN", "", "Discord bot token", true},
	{"discord.owner_ids", "KROOS_DISCORD_OWNER_IDS", "", "Comma-separated owner user IDs", false},
	{"discord.guild_tag", "KROOS_DISCORD_GUILD_TAG", "", "Tag required in guild channels unless @mentioned", false},

	{"telegram.enabled", "KROOS_TELEGRAM_ENABLED", "true", "Enable Telegram channel when token set", false},
	{"telegram.token", "KROOS_TELEGRAM_TOKEN", "", "BotFather token", true},
	{"telegram.owner_ids", "KROOS_TELEGRAM_OWNER_IDS", "", "Comma-separated owner user IDs", false},
	{"telegram.group_tag", "KROOS_TELEGRAM_GROUP_TAG", "", "Extra group trigger tag", false},
}

// Lookup returns the key with the given dotted name or env var name.
func Lookup(name string) (Key, bool) {
	for _, k := range Keys {
		if k.Name == name || k.Env == name {
			return k, true
		}
	}
	return Key{}, false
}

// GlobalPath is ~/.kroos/config.json.
func GlobalPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kroos", "config.json")
}

// ProjectPath is <cwd>/.kroos/config.json.
func ProjectPath() string {
	cwd, _ := os.Getwd()
	return filepath.Join(cwd, ".kroos", "config.json")
}

// Load reads a config file. A missing file yields an empty map.
func Load(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

// Save writes m to path (0600, since it may hold secrets).
func Save(path string, m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// Apply loads each file in order and exports its values as env vars, never
// overriding a var already set (so earlier files and the real env win).
func Apply(paths ...string) error {
	for _, p := range paths {
		m, err := Load(p)
		if err != nil {
			return err
		}
		for name, v := range m {
			k, ok := Lookup(name)
			if !ok {
				fmt.Fprintf(os.Stderr, "warning: %s: unknown config key %q\n", p, name)
				continue
			}
			if _, set := os.LookupEnv(k.Env); !set {
				os.Setenv(k.Env, v)
			}
		}
	}
	return nil
}

// Mask hides all but the last 4 chars of a secret.
func Mask(v string) string {
	if len(v) <= 4 {
		return strings.Repeat("*", len(v))
	}
	return strings.Repeat("*", 8) + v[len(v)-4:]
}

// Names returns all key names sorted.
func Names() []string {
	out := make([]string, len(Keys))
	for i, k := range Keys {
		out[i] = k.Name
	}
	sort.Strings(out)
	return out
}
