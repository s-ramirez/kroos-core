package imessage

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/s-ramirez/kroos-core/internal/channel"
)

// fakeBB is a minimal BlueBubbles server recording requests.
type fakeBB struct {
	mu       sync.Mutex
	calls    []string // "METHOD path"
	sent     []string // message texts
	webhooks []map[string]any
	texts    []map[string]any // full /message/text payloads
	uploads  []string         // uploaded attachment names
	created  []string         // /chat/new addresses
	chats    []map[string]string
	password string
	private  bool
}

func newFakeBB(t *testing.T, password string) (*fakeBB, *httptest.Server) {
	f := &fakeBB{password: password}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("password") != f.password {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.EscapedPath())
		reply := func(data any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "data": data})
		}
		switch {
		case r.URL.Path == "/api/v1/ping":
			reply("pong")
		case r.URL.Path == "/api/v1/server/info":
			reply(map[string]any{"private_api": f.private, "helper_connected": f.private})
		case r.URL.Path == "/api/v1/webhook" && r.Method == http.MethodGet:
			reply(f.webhooks)
		case r.URL.Path == "/api/v1/webhook" && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			body["id"] = len(f.webhooks) + 1
			f.webhooks = append(f.webhooks, body)
			reply(body)
		case strings.HasPrefix(r.URL.Path, "/api/v1/webhook/") && r.Method == http.MethodDelete:
			f.webhooks = nil
			reply(nil)
		case r.URL.Path == "/api/v1/message/text":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.texts = append(f.texts, body)
			msg, _ := body["message"].(string)
			f.sent = append(f.sent, msg)
			reply(map[string]string{"guid": "x"})
		case r.URL.Path == "/api/v1/message/attachment":
			if err := r.ParseMultipartForm(1 << 20); err != nil || r.FormValue("chatGuid") == "" {
				http.Error(w, "bad form", 400)
				return
			}
			fh, _ := r.MultipartForm.File["attachment"][0].Open()
			b, _ := io.ReadAll(fh)
			f.uploads = append(f.uploads, r.FormValue("name")+":"+string(b))
			reply(nil)
		case r.URL.Path == "/api/v1/chat/query":
			reply(f.chats)
		case r.URL.Path == "/api/v1/chat/new":
			var body struct{ Addresses []string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.created = append(f.created, body.Addresses...)
			reply(nil)
		case strings.HasPrefix(r.URL.Path, "/api/v1/attachment/"):
			_, _ = io.WriteString(w, "IMAGEBYTES")
		default:
			reply(nil)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeBB) snapshot() (calls, sent []string, hooks int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...), append([]string(nil), f.sent...), len(f.webhooks)
}

func post(t *testing.T, h http.HandlerFunc, body []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.URL.RawQuery = "password=secret"
	h(httptest.NewRecorder(), req)
}

func TestSendEscapesPasswordAndSplitsParagraphs(t *testing.T) {
	f, srv := newFakeBB(t, "p&ss#word")
	ch := New(Config{ServerURL: srv.URL, Password: "p&ss#word"})

	err := ch.Send(context.Background(), channel.Outbound{
		Thread:  "iMessage;-;+15551234567",
		Content: "**Hello** there.\n\nSecond para.\n[tokens: 12]",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	_, sent, _ := f.snapshot()
	if len(sent) != 2 || sent[0] != "Hello there." || sent[1] != "Second para." {
		t.Fatalf("sent = %q", sent)
	}
}

func TestSplitMessageLong(t *testing.T) {
	long := strings.Repeat("word ", 2000) // 10000 chars
	chunks := splitMessage(long, 4000)
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(chunks))
	}
	for _, c := range chunks {
		if len(c) > 4000 {
			t.Errorf("chunk too long: %d", len(c))
		}
	}
	// Multibyte runes must not be split mid-rune.
	for _, c := range splitMessage(strings.Repeat("é", 10), 3) {
		if !strings.HasPrefix(c, "é") || len([]rune(c)) > 3 {
			t.Errorf("bad rune chunk %q", c)
		}
	}
}

