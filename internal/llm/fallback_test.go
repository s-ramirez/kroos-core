package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/s-ramirez/kroos-core/internal/types"
)

// omlxServer serves /v1/models and /v1/chat/completions, recording the model asked for.
func omlxServer(t *testing.T, gotModel *atomic.Value) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"gemma"},{"id":"qwen"}]}`)
		case "/v1/chat/completions":
			var req types.ChatRequest
			decodeJSON(t, r, &req)
			gotModel.Store(req.Model)
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"from omlx"}}]}`)
		}
	}))
}

func TestFallbackSkipsNotLoggedInChatGPT(t *testing.T) {
	var gotModel atomic.Value
	srv := omlxServer(t, &gotModel)
	defer srv.Close()
	p := &Providers{Default: ProviderChatGPT, Fallback: true, OMLXURL: srv.URL,
		Models: map[string]string{ProviderChatGPT: "gpt-5.4"}, AuthPath: filepath.Join(t.TempDir(), "auth.json")}

	resp, err := p.Client("", "gpt-5.4", time.Second).Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Fallback runs omlx on its own default (first served), not the chatgpt model.
	if types.GetResponseText(resp) != "from omlx" || gotModel.Load() != "gemma" {
		t.Fatalf("resp=%q model=%v", types.GetResponseText(resp), gotModel.Load())
	}
}

func TestFallbackOnServerErrorThenCooldown(t *testing.T) {
	var primaryCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer primary.Close()
	var gotModel atomic.Value
	backup := omlxServer(t, &gotModel)
	defer backup.Close()

	// Simulate chatgpt as the failing primary by pointing its client at the 502 server.
	path := filepath.Join(t.TempDir(), "auth.json")
	saveChatGPTTokens(path, ChatGPTTokens{AccessToken: fakeJWT(time.Now().Add(time.Hour)), RefreshToken: "r"})
	p := &Providers{Default: ProviderChatGPT, Fallback: true, OMLXURL: backup.URL, AuthPath: path,
		Models: map[string]string{ProviderChatGPT: "gpt-5.4", ProviderOMLX: "qwen"}}
	fc := p.Client("", "", time.Second)
	cg, err := p.New(ProviderChatGPT, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cg.(*ChatGPTClient).baseURL = primary.URL
	fc.clients[fallbackTarget{ProviderChatGPT, ""}] = cg

	msgs := []types.Message{{Role: "user", Content: "hi"}}
	for i := 0; i < 2; i++ {
		resp, err := fc.Chat(context.Background(), msgs, nil)
		if err != nil || types.GetResponseText(resp) != "from omlx" {
			t.Fatalf("call %d: %v %v", i, resp, err)
		}
	}
	if primaryCalls.Load() != 1 {
		t.Fatalf("primary should be skipped during cooldown, calls=%d", primaryCalls.Load())
	}
	if gotModel.Load() != "qwen" {
		t.Fatalf("omlx model = %v", gotModel.Load())
	}
}

func TestFallbackDisabled(t *testing.T) {
	p := &Providers{Default: ProviderChatGPT, OMLXURL: "http://127.0.0.1:1", AuthPath: filepath.Join(t.TempDir(), "auth.json"),
		Models: map[string]string{ProviderChatGPT: "gpt-5.4"}}
	_, err := p.Client("", "", time.Second).Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	if err != ErrNotLoggedIn {
		t.Fatalf("err = %v", err)
	}
}

func TestFallbackAllFail(t *testing.T) {
	p := &Providers{Default: ProviderChatGPT, Fallback: true, AuthPath: filepath.Join(t.TempDir(), "auth.json")}
	_, err := p.Client("", "", time.Second).Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "all providers failed") || !strings.Contains(err.Error(), "omlx") {
		t.Fatalf("err = %v", err)
	}
}

func TestFirstReady(t *testing.T) {
	p := &Providers{OMLXURL: "http://x", AuthPath: filepath.Join(t.TempDir(), "auth.json")}
	if got := p.FirstReady(ProviderChatGPT); got != ProviderOMLX {
		t.Fatalf("got %s", got)
	}
}

func TestCooldownForRejectedRequests(t *testing.T) {
	cases := map[error]time.Duration{
		&ClientError{Status: 400, Body: "model not supported"}: fallbackRejectedCooldown,
		&ClientError{Status: 404}:                              fallbackRejectedCooldown,
		&ClientError{Status: 429}:                              fallbackCooldown,
		&ClientError{Status: 502}:                              fallbackCooldown,
		fmt.Errorf("dial tcp: timeout"):                        fallbackCooldown,
	}
	for err, want := range cases {
		if got := cooldownFor(err); got != want {
			t.Errorf("%v: got %s want %s", err, got, want)
		}
	}
}
