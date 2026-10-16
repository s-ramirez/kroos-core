package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/s-ramirez/kroos-core/internal/channel"
)

const testBotID = 999

func newTestChannel(cfg Config) *Channel {
	c := New(cfg)
	c.botID, c.botUsername = testBotID, "KroosBot"
	return c
}

func tmsg(from int64, chatID int64, chatType, text string) *message {
	return &message{
		From: &user{ID: from, Username: fmt.Sprintf("u%d", from)},
		Chat: chat{ID: chatID, Type: chatType},
		Text: text,
	}
}

func TestAcceptGating(t *testing.T) {
	c := newTestChannel(Config{OwnerIDs: []string{"1"}, GroupTag: "#kroos", OpenChats: []string{"-100"}})

	reply := tmsg(2, -100, "supergroup", "and this?")
	reply.ReplyTo = &message{From: &user{ID: testBotID, IsBot: true}}

	cases := []struct {
		label    string
		m        *message
		wantText string
		wantDrop bool
	}{
		{"owner private", tmsg(1, 1, "private", "hi"), "hi", false},
		{"stranger private", tmsg(2, 2, "private", "hi"), "", true},
		{"owner group no mention", tmsg(1, -5, "group", "hi"), "", true},
		{"owner group mention", tmsg(1, -5, "group", "@kroosbot hi"), "hi", false},
		{"owner group tag", tmsg(1, -5, "group", "#kroos hi"), "hi", false},
		{"owner command suffix", tmsg(1, -5, "group", "/new@KroosBot"), "/new", false},
		{"stranger closed group", tmsg(2, -5, "group", "@KroosBot hi"), "", true},
		{"stranger open no mention", tmsg(2, -100, "supergroup", "hi"), "", true},
		{"stranger open mention", tmsg(2, -100, "supergroup", "@KroosBot hi"), "[u2]: hi", false},
		{"stranger open reply", reply, "[u2]: and this?", false},
		{"owner open mention", tmsg(1, -100, "supergroup", "@KroosBot hi"), "[u1 (owner)]: hi", false},
	}
	for _, tc := range cases {
		text, reason := c.accept(tc.m)
		if (reason != "") != tc.wantDrop {
			t.Errorf("%s: reason %q, wantDrop %v", tc.label, reason, tc.wantDrop)
		}
		if text != tc.wantText {
			t.Errorf("%s: text %q, want %q", tc.label, text, tc.wantText)
		}
	}
}

// fakeAPI serves getMe/getUpdates/sendMessage for Listen/Send tests.
type fakeAPI struct {
	mu      sync.Mutex
	updates []update
	sent    []map[string]any
}