func TestTapbackDropped(t *testing.T) {
	ch := New(Config{ServerURL: "http://localhost", Password: "secret",
		OwnerHandles: []string{"+15551234567"}, DebounceDelay: 20 * time.Millisecond})
	out := make(chan channel.Inbound, 4)
	h := ch.makeHandler(context.Background(), out)

	for i, assoc := range []any{2000, 3003, "love"} {
		b, _ := json.Marshal(map[string]any{"type": "new-message", "data": map[string]any{
			"guid": "tb" + string(rune('a'+i)), "text": "Loved “hi”",
			"handle":                map[string]string{"address": "+15551234567"},
			"chats":                 []map[string]string{{"guid": "iMessage;-;+15551234567"}},
			"associatedMessageType": assoc,
		}})
		post(t, h, b)
	}
	select {
	case msg := <-out:
		t.Fatalf("tapback forwarded: %+v", msg)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAttachmentDownloaded(t *testing.T) {
	_, srv := newFakeBB(t, "secret")
	dir := t.TempDir()
	ch := New(Config{ServerURL: srv.URL, Password: "secret", AttachmentDir: dir,
		OwnerHandles: []string{"+15551234567"}, DebounceDelay: 20 * time.Millisecond})
	out := make(chan channel.Inbound, 4)
	h := ch.makeHandler(context.Background(), out)

	b, _ := json.Marshal(map[string]any{"type": "new-message", "data": map[string]any{
		"guid": "m1", "text": "￼",
		"handle":      map[string]string{"address": "+15551234567"},
		"chats":       []map[string]string{{"guid": "iMessage;-;+15551234567"}},
		"attachments": []map[string]string{{"guid": "att-1", "mimeType": "image/jpeg", "transferName": "../IMG 1.jpeg"}},
	}})
	post(t, h, b)

	select {
	case msg := <-out:
		if msg.Content != "" || len(msg.Attachments) != 1 {
			t.Fatalf("content=%q attachments=%+v", msg.Content, msg.Attachments)
		}
		a := msg.Attachments[0]
		if a.MimeType != "image/jpeg" || a.Name != "IMG 1.jpeg" || !strings.HasPrefix(a.Path, dir) {
			t.Errorf("attachment = %+v", a)
		}
		data, err := os.ReadFile(a.Path)
		if err != nil || string(data) != "IMAGEBYTES" {
			t.Fatalf("file %q: %v %q", a.Path, err, data)
		}
		if msg.MessageID != "m1" {
			t.Errorf("MessageID = %q", msg.MessageID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
}

func TestListenRegistersAndUnregistersWebhook(t *testing.T) {
	f, srv := newFakeBB(t, "secret")
	f.private = true
	ch := New(Config{ServerURL: srv.URL, Password: "secret", WebhookPort: freePort(t)})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ch.Listen(ctx, make(chan channel.Inbound)) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, hooks := f.snapshot(); hooks == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("webhook never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.mu.Lock()
	gotURL, _ := f.webhooks[0]["url"].(string)
	f.mu.Unlock()
	if !strings.HasPrefix(gotURL, "http://localhost:") || !strings.HasSuffix(gotURL, "/bluebubbles?password=secret") {
		t.Errorf("registered url = %q", gotURL)
	}
	if !ch.privateAPIEnabled() {
		t.Error("private API should be detected")
	}

	cancel()
	<-done
	if _, _, hooks := f.snapshot(); hooks != 0 {
		t.Errorf("webhook not unregistered, %d remain", hooks)
	}
}

func TestReadReceiptAndTyping(t *testing.T) {
	f, srv := newFakeBB(t, "secret")
	ch := New(Config{ServerURL: srv.URL, Password: "secret", ReadReceipts: true,
		OwnerHandles: []string{"+15551234567"}, DebounceDelay: 20 * time.Millisecond})
	ch.privateAPI = true
	out := make(chan channel.Inbound, 4)
	h := ch.makeHandler(context.Background(), out)
	post(t, h, makeNewMessagePayload("+15551234567", "hi", "iMessage;-;+15551234567", false))
	<-out
	// The dispatcher drives typing for the turn via channel.Typer.
	if err := ch.StartTyping(context.Background(), "iMessage;-;+15551234567"); err != nil {
		t.Fatal(err)
	}
	_ = ch.Send(context.Background(), channel.Outbound{Thread: "iMessage;-;+15551234567", Content: "yo"})

	want := []string{
		"POST /api/v1/chat/iMessage%3B-%3B+15551234567/read",
		"POST /api/v1/chat/iMessage%3B-%3B+15551234567/typing",
		"DELETE /api/v1/chat/iMessage%3B-%3B+15551234567/typing",
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		calls, _, _ := f.snapshot()
		missing := ""
		for _, w := range want {
			found := false
			for _, c := range calls {
				if c == w {
					found = true
				}
			}
			if !found {
				missing = w
			}
		}
		if missing == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing call %q in %v", missing, calls)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRedact(t *testing.T) {
	got := redact("from +15551234567 chat iMessage;-;bob@example.com")
	if strings.Contains(got, "5551234567") || strings.Contains(got, "bob@") {
		t.Errorf("redact leaked: %q", got)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestSendAttachmentAndThreadedGroupReply(t *testing.T) {
	f, srv := newFakeBB(t, "secret")
	ch := New(Config{ServerURL: srv.URL, Password: "secret"})
	ch.privateAPI = true
	img := filepath.Join(t.TempDir(), "pic.png")
	_ = os.WriteFile(img, []byte("PNG"), 0o600)

	err := ch.Send(context.Background(), channel.Outbound{
		Thread: "iMessage;+;chat1", Content: "one\n\ntwo", ReplyTo: "msg-guid",
		Attachments: []channel.Attachment{{Path: img, MimeType: "image/png", Name: "pic.png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.uploads) != 1 || f.uploads[0] != "pic.png:PNG" {
		t.Errorf("uploads = %v", f.uploads)
	}
	if len(f.texts) != 2 || f.texts[0]["selectedMessageGuid"] != "msg-guid" || f.texts[0]["method"] != "private-api" {
		t.Fatalf("texts = %v", f.texts)
	}
	if _, threaded := f.texts[1]["selectedMessageGuid"]; threaded {
		t.Error("only the first bubble should be threaded")
	}
}

func TestDMReplyNotThreaded(t *testing.T) {
	f, srv := newFakeBB(t, "secret")
	ch := New(Config{ServerURL: srv.URL, Password: "secret"})
	ch.privateAPI = true
	_ = ch.Send(context.Background(), channel.Outbound{Thread: "iMessage;-;+1555", Content: "hi", ReplyTo: "g"})
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, threaded := f.texts[0]["selectedMessageGuid"]; threaded {
		t.Error("DM reply should not be threaded")
	}
}

func TestResolveChat(t *testing.T) {
	f, srv := newFakeBB(t, "secret")
	f.chats = []map[string]string{
		{"guid": "iMessage;+;chat9", "chatIdentifier": "chat9"},
		{"guid": "SMS;-;+15551234567", "chatIdentifier": "+15551234567"},
	}
	c := NewClient(srv.URL, "secret")
	guid, err := c.ResolveChat(context.Background(), "+1 (555) 123-4567")
	if err != nil || guid != "SMS;-;+15551234567" {
		t.Errorf("guid = %q, err = %v", guid, err)
	}
	guid, _ = c.ResolveChat(context.Background(), "nobody@example.com")
	if guid != "" {
		t.Errorf("unknown handle resolved to %q", guid)
	}
}

func TestTypingNoopWithoutPrivateAPI(t *testing.T) {
	f, srv := newFakeBB(t, "secret")
	ch := New(Config{ServerURL: srv.URL, Password: "secret", ReadReceipts: true})
	if err := ch.StartTyping(context.Background(), "iMessage;-;x"); err != nil {
		t.Fatal(err)
	}
	if calls, _, _ := f.snapshot(); len(calls) != 0 {
		t.Fatalf("typing called without private API: %v", calls)
	}
}
