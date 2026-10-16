// Package config maps dotted configuration keys (e.g. "heartbeat.interval")
// to the KROOS_* environment variables the runtime reads, and persists them
// in JSON config files. Precedence, highest first: CLI flag, process env,
// $KROOS_HOME/.env, $KROOS_HOME/config.json.
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
	{"model", "KROOS_MODEL", "", "Model for the selected provider (default: <provider>.model)", false},
	{"provider", "KROOS_PROVIDER", "chatgpt", "LLM provider: chatgpt | omlx", false},
	{"fallback", "KROOS_FALLBACK", "true", "Fall back to the other provider when one fails", false},
	{"chatgpt.model", "KROOS_CHATGPT_MODEL", "", "Default ChatGPT model (empty = first in your account's catalog)", false},
	{"omlx.model", "KROOS_OMLX_MODEL", "", "Default oMLX model (empty = first served)", false},
	{"omlx.url", "KROOS_OMLX_URL", "", "oMLX server URL (e.g. http://host:8000)", false},
	{"omlx.api_key", "KROOS_OMLX_API_KEY", "", "oMLX API key", true},
	{"llm.timeout", "KROOS_LLM_TIMEOUT", "5m", "LLM request timeout", false},
	{"llm.subagent_timeout", "KROOS_SUBAGENT_LLM_TIMEOUT", "", "Subagent LLM timeout (default: llm.timeout)", false},
	{"log_dir", "KROOS_LOG_DIR", "", "Directory for LLM request/response logs", false},
	{"webui.port", "KROOS_WEBUI_PORT", "", "Enable web UI plugin on this port", false},
	{"attachment_dir", "KROOS_ATTACHMENT_DIR", "", "Where channels save inbound media (default $TMPDIR/kroos-attachments)", false},
	{"image_input", "KROOS_IMAGE_INPUT", "true", "Inline image attachments as vision input", false},
	{"skills.curator_interval", "KROOS_SKILL_CURATOR_INTERVAL", "168h", "Interval between skill-curator runs; 0 disables", false},
	{"skills.stale_after", "KROOS_SKILL_STALE_AFTER", "720h", "Idle time before an agent-created skill is marked stale", false},
	{"skills.archive_after", "KROOS_SKILL_ARCHIVE_AFTER", "2160h", "Idle time before an agent-created skill is archived", false},
	{"skills.review_interval", "KROOS_SKILL_REVIEW_INTERVAL", "10", "Tool calls between background skill reviews; 0 disables", false},
	{"doctor.source_dir", "KROOS_SOURCE_DIR", "", "kroos code checkout the doctor may fix via PRs (default: checkout containing the binary)", false},
	{"doctor.log_file", "KROOS_LOG_FILE", "", "Process log the doctor inspects (set by deploy)", false},
	{"web_search.brave_api_key", "KROOS_BRAVE_API_KEY", "", "Brave Search API key", true},

	{"heartbeat.interval", "KROOS_HEARTBEAT_INTERVAL", "", "Heartbeat interval (Go duration); empty disables", false},
	{"heartbeat.target", "KROOS_HEARTBEAT_TARGET", "cli", "cli | imessage | discord | telegram | none", false},
	{"heartbeat.to", "KROOS_HEARTBEAT_TO", "", "Recipient handle/channel ID for non-CLI targets", false},
	{"heartbeat.model", "KROOS_HEARTBEAT_MODEL", "", "Model override for heartbeat turns", false},
	{"heartbeat.prompt", "KROOS_HEARTBEAT_PROMPT", "", "Custom heartbeat prompt", false},
	{"heartbeat.active_hours_start", "KROOS_HEARTBEAT_ACTIVE_HOURS_START", "", "HH:MM active window start", false},
	{"heartbeat.active_hours_end", "KROOS_HEARTBEAT_ACTIVE_HOURS_END", "", "HH:MM active window end", false},
	{"heartbeat.timezone", "KROOS_HEARTBEAT_TIMEZONE", "", "IANA timezone (empty = local)", false},

	{"imessage.enabled", "BLUEBUBBLES_ENABLED", "true", "Enable iMessage channel when creds set", false},
	{"imessage.server_url", "BLUEBUBBLES_SERVER_URL", "", "BlueBubbles server URL", false},
	{"imessage.password", "BLUEBUBBLES_PASSWORD", "", "BlueBubbles password", true},
	{"imessage.webhook_host", "BLUEBUBBLES_WEBHOOK_HOST", "127.0.0.1", "Webhook bind host", false},
	{"imessage.webhook_port", "BLUEBUBBLES_WEBHOOK_PORT", "8421", "Webhook bind port", false},
	{"imessage.webhook_path", "BLUEBUBBLES_WEBHOOK_PATH", "/bluebubbles", "Webhook path", false},
	{"imessage.webhook_url", "BLUEBUBBLES_WEBHOOK_URL", "", "URL BlueBubbles calls (default derived)", false},
	{"imessage.register_webhook", "BLUEBUBBLES_REGISTER_WEBHOOK", "true", "Register webhook with BlueBubbles", false},
	{"imessage.owner_handles", "BLUEBUBBLES_ALLOWED_USERS", "", "Comma-separated owner handles", false},
	{"imessage.group_tag", "BLUEBUBBLES_GROUP_TAG", "", "Tag required in group chats", false},
	{"imessage.read_receipts", "BLUEBUBBLES_READ_RECEIPTS", "true", "Read receipts + typing indicator", false},
	{"imessage.home_channel", "BLUEBUBBLES_HOME_CHANNEL", "", "Default chat GUID for deliveries with no recipient", false},

	{"discord.enabled", "DISCORD_ENABLED", "true", "Enable Discord channel when token set", false},
	{"discord.token", "DISCORD_BOT_TOKEN", "", "Discord bot token", true},
	{"discord.owner_ids", "DISCORD_ALLOWED_USERS", "", "Comma-separated owner user IDs", false},
	{"discord.guild_tag", "DISCORD_GUILD_TAG", "", "Tag required in guild channels unless @mentioned", false},
	{"discord.allow_all_users", "DISCORD_ALLOW_ALL_USERS", "false", "Let any user address the bot (not just allowed users)", false},
	{"discord.home_channel", "DISCORD_HOME_CHANNEL", "", "Default channel ID for deliveries with no recipient", false},

	{"telegram.enabled", "TELEGRAM_ENABLED", "true", "Enable Telegram channel when token set", false},
	{"telegram.token", "TELEGRAM_BOT_TOKEN", "", "BotFather token", true},
	{"telegram.owner_ids", "TELEGRAM_ALLOWED_USERS", "", "Comma-separated owner user IDs", false},
	{"telegram.group_tag", "TELEGRAM_GROUP_TAG", "", "Extra group trigger tag", false},
	{"telegram.allow_all_users", "TELEGRAM_ALLOW_ALL_USERS", "false", "Let any user address the bot (not just allowed users)", false},
	{"telegram.home_channel", "TELEGRAM_HOME_CHANNEL", "", "Default chat ID for deliveries with no recipient", false},
	{"telegram.home_channel_thread_id", "TELEGRAM_HOME_CHANNEL_THREAD_ID", "", "Forum topic ID within the home chat", false},
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

// Home is the instance directory holding all per-instance state (config,
// auth, .env, agent files, skills, memory, conversations, tasks):
// $KROOS_HOME, default ~/.kroos. It is read from the real environment only.
func Home() string {
	if h := strings.TrimSpace(os.Getenv("KROOS_HOME")); h != "" {
		if abs, err := filepath.Abs(h); err == nil {
			return abs
		}
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kroos")
}

// GlobalPath is $KROOS_HOME/config.json.
func GlobalPath() string { return filepath.Join(Home(), "config.json") }

// EnvPath is $KROOS_HOME/.env.
func EnvPath() string { return filepath.Join(Home(), ".env") }

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
