package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runSecret(t *testing.T, tool *SecretTool, p SecretParams) ToolResult {
	t.Helper()
	raw, _ := json.Marshal(p)
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestSecretSetListDelete(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", "# comment\nKROOS_MODEL=x\nOTHER=1\n")
	path := filepath.Join(dir, ".env")
	tool := NewSecretTool(path)
	t.Setenv("HOMESERVER_ANSIBLE_PASSWORD", "")

	res := runSecret(t, tool, SecretParams{Action: "set", Name: "HOMESERVER_ANSIBLE_PASSWORD", Value: "p#ss word"})
	if res.IsError || strings.Contains(res.Output, "p#ss") {
		t.Fatalf("set: %+v", res)
	}
	if os.Getenv("HOMESERVER_ANSIBLE_PASSWORD") != "p#ss word" {
		t.Error("env not exported")
	}
	runSecret(t, tool, SecretParams{Action: "set", Name: "HOMESERVER_ANSIBLE_PASSWORD", Value: "new"})
	data, _ := os.ReadFile(path)
	if got := string(data); got != "# comment\nKROOS_MODEL=x\nOTHER=1\nHOMESERVER_ANSIBLE_PASSWORD=new\n" {
		t.Errorf("file = %q", got)
	}
	if res := runSecret(t, tool, SecretParams{Action: "list"}); res.Output != "HOMESERVER_ANSIBLE_PASSWORD\nOTHER" {
		t.Errorf("list = %q", res.Output)
	}
	runSecret(t, tool, SecretParams{Action: "delete", Name: "HOMESERVER_ANSIBLE_PASSWORD"})
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "HOMESERVER") {
		t.Errorf("not deleted: %q", data)
	}
}

func TestSecretRejectsConfigKeys(t *testing.T) {
	tool := NewSecretTool(filepath.Join(t.TempDir(), ".env"))
	for _, n := range []string{"TELEGRAM_ALLOW_ALL_USERS", "KROOS_ANYTHING", "lower"} {
		if res := runSecret(t, tool, SecretParams{Action: "set", Name: n, Value: "v"}); !res.IsError {
			t.Errorf("%s accepted", n)
		}
	}
}
