package llm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	ProviderChatGPT = "chatgpt"
	ProviderOMLX    = "omlx"
)

// Providers builds ChatClients for the supported providers. Providers are
// interchangeable: Client() chains the requested one with the rest so a
// missing login or an outage falls through to the next.
type Providers struct {
	Default  string            // provider used when a definition names none
	Models   map[string]string // per-provider default model ("" omlx = first served)
	Fallback bool              // try the other providers when one fails
	OMLXURL  string
	OMLXKey  string
	AuthPath string // ChatGPT token store (default $KROOS_HOME/auth.json)
	Logf     func(format string, args ...any)
	// OnResult, if set, is told the outcome of every request that reached a
	// provider (err nil = success). Unconfigured providers aren't reported.
	OnResult func(provider string, err error)

	mu          sync.Mutex
	auth        *ChatGPTAuth
	omlxDefault string
	downUntil   map[string]time.Time // provider cooldowns after failures, shared by all clients
}

func (p *Providers) isDown(provider string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.downUntil[provider].After(now)
}

func (p *Providers) markDown(provider string, d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.downUntil == nil {
		p.downUntil = map[string]time.Time{}
	}
	if d <= 0 {
		delete(p.downUntil, provider)
		return
	}
	p.downUntil[provider] = time.Now().Add(d)
}

// Names lists the supported providers in fallback order.
func (p *Providers) Names() []string { return []string{ProviderChatGPT, ProviderOMLX} }

// Ready reports whether provider is configured enough to try (no network).
func (p *Providers) Ready(provider string) error {
	switch provider {
	case ProviderChatGPT:
		_, err := p.chatgptAuth()
		return err
	case ProviderOMLX:
		if p.OMLXURL == "" {
			return errors.New("omlx: omlx.url not set")
		}
		return nil
	}
	return unknownProvider(provider)
}

// FirstReady returns preferred if ready, else the first ready provider, else preferred.
func (p *Providers) FirstReady(preferred string) string {
	if p.Ready(preferred) == nil {
		return preferred
	}
	for _, n := range p.Names() {
		if p.Ready(n) == nil {
			return n
		}
	}
	return preferred
}

// New returns a client for exactly provider ("" = Default) and model ("" =
// provider default), without fallback.
func (p *Providers) New(provider, model string, timeout time.Duration) (ChatClient, error) {
	return p.newCtx(context.Background(), provider, model, timeout)
}

func (p *Providers) newCtx(ctx context.Context, provider, model string, timeout time.Duration) (ChatClient, error) {
	if provider == "" {
		provider = p.Default
	}
	if err := p.Ready(provider); err != nil {
		return nil, err
	}
	model, err := p.ModelFor(ctx, provider, model)
	if err != nil {
		return nil, err
	}
	switch provider {
	case ProviderChatGPT:
		auth, _ := p.chatgptAuth()
		return NewChatGPTClient(model, auth, timeout), nil
	case ProviderOMLX:
		return NewClient(p.OMLXURL, model, &Options{Timeout: timeout, APIKey: p.OMLXKey}), nil
	}
	return nil, unknownProvider(provider)
}

// ModelFor returns model, or provider's default when empty. oMLX with no
// configured default uses the first model the server lists.
func (p *Providers) ModelFor(ctx context.Context, provider, model string) (string, error) {
	if model != "" {
		return model, nil
	}
	if m := p.Models[provider]; m != "" {
		return m, nil
	}
	if provider != ProviderOMLX {
		return "", fmt.Errorf("%s: no model configured (set %s.model)", provider, provider)
	}
	p.mu.Lock()
	cached := p.omlxDefault
	p.mu.Unlock()
	if cached != "" {
		return cached, nil
	}
	models, err := p.ListModels(ctx, ProviderOMLX)
	if err != nil {
		return "", err
	}
	if len(models) == 0 {
		return "", errors.New("omlx: server lists no models (set omlx.model)")
	}
	p.mu.Lock()
	p.omlxDefault = models[0]
	p.mu.Unlock()
	return models[0], nil
}

// chatgptAuth loads tokens; failures aren't cached so a later `kroos login` is picked up.
func (p *Providers) chatgptAuth() (*ChatGPTAuth, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.auth != nil {
		return p.auth, nil
	}
	path := p.AuthPath
	if path == "" {
		path = DefaultAuthPath()
	}
	auth, err := LoadChatGPTAuth(path)
	if err != nil {
		return nil, err
	}
	p.auth = auth
	return auth, nil
}

func (p *Providers) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}

func unknownProvider(provider string) error {
	return fmt.Errorf("unknown provider %q (want %s or %s)", provider, ProviderChatGPT, ProviderOMLX)
}
