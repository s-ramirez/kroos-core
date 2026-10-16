package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/s-ramirez/kroos-core/internal/types"
)

func fakeJWT(exp time.Time) string {
	claims, _ := json.Marshal(map[string]any{
		"exp":                         exp.Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1"},
	})
	return "h." + base64.RawURLEncoding.EncodeToString(claims) + ".s"
}

func TestBuildResponsesRequest(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "be nice"},
		{Role: "user", Content: []any{
			map[string]any{"type": "text", "text": "look"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AA"}},
		}},
		{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "call_1", Type: "function", Function: types.ToolCallFunction{Name: "ls", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "call_1", Content: "a.txt"},
	}
	tools := []types.ChatRequestTool{{Type: "function", Function: types.ChatRequestFunction{Name: "ls"}}}
	req := buildResponsesRequest("gpt-5.4", msgs, tools)
	if req.Instructions != "be nice" || !req.Stream || req.Store {
		t.Fatalf("bad header fields: %+v", req)
	}
	raw, _ := json.Marshal(req.Input)
	for _, want := range []string{`"input_text"`, `"input_image"`, `"function_call"`, `"function_call_output"`, `"call_id":"call_1"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("input missing %s: %s", want, raw)
		}
	}
	if len(req.Tools) != 1 || req.ToolChoice != "auto" {
		t.Fatalf("tools: %+v", req.Tools)
	}
}

func TestParseResponsesStream(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"c1","name":"ls","arguments":"{}"}}`,
		``,
		`data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]},{"type":"function_call","call_id":"c1","name":"ls","arguments":"{}"}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`,
		``,
	}, "\n")
	resp, err := parseResponsesStream(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if types.GetResponseText(resp) != "hi" || len(types.GetToolCalls(resp)) != 1 || resp.Usage.TotalTokens != 5 {
		t.Fatalf("unexpected: %+v", resp)
	}
}

func TestParseResponsesStreamFailed(t *testing.T) {
	_, err := parseResponsesStream(strings.NewReader(`data: {"type":"response.failed","response":{"error":{"message":"nope"}}}` + "\n\n"))
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
}

func TestChatGPTClientRefreshesOn401(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ChatGPT-Account-ID") != "acct-1" {
			t.Errorf("missing account header")
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}`+"\n\n")
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "auth.json")
	if err := saveChatGPTTokens(path, ChatGPTTokens{AccessToken: fakeJWT(time.Now().Add(time.Hour)), RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	auth, err := LoadChatGPTAuth(path)
	if err != nil {
		t.Fatal(err)
	}
	// Point refresh at a fake token endpoint by swapping the HTTP transport.
	auth.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := fmt.Sprintf(`{"access_token":%q,"refresh_token":"r2"}`, fakeJWT(time.Now().Add(time.Hour)))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	c := NewChatGPTClient("gpt-5.4", auth, time.Second*5)
	c.baseURL = srv.URL
	resp, err := c.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if types.GetResponseText(resp) != "ok" || calls.Load() != 2 || auth.tokens.RefreshToken != "r2" {
		t.Fatalf("resp=%+v calls=%d", resp, calls.Load())
	}
	if _, err := LoadChatGPTAuth(path); err != nil {
		t.Fatalf("reload after refresh: %v", err)
	}
}

func TestProvidersNotLoggedIn(t *testing.T) {
	p := &Providers{Default: ProviderChatGPT, AuthPath: filepath.Join(t.TempDir(), "auth.json")}
	if _, err := p.New("", "m", 0); err != ErrNotLoggedIn {
		t.Fatalf("err = %v", err)
	}
	if _, err := p.New("bogus", "m", 0); err == nil {
		t.Fatal("want unknown provider error")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			if r.Header.Get("ChatGPT-Account-ID") != "acct-1" {
				t.Errorf("missing account header")
			}
			fmt.Fprint(w, `{"models":[{"slug":"b","priority":2},{"slug":"hidden","visibility":"hide"},{"slug":"a","priority":1}]}`)
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer k" {
				t.Errorf("missing omlx key")
			}
			fmt.Fprint(w, `{"data":[{"id":"z"},{"id":"y"}]}`)
		}
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "auth.json")
	saveChatGPTTokens(path, ChatGPTTokens{AccessToken: fakeJWT(time.Now().Add(time.Hour)), RefreshToken: "r"})
	auth, _ := LoadChatGPTAuth(path)
	got, err := listChatGPTModels(context.Background(), srv.Client(), srv.URL, auth)
	if err != nil || strings.Join(got, ",") != "a,b" {
		t.Fatalf("chatgpt models = %v, %v", got, err)
	}
	p := &Providers{OMLXURL: srv.URL, OMLXKey: "k"}
	got, err = p.ListModels(context.Background(), ProviderOMLX)
	if err != nil || strings.Join(got, ",") != "y,z" {
		t.Fatalf("omlx models = %v, %v", got, err)
	}
}

func decodeJSON(t *testing.T, r *http.Request, v any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		t.Errorf("decode: %v", err)
	}
}
