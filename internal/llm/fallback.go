package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/s-ramirez/kroos-core/internal/types"
)

// fallbackCooldown is how long a failed provider is skipped before retrying
// it; requests the provider rejected outright (4xx other than 408/429, e.g.
// an unsupported model) are config problems, so they're skipped longer.
const (
	fallbackCooldown         = time.Minute
	fallbackRejectedCooldown = 10 * time.Minute
)

func cooldownFor(err error) time.Duration {
	var ce *ClientError
	if errors.As(err, &ce) && ce.Status >= 400 && ce.Status < 500 && ce.Status != 408 && ce.Status != 429 {
		return fallbackRejectedCooldown
	}
	return fallbackCooldown
}

type fallbackTarget struct {
	provider, model string
}

// FallbackClient tries its targets in order: the requested provider/model
// first, then the other providers on their default models. Clients are built
// lazily so an unconfigured provider is just skipped.
type FallbackClient struct {
	p       *Providers
	timeout time.Duration
	targets []fallbackTarget

	mu      sync.Mutex
	clients map[fallbackTarget]ChatClient
}

// Client returns a fallback-chained client for provider ("" = Default) and
// model ("" = provider default). With Fallback off it only uses provider.
func (p *Providers) Client(provider, model string, timeout time.Duration) *FallbackClient {
	if provider == "" {
		provider = p.Default
	}
	targets := []fallbackTarget{{provider, model}}
	if p.Fallback {
		for _, n := range p.Names() {
			if n != provider {
				targets = append(targets, fallbackTarget{n, ""})
			}
		}
	}
	return &FallbackClient{
		p: p, timeout: timeout, targets: targets,
		clients: map[fallbackTarget]ChatClient{},
	}
}

func (f *FallbackClient) Chat(ctx context.Context, messages []types.Message, tools []types.ChatRequestTool) (*types.ChatResponse, error) {
	// Healthy targets first, cooling-down ones last (still tried rather than failing outright).
	now := time.Now()
	var order, cooling []fallbackTarget
	for _, t := range f.targets {
		if f.p.isDown(t.provider, now) {
			cooling = append(cooling, t)
		} else {
			order = append(order, t)
		}
	}
	order = append(order, cooling...)

	var errs []string
	for i, t := range order {
		client, err := f.client(ctx, t)
		if err == nil {
			var resp *types.ChatResponse
			resp, err = client.Chat(ctx, messages, tools)
			if ctx.Err() != nil {
				return nil, err
			}
			if f.p.OnResult != nil {
				f.p.OnResult(t.provider, err)
			}
			if err == nil {
				f.p.markDown(t.provider, 0)
				return resp, nil
			}
		}
		if ctx.Err() != nil {
			return nil, err
		}
		f.p.markDown(t.provider, cooldownFor(err))
		errs = append(errs, fmt.Sprintf("%s: %v", t.provider, err))
		if len(order) == 1 {
			return nil, err
		}
		if i+1 < len(order) {
			f.p.logf("%s failed (%v); falling back to %s", t.provider, err, order[i+1].provider)
		}
	}
	return nil, errors.New("all providers failed: " + strings.Join(errs, "; "))
}

func (f *FallbackClient) client(ctx context.Context, t fallbackTarget) (ChatClient, error) {
	f.mu.Lock()
	c, ok := f.clients[t]
	f.mu.Unlock()
	if ok {
		return c, nil
	}
	c, err := f.p.newCtx(ctx, t.provider, t.model, f.timeout)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.clients[t] = c
	f.mu.Unlock()
	return c, nil
}
