package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/s-ramirez/kroos-core/internal/config"
)

// SecretTool stores credentials the user hands the agent (e.g. an Ansible
// become password) in the .env file and the process environment, so later
// bash commands can read them as $NAME. Values are never echoed back.
type SecretTool struct {
	path string
	mu   sync.Mutex
}

var secretNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

var secretDefinition = ToolDefinition{
	Name:        "secret",
	Description: "Store, list, or delete secrets (passwords, tokens, API keys) persisted in .env and exported as environment variables.",
	Guidelines: []string{
		"When the user gives you a password, token, or key to keep for later, store it with secret action=set under a descriptive UPPER_SNAKE name (e.g. HOMESERVER_ANSIBLE_PASSWORD) instead of writing it to memory or files.",
		"Stored secrets are available to bash as environment variables: reference them as \"$NAME\" (e.g. ansible-playbook --become-password-file <(printf %s \"$NAME\")); never echo, print, or paste their values into replies.",
		"Use secret action=list to see which secrets exist before asking the user for one again.",
	},
	Parameters: JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"set", "list", "delete"},
			},
			"name": map[string]any{
				"type":        "string",
				"description": "UPPER_SNAKE_CASE variable name (set/delete).",
			},
			"value": map[string]any{
				"type":        "string",
				"description": "Secret value (set).",
			},
		},
		"required": []string{"action"},
	},
}

// SecretParams is the parameter struct for secret.
type SecretParams struct {
	Action string `json:"action"`
	Name   string `json:"name"`
	Value  string `json:"value"`
}

// NewSecretTool returns a SecretTool persisting to the .env file at path.
func NewSecretTool(path string) *SecretTool {
	return &SecretTool{path: path}
}

func (t *SecretTool) Definition() ToolDefinition { return secretDefinition }

func (t *SecretTool) Execute(_ context.Context, raw json.RawMessage) (ToolResult, error) {
	var params SecretParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return errorResult("invalid JSON arguments"), nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if params.Action == "list" {
		names, err := t.names()
		if err != nil {
			return errorResultf("read %s: %v", t.path, err), nil
		}
		if len(names) == 0 {
			return ToolResult{Output: "No secrets stored."}, nil
		}
		return ToolResult{Output: strings.Join(names, "\n")}, nil
	}

	name := strings.TrimSpace(params.Name)
	if !secretNameRe.MatchString(name) {
		return errorResult("name must be UPPER_SNAKE_CASE (A-Z, 0-9, _), max 64 chars"), nil
	}
	if _, ok := config.Lookup(name); ok || strings.HasPrefix(name, "KROOS_") {
		return errorResultf("%s is a kroos setting; change it with `kroos config set`, not secret", name), nil
	}

	switch params.Action {
	case "set":
		if params.Value == "" {
			return errorResult("value is required for set"), nil
		}
		if strings.ContainsAny(params.Value, "\r\n") {
			return errorResult("value must be a single line"), nil
		}
		if err := t.rewrite(name, params.Value, true); err != nil {
			return errorResultf("write %s: %v", t.path, err), nil
		}
		os.Setenv(name, params.Value)
		return ToolResult{Output: fmt.Sprintf("Stored %s; available to bash as $%s.", name, name)}, nil
	case "delete":
		if err := t.rewrite(name, "", false); err != nil {
			return errorResultf("write %s: %v", t.path, err), nil
		}
		os.Unsetenv(name)
		return ToolResult{Output: fmt.Sprintf("Deleted %s.", name)}, nil
	}
	return errorResultf("unknown action %q", params.Action), nil
}

// names lists the non-config variables defined in the .env file.
func (t *SecretTool) names() ([]string, error) {
	lines, err := t.lines()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range lines {
		k := envLineKey(l)
		if k == "" || strings.HasPrefix(k, "KROOS_") {
			continue
		}
		if _, ok := config.Lookup(k); !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

// rewrite replaces (or removes, when keep is false) name's line in the .env
// file, appending it when absent. Written 0600.
func (t *SecretTool) rewrite(name, value string, keep bool) error {
	lines, err := t.lines()
	if err != nil {
		return err
	}
	entry := name + "=" + quoteEnv(value)
	out := make([]string, 0, len(lines)+1)
	found := false
	for _, l := range lines {
		if envLineKey(l) == name {
			if keep && !found {
				out = append(out, entry)
			}
			found = true
			continue
		}
		out = append(out, l)
	}
	if keep && !found {
		out = append(out, entry)
	}
	data := strings.Join(out, "\n")
	if data != "" {
		data += "\n"
	}
	if err := os.WriteFile(t.path, []byte(data), 0o600); err != nil {
		return err
	}
	return os.Chmod(t.path, 0o600)
}

func (t *SecretTool) lines() ([]string, error) {
	data, err := os.ReadFile(t.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n"), nil
}

// envLineKey returns the variable a .env line assigns, or "".
func envLineKey(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	k, _, ok := strings.Cut(line, "=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(k)
}

// quoteEnv single-quotes values the .env loader would otherwise mangle.
// The loader strips one pair of matching outer quotes, so values that are
// themselves quoted or padded stay intact.
func quoteEnv(v string) string {
	if v == strings.TrimSpace(v) && !strings.ContainsAny(v, `"'#`) {
		return v
	}
	return "'" + v + "'"
}