func (f *fakeAPI) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/botTOKEN/") {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var params map[string]any
		_ = json.Unmarshal(body, &params)
		var result any
		switch strings.TrimPrefix(r.URL.Path, "/botTOKEN/") {
		case "getMe":
			result = user{ID: testBotID, IsBot: true, Username: "KroosBot"}
		case "getUpdates":
			f.mu.Lock()
			result, f.updates = f.updates, nil
			f.mu.Unlock()
			if result == nil {
				result = []update{}
				time.Sleep(20 * time.Millisecond)
			}
		case "sendMessage":
			f.mu.Lock()
			f.sent = append(f.sent, params)
			f.mu.Unlock()
			result = message{}
		default:
			t.Errorf("unexpected method %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}
}

func TestListenForwardsOwnerMessages(t *testing.T) {
	api := &fakeAPI{updates: []update{
		{UpdateID: 1, Message: tmsg(2, 2, "private", "stranger")},
		{UpdateID: 2, Message: tmsg(1, 1, "private", "hello")},
	}}
	srv := httptest.NewServer(api.handler(t))
	defer srv.Close()

	c := New(Config{Token: "TOKEN", OwnerIDs: []string{"1"}, APIBase: srv.URL, PollTimeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan channel.Inbound, 4)
	go func() { _ = c.Listen(ctx, out) }()

	select {
	case msg := <-out:
		if msg.Channel != "telegram" || msg.Thread != "1" || msg.Sender != "1" || msg.Content != "hello" {
			t.Fatalf("unexpected inbound: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for inbound")
	}
	select {
	case msg := <-out:
		t.Fatalf("unexpected extra inbound: %+v", msg)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSendChunksAndDropsMeta(t *testing.T) {
	api := &fakeAPI{}
	srv := httptest.NewServer(api.handler(t))
	defer srv.Close()
	c := New(Config{Token: "TOKEN", APIBase: srv.URL})

	long := strings.Repeat("é", 3000) // 6000 bytes
	if err := c.Send(context.Background(), channel.Outbound{Thread: "42", Content: "[tokens: 5]\n" + long}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(api.sent) != 2 {
		t.Fatalf("want 2 chunks, got %d", len(api.sent))
	}
	var joined string
	for _, p := range api.sent {
		if p["chat_id"] != "42" {
			t.Errorf("chat_id = %v", p["chat_id"])
		}
		s := p["text"].(string)
		if !utf8.ValidString(s) || len(s) > maxMessageLen {
			t.Errorf("bad chunk (len %d, valid %v)", len(s), utf8.ValidString(s))
		}
		joined += s
	}
	if joined != long {
		t.Error("chunks don't reassemble to original (meta line should be dropped)")
	}
}

func TestSendSurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
	}))
	defer srv.Close()
	c := New(Config{Token: "TOKEN", APIBase: srv.URL})
	err := c.Send(context.Background(), channel.Outbound{Thread: "1", Content: "x"})
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("want chat not found error, got %v", err)
	}
	if strings.Contains(err.Error(), "TOKEN") {
		t.Fatalf("error leaks token: %v", err)
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{"TELEGRAM_BOT_TOKEN": "t", "TELEGRAM_ALLOWED_USERS": "1, 2"}
	getenv := func(k string) string { return env[k] }
	parseBool := func(string, bool) bool { return true }
	cfg, _, err := ConfigFromEnv(getenv, parseBool)
	if err != nil || cfg == nil || len(cfg.OwnerIDs) != 2 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	env["TELEGRAM_ALLOWED_USERS"] = "@alice"
	if _, _, err := ConfigFromEnv(getenv, parseBool); err == nil {
		t.Fatal("want error for non-numeric owner ID")
	}
	delete(env, "TELEGRAM_BOT_TOKEN")
	if cfg, _, _ := ConfigFromEnv(getenv, parseBool); cfg != nil {
		t.Fatal("want nil config without token")
	}
}

func TestAllowAllAcceptsStrangers(t *testing.T) {
	c := newTestChannel(Config{OwnerIDs: []string{"1"}, AllowAll: true})
	if text, reason := c.accept(tmsg(2, 2, "private", "hi")); reason != "" || text != "hi" {
		t.Errorf("private: %q %q", text, reason)
	}
	if _, reason := c.accept(tmsg(2, -5, "group", "hi")); reason == "" {
		t.Error("group without mention should still drop")
	}
}

func TestHomeTopic(t *testing.T) {
	api := &fakeAPI{}
	srv := httptest.NewServer(api.handler(t))
	defer srv.Close()
	c := New(Config{Token: "TOKEN", APIBase: srv.URL, HomeChannel: "-100", HomeThreadID: "7"})
	if c.Home() != "-100:7" {
		t.Fatalf("Home = %q", c.Home())
	}
	if err := c.Send(context.Background(), channel.Outbound{Thread: c.Home(), Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if p := api.sent[0]; p["chat_id"] != "-100" || p["message_thread_id"] != float64(7) {
		t.Errorf("sent %v", p)
	}
	if New(Config{HomeChannel: "-100", HomeThreadID: "1"}).Home() != "-100" {
		t.Error("General topic should omit thread id")
	}
}
