package imessage

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/s-ramirez/kroos-core/internal/channel"
)

func TestNormalizeHandle(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"+15551234567", "+15551234567"},
		{"imessage:+15551234567", "+15551234567"},
		{"sms:+15551234567", "+15551234567"},
		{"+1 (555) 123-4567", "+15551234567"},
		{"user@example.com", "user@example.com"},
		{"User@Example.COM", "user@example.com"},
		{"imessage:User@Example.COM", "user@example.com"},
	}
	for _, tt := range tests {
		got := normalizeHandle(tt.in)
		if got != tt.want {
			t.Errorf("normalizeHandle(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestWebhookHandler_AuthRejects(t *testing.T) {
	ch := New(Config{
		ServerURL:    "http://localhost",
		Password:     "secret",
		WebhookPath:  "/bb",
		WebhookPort:  0,
		OwnerHandles: []string{"+15551234567"},
	})
	out := make(chan channel.Inbound, 4)
	handler := ch.makeHandler(context.Background(), out)

	body := makeNewMessagePayload("+15551234567", "hello", "iMessage;-;+15551234567", false)
	req := httptest.NewRequest(http.MethodPost, "/bb", bytes.NewReader(body))
	// No password supplied.
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", rr.Code)
	}
	if len(out) != 0 {
		t.Error("no message should be enqueued on auth failure")
	}
}

func TestWebhookHandler_NonOwnerDropped(t *testing.T) {
	ch := New(Config{
		ServerURL:    "http://localhost",
		Password:     "secret",
		OwnerHandles: []string{"+15550000000"},
	})
	out := make(chan channel.Inbound, 4)
	handler := ch.makeHandler(context.Background(), out)

	body := makeNewMessagePayload("+19999999999", "hello", "iMessage;-;+19999999999", false)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.URL.RawQuery = "password=secret"
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}
	if len(out) != 0 {
		t.Error("non-owner message should be silently dropped")
	}
}

