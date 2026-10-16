package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestIMessageSendResolvesOrCreatesChat(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var textChat string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("password") != "p&w" {
			http.Error(w, "unauthorized", 401)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, r.URL.Path)
		var data any
		switch r.URL.Path {
		case "/api/v1/chat/query":
			data = []map[string]string{{"guid": "iMessage;-;a@b.com", "chatIdentifier": "a@b.com"}}
		case "/api/v1/message/text":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			textChat, _ = body["chatGuid"].(string)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "data": data})
	}))
	defer srv.Close()
	tool := NewIMessageSendTool(IMessageSendConfig{ServerURL: srv.URL, Password: "p&w"})

	raw, _ := json.Marshal(IMessageSendParams{To: "A@B.com", Message: "**hi**"})
	if res, _ := tool.Execute(context.Background(), raw); res.IsError {
		t.Fatalf("existing chat: %s", res.Output)
	}
	if textChat != "iMessage;-;a@b.com" {
		t.Errorf("sent to %q", textChat)
	}

	paths = nil
	raw, _ = json.Marshal(IMessageSendParams{To: "+15550001111", Message: "hi"})
	if res, _ := tool.Execute(context.Background(), raw); res.IsError {
		t.Fatalf("new chat: %s", res.Output)
	}
	if len(paths) != 2 || paths[1] != "/api/v1/chat/new" {
		t.Errorf("paths = %v", paths)
	}
}
