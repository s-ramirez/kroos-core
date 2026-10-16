package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramSendToolPostsMessage(t *testing.T) {
	var gotPath string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()

	tool := NewTelegramSendTool(TelegramSendConfig{Token: "TOK", APIBase: srv.URL})
	input, _ := json.Marshal(TelegramSendParams{To: "-100", Message: "hello"})
	res, err := tool.Execute(context.Background(), input)
	if err != nil || res.IsError {
		t.Fatalf("Execute: res=%+v err=%v", res, err)
	}
	if gotPath != "/botTOK/sendMessage" || got["chat_id"] != "-100" || got["text"] != "hello" {
		t.Fatalf("unexpected request path=%s body=%v", gotPath, got)
	}
}

func TestTelegramSendToolValidates(t *testing.T) {
	tool := NewTelegramSendTool(TelegramSendConfig{Token: "TOK"})
	for _, p := range []TelegramSendParams{{Message: "x"}, {To: "1"}} {
		input, _ := json.Marshal(p)
		res, _ := tool.Execute(context.Background(), input)
		if !res.IsError || !strings.Contains(res.Output, "required") {
			t.Errorf("params %+v: want required error, got %+v", p, res)
		}
	}
}