func TestWebhookHandler_OwnerDMForwarded(t *testing.T) {
	ch := New(Config{
		ServerURL:     "http://localhost",
		Password:      "secret",
		OwnerHandles:  []string{"+15551234567"},
		DebounceDelay: 50 * time.Millisecond,
	})
	out := make(chan channel.Inbound, 4)
	handler := ch.makeHandler(context.Background(), out)

	body := makeNewMessagePayload("+15551234567", "ping", "iMessage;-;+15551234567", false)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.URL.RawQuery = "password=secret"
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	select {
	case msg := <-out:
		if msg.Content != "ping" {
			t.Errorf("content = %q, want %q", msg.Content, "ping")
		}
		if msg.Thread != "iMessage;-;+15551234567" {
			t.Errorf("thread = %q, want chatGuid", msg.Thread)
		}
		if msg.Channel != "imessage" {
			t.Errorf("channel = %q", msg.Channel)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbound message")
	}
}

func TestWebhookHandler_FromMeDropped(t *testing.T) {
	ch := New(Config{
		ServerURL:    "http://localhost",
		Password:     "secret",
		OwnerHandles: []string{"+15551234567"},
	})
	out := make(chan channel.Inbound, 4)
	handler := ch.makeHandler(context.Background(), out)

	body := makeNewMessagePayload("+15551234567", "echo", "iMessage;-;+15551234567", true)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.URL.RawQuery = "password=secret"
	rr := httptest.NewRecorder()
	handler(rr, req)

	if len(out) != 0 {
		t.Error("fromMe message should be dropped")
	}
}

func TestWebhookHandler_GroupRequiresTag(t *testing.T) {
	ch := New(Config{
		ServerURL:     "http://localhost",
		Password:      "secret",
		OwnerHandles:  []string{"+15551234567"},
		GroupTag:      "@kroos",
		DebounceDelay: 50 * time.Millisecond,
	})
	out := make(chan channel.Inbound, 4)
	handler := ch.makeHandler(context.Background(), out)

	// Group message without tag — should be dropped.
	body := makeNewMessagePayload("+15551234567", "hey everyone", "iMessage;+;groupchat123", false)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.URL.RawQuery = "password=secret"
	httptest.NewRecorder()
	rr := httptest.NewRecorder()
	handler(rr, req)
	if len(out) != 0 {
		t.Error("group message without tag should be dropped")
	}

	// Group message with tag — should be forwarded, tag stripped.
	body = makeNewMessagePayload("+15551234567", "@kroos what time is it?", "iMessage;+;groupchat123", false)
	req = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.URL.RawQuery = "password=secret"
	rr = httptest.NewRecorder()
	handler(rr, req)

	select {
	case msg := <-out:
		if strings.Contains(msg.Content, "@kroos") {
			t.Errorf("tag should be stripped from content, got %q", msg.Content)
		}
		if !strings.Contains(msg.Content, "what time is it?") {
			t.Errorf("content should retain question, got %q", msg.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for group message")
	}
}

func TestWebhookHandler_GroupParticipantsForwarded(t *testing.T) {
	ch := New(Config{
		ServerURL:     "http://localhost",
		Password:      "secret",
		OwnerHandles:  []string{"+15551234567"},
		GroupTag:      "@kroos",
		DebounceDelay: 50 * time.Millisecond,
	})
	out := make(chan channel.Inbound, 4)
	handler := ch.makeHandler(context.Background(), out)

	body := makeGroupMessagePayload(
		"+15551234567",
		"@kroos say hi to everyone",
		"iMessage;+;groupchat123",
		[]string{"+15551234567", "+15559999999", "alice@example.com"},
	)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.URL.RawQuery = "password=secret"
	rr := httptest.NewRecorder()
	handler(rr, req)

	select {
	case msg := <-out:
		// Sender should be excluded; other two should be present.
		wantHas := []string{"+15559999999", "alice@example.com"}
		for _, h := range wantHas {
			found := false
			for _, p := range msg.Participants {
				if p == h {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Participants missing %q: %v", h, msg.Participants)
			}
		}
		for _, p := range msg.Participants {
			if p == "+15551234567" {
				t.Errorf("sender should be excluded from Participants, got %v", msg.Participants)
			}
		}
		if !strings.Contains(msg.Content, "[group participants:") {
			t.Errorf("content should include participants prefix, got %q", msg.Content)
		}
		if !strings.Contains(msg.Content, "say hi to everyone") {
			t.Errorf("content should retain body, got %q", msg.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for group message")
	}
}

func TestWebhookHandler_OpenChatAcceptsNonOwnersWithTag(t *testing.T) {
	ch := New(Config{
		ServerURL:     "http://localhost",
		Password:      "secret",
		OwnerHandles:  []string{"+15551234567"},
		GroupTag:      "@kroos",
		OpenChats:     []string{"iMessage;+;open1"},
		DebounceDelay: 50 * time.Millisecond,
	})
	out := make(chan channel.Inbound, 4)
	handler := ch.makeHandler(context.Background(), out)
	post := func(handle, text, chat string) {
		body := makeNewMessagePayload(handle, text, chat, false)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.URL.RawQuery = "password=secret"
		handler(httptest.NewRecorder(), req)
	}

	post("+15550000000", "@kroos hi", "iMessage;+;closed")       // closed group: drop
	post("+15550000000", "hi", "iMessage;+;open1")               // no tag: drop
	post("+15550000000", "@kroos hi", "iMessage;-;+15550000000") // DM: drop
	post("+15550000000", "@kroos what's up", "iMessage;+;open1")

	select {
	case msg := <-out:
		if msg.Thread != "iMessage;+;open1" {
			t.Fatalf("thread = %q", msg.Thread)
		}
		if !strings.Contains(msg.Content, "[+15550000000]: what's up") {
			t.Errorf("content should carry sender prefix, got %q", msg.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for open-chat message")
	}
	select {
	case msg := <-out:
		t.Fatalf("unexpected extra message: %+v", msg)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestWebhookHandler_OpenChatRequiresGroupTagConfigured(t *testing.T) {
	ch := New(Config{
		ServerURL:     "http://localhost",
		Password:      "secret",
		OpenChats:     []string{"iMessage;+;open1"},
		DebounceDelay: 50 * time.Millisecond,
	})
	out := make(chan channel.Inbound, 4)
	handler := ch.makeHandler(context.Background(), out)
	body := makeNewMessagePayload("+15550000000", "hi", "iMessage;+;open1", false)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.URL.RawQuery = "password=secret"
	handler(httptest.NewRecorder(), req)
	select {
	case msg := <-out:
		t.Fatalf("expected drop without GroupTag, got %+v", msg)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestWebhookHandler_DMHasNoParticipants(t *testing.T) {
	ch := New(Config{
		ServerURL:     "http://localhost",
		Password:      "secret",
		OwnerHandles:  []string{"+15551234567"},
		DebounceDelay: 50 * time.Millisecond,
	})
	out := make(chan channel.Inbound, 4)
	handler := ch.makeHandler(context.Background(), out)

	body := makeNewMessagePayload("+15551234567", "ping", "iMessage;-;+15551234567", false)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.URL.RawQuery = "password=secret"
	rr := httptest.NewRecorder()
	handler(rr, req)

	select {
	case msg := <-out:
		if len(msg.Participants) != 0 {
			t.Errorf("DM should have no Participants, got %v", msg.Participants)
		}
		if strings.Contains(msg.Content, "group participants") {
			t.Errorf("DM content should not carry participants prefix, got %q", msg.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for DM")
	}
}

func TestWebhookHandler_NonPostIgnored(t *testing.T) {
	ch := New(Config{Password: "secret"})
	out := make(chan channel.Inbound, 4)
	handler := ch.makeHandler(context.Background(), out)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.URL.RawQuery = "password=secret"
	rr := httptest.NewRecorder()
	handler(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", rr.Code)
	}
}

// makeNewMessagePayload builds a minimal BlueBubbles webhook JSON body.
func makeNewMessagePayload(handle, text, chatGUID string, fromMe bool) []byte {
	type handleObj struct {
		Address string `json:"address"`
	}
	type chatObj struct {
		Guid string `json:"guid"`
	}
	type data struct {
		Text     string    `json:"text"`
		IsFromMe bool      `json:"isFromMe"`
		Chats    []chatObj `json:"chats"`
		Handle   handleObj `json:"handle"`
	}
	type payload struct {
		Type string `json:"type"`
		Data data   `json:"data"`
	}
	p := payload{
		Type: "new-message",
		Data: data{
			Text:     text,
			IsFromMe: fromMe,
			Chats:    []chatObj{{Guid: chatGUID}},
			Handle:   handleObj{Address: handle},
		},
	}
	b, _ := json.Marshal(p)
	return b
}

// makeGroupMessagePayload builds a group-chat webhook body with a participants roster.
func makeGroupMessagePayload(handle, text, chatGUID string, participants []string) []byte {
	type handleObj struct {
		Address string `json:"address"`
	}
	type partObj struct {
		Address string `json:"address"`
	}
	type chatObj struct {
		Guid         string    `json:"guid"`
		Participants []partObj `json:"participants"`
	}
	type data struct {
		Text     string    `json:"text"`
		IsFromMe bool      `json:"isFromMe"`
		Chats    []chatObj `json:"chats"`
		Handle   handleObj `json:"handle"`
	}
	type payload struct {
		Type string `json:"type"`
		Data data   `json:"data"`
	}
	parts := make([]partObj, 0, len(participants))
	for _, p := range participants {
		parts = append(parts, partObj{Address: p})
	}
	p := payload{
		Type: "new-message",
		Data: data{
			Text:   text,
			Chats:  []chatObj{{Guid: chatGUID, Participants: parts}},
			Handle: handleObj{Address: handle},
		},
	}
	b, _ := json.Marshal(p)
	return b
}
