package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// chatgptModelsClientVersions: the catalog gates models by client_version;
// try the newest first, then the ungated one (as hermes-agent does).
var chatgptModelsClientVersions = []string{"99.0.0", "0.0.0"}

// ListModels returns the model IDs the provider offers ("" = Default).
func (p *Providers) ListModels(ctx context.Context, provider string) ([]string, error) {
	if provider == "" {
		provider = p.Default
	}
	hc := &http.Client{Timeout: 15 * time.Second}
	switch provider {
	case ProviderChatGPT:
		auth, err := p.chatgptAuth()
		if err != nil {
			return nil, err
		}
		return listChatGPTModels(ctx, hc, ChatGPTBaseURL, auth)
	case ProviderOMLX:
		if p.OMLXURL == "" {
			return nil, fmt.Errorf("omlx: omlx.url not set")
		}
		return listOpenAIModels(ctx, hc, strings.TrimRight(p.OMLXURL, "/"), p.OMLXKey)
	}
	return nil, unknownProvider(provider)
}

func listChatGPTModels(ctx context.Context, hc *http.Client, baseURL string, auth *ChatGPTAuth) ([]string, error) {
	token, err := auth.Token(ctx)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{"Authorization": "Bearer " + token}
	for k, v := range chatgptAccountHeaders(token) {
		headers[k] = v
	}
	for _, v := range chatgptModelsClientVersions {
		var body struct {
			Models []struct {
				Slug       string `json:"slug"`
				Visibility string `json:"visibility"`
				Priority   *int   `json:"priority"`
			} `json:"models"`
		}
		if err := getJSON(ctx, hc, baseURL+"/models?client_version="+v, headers, &body); err != nil {
			return nil, fmt.Errorf("chatgpt models: %w", err)
		}
		type ranked struct {
			rank int
			slug string
		}
		var rs []ranked
		seen := map[string]bool{}
		for _, m := range body.Models {
			slug := strings.TrimSpace(m.Slug)
			vis := strings.ToLower(m.Visibility)
			if slug == "" || vis == "hide" || vis == "hidden" || seen[slug] {
				continue
			}
			seen[slug] = true
			r := 10_000
			if m.Priority != nil {
				r = *m.Priority
			}
			rs = append(rs, ranked{r, slug})
		}
		if len(rs) == 0 {
			continue
		}
		sort.Slice(rs, func(i, j int) bool {
			if rs[i].rank != rs[j].rank {
				return rs[i].rank < rs[j].rank
			}
			return rs[i].slug < rs[j].slug
		})
		out := make([]string, len(rs))
		for i, r := range rs {
			out[i] = r.slug
		}
		return out, nil
	}
	return nil, nil
}

func listOpenAIModels(ctx context.Context, hc *http.Client, baseURL, apiKey string) ([]string, error) {
	headers := map[string]string{}
	if apiKey != "" {
		headers["Authorization"] = "Bearer " + apiKey
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := getJSON(ctx, hc, baseURL+"/v1/models", headers, &body); err != nil {
		return nil, fmt.Errorf("omlx models: %w", err)
	}
	var out []string
	for _, m := range body.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}

func getJSON(ctx context.Context, hc *http.Client, u string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}
