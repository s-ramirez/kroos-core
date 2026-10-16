package llm

import (
	"fmt"
	"sync"
	"time"
)

const (
	ProviderChatGPT = "chatgpt"
	ProviderOMLX    = "omlx"
)

// Providers builds ChatClients for the supported providers.
type Providers struct {
	Default  string // provider used when a definition names none
	OMLXURL  string
	OMLXKey  string
	AuthPath string // ChatGPT token store (default ~/.kroos/auth.json)

	once    sync.Once
	auth    *ChatGPTAuth
	authErr error
}

// New returns a client for provider ("" = Default) and model.
func (p *Providers) New(provider, model string, timeout time.Duration) (ChatClient, error) {
	if provider == "" {
		provider = p.Default
	}
	switch provider {
	case ProviderChatGPT:
		auth, err := p.chatgptAuth()
		if err != nil {
			return nil, err
		}
		return NewChatGPTClient(model, auth, timeout), nil
	case ProviderOMLX:
		if p.OMLXURL == "" {
			return nil, fmt.Errorf("omlx: omlx.url not set")
		}
		return NewClient(p.OMLXURL, model, &Options{Timeout: timeout, APIKey: p.OMLXKey}), nil
	}
	return nil, unknownProvider(provider)
}

// Names lists the supported providers.
func (p *Providers) Names() []string { return []string{ProviderChatGPT, ProviderOMLX} }

func (p *Providers) chatgptAuth() (*ChatGPTAuth, error) {
	p.once.Do(func() {
		path := p.AuthPath
		if path == "" {
			path = DefaultAuthPath()
		}
		p.auth, p.authErr = LoadChatGPTAuth(path)
	})
	return p.auth, p.authErr
}

func unknownProvider(provider string) error {
	return fmt.Errorf("unknown provider %q (want %s or %s)", provider, ProviderChatGPT, ProviderOMLX)
}
