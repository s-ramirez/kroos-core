package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/types"
)

func TestClientChatSendsExpectedRequest(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotAuth string
	var gotRequest types.ChatRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")

		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-model", nil)
	resp, err := client.Chat(context.Background(), []types.Message{
		{Role: "user", Content: "hello"},
	}, []types.ChatRequestTool{
		{
			Type: "function",
			Function: types.ChatRequestFunction{
				Name:        "read",
				Description: "Read file",
				Parameters: map[string]any{
					"type": "object",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("unexpected method: %s", gotMethod)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("unexpected path: %s", gotPath)
	}
	if gotAuth != "" {
		t.Fatalf("unexpected authorization header: %q", gotAuth)
	}
	if gotRequest.Model != "test-model" {
		t.Fatalf("unexpected model: %s", gotRequest.Model)
	}
	if len(gotRequest.Messages) != 1 || gotRequest.Messages[0].Content != "hello" {
		t.Fatalf("unexpected messages: %#v", gotRequest.Messages)
	}
	if len(gotRequest.Tools) != 1 || gotRequest.Tools[0].Function.Name != "read" {
		t.Fatalf("unexpected tools: %#v", gotRequest.Tools)
	}
	if types.GetResponseText(resp) != "ok" {
		t.Fatalf("unexpected response text: %q", types.GetResponseText(resp))
	}
}

func TestClientChatSendsBearerTokenWhenAPIKeyConfigured(t *testing.T) {
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-model", &Options{APIKey: "secret-key"})
	_, err := client.Chat(context.Background(), []types.Message{
		{Role: "user", Content: "hello"},
	}, nil)
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}

	if gotAuth != "Bearer secret-key" {
		t.Fatalf("unexpected authorization header: %q", gotAuth)
	}
}

func TestClientChatReturnsClientErrorForNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-model", nil)
	_, err := client.Chat(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}

	var clientErr *ClientError
	if !errors.As(err, &clientErr) {
		t.Fatalf("expected ClientError, got %T", err)
	}
	if !strings.Contains(err.Error(), "API error (400)") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("unexpected error body: %v", err)
	}
	if !strings.Contains(strings.ToLower(clientErr.Body), "bad request") {
		t.Fatalf("unexpected client error body: %q", clientErr.Body)
	}
}

func TestClientChatReturnsDecodeErrorForInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-model", nil)
	_, err := client.Chat(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestClientErrorIsRateLimitedOn429(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit"}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-model", nil)
	_, err := client.Chat(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
}
