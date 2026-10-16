package tools

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	webSearchDefaultCount  = 5
	webSearchMaxCount      = 10
	webSearchDefaultTimout = 20 * time.Second
)

var (
	webSearchEndpoint   = "https://api.search.brave.com/res/v1/web/search"
	webSearchHTTPClient = &http.Client{Timeout: webSearchDefaultTimout}
)

type WebSearchTool struct{}

var webSearchDefinition = ToolDefinition{
	Name:        "web_search",
	Description: "Search the web for recent and relevant pages. Returns result titles, URLs, and snippets.",
	Guidelines: []string{
		"Use web_search when you need to discover relevant URLs or gather current web sources.",
		"Use web_fetch after web_search when you want to read a specific page in more detail.",
		"Prefer web_search over bash for public web search APIs.",
	},
	Parameters: JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "Search query"},
			"count": map[string]any{"type": "number", "description": "Number of results to return (default: 5, max: 10)"},
		},
		"required": []string{"query"},
	},
	SelectionRules: []SelectionRule{
		{Text: "Use web_search for public web discovery instead of shelling out to ad-hoc search commands."},
	},
}

type WebSearchParams struct {
	Query string `json:"query"`
	Count int    `json:"count"`
}

type braveSearchResponse struct {
	Query struct {
		Original             string `json:"original"`
		MoreResultsAvailable bool   `json:"more_results_available"`
	} `json:"query"`
	Web struct {
		Results []struct {
			Title         string   `json:"title"`
			URL           string   `json:"url"`
			Description   string   `json:"description"`
			ExtraSnippets []string `json:"extra_snippets"`
			Profile       struct {
				Name string `json:"name"`
			} `json:"profile"`
		} `json:"results"`
	} `json:"web"`
}

type webSearchPayload struct {
	Provider             string                  `json:"provider"`
	Query                string                  `json:"query"`
	MoreResultsAvailable bool                    `json:"moreResultsAvailable"`
	Results              []webSearchResultRecord `json:"results"`
}

type webSearchResultRecord struct {
	Title   string   `json:"title"`
	URL     string   `json:"url"`
	Snippet string   `json:"snippet,omitempty"`
	Source  string   `json:"source,omitempty"`
	Extras  []string `json:"extras,omitempty"`
}

func NewWebSearchTool() *WebSearchTool {
	return &WebSearchTool{}
}

func (t *WebSearchTool) Definition() ToolDefinition {
	return webSearchDefinition
}

func (t *WebSearchTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var params WebSearchParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return errorResult("invalid JSON arguments"), nil
	}
	params.Query = strings.TrimSpace(params.Query)
	if params.Query == "" {
		return errorResult("query is required"), nil
	}

	apiKey := strings.TrimSpace(os.Getenv("KROOS_BRAVE_API_KEY"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("BRAVE_API_KEY"))
	}
	if apiKey == "" {
		return errorResult("web_search requires KROOS_BRAVE_API_KEY or BRAVE_API_KEY"), nil
	}

	count := params.Count
	if count <= 0 {
		count = webSearchDefaultCount
	}
	if count > webSearchMaxCount {
		count = webSearchMaxCount
	}

	endpoint, err := url.Parse(webSearchEndpoint)
	if err != nil {
		return errorResultf("failed to build search request: %v", err), nil
	}
	query := endpoint.Query()
	query.Set("q", params.Query)
	query.Set("count", fmt.Sprintf("%d", count))
	query.Set("safesearch", "moderate")
	query.Set("spellcheck", "1")
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return errorResultf("failed to build search request: %v", err), nil
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-Subscription-Token", apiKey)

	resp, err := webSearchHTTPClient.Do(req)
	if err != nil {
		return errorResultf("search failed: %v", err), nil
	}
	defer resp.Body.Close()

	body, err := readWebSearchResponseBody(resp)
	if err != nil {
		return errorResultf("failed to read search response: %v", err), nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return errorResultf("search failed (%d): %s", resp.StatusCode, message), nil
	}

	var decoded braveSearchResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return errorResultf("failed to decode search response: %v", err), nil
	}

	results := make([]webSearchResultRecord, 0, len(decoded.Web.Results))
	for _, item := range decoded.Web.Results {
		record := webSearchResultRecord{
			Title:   strings.TrimSpace(item.Title),
			URL:     strings.TrimSpace(item.URL),
			Snippet: strings.TrimSpace(item.Description),
			Source:  strings.TrimSpace(item.Profile.Name),
		}
		if len(item.ExtraSnippets) > 0 {
			record.Extras = append([]string(nil), item.ExtraSnippets...)
		}
		results = append(results, record)
	}

	payload := webSearchPayload{
		Provider:             "brave",
		Query:                decoded.Query.Original,
		MoreResultsAvailable: decoded.Query.MoreResultsAvailable,
		Results:              results,
	}
	if payload.Query == "" {
		payload.Query = params.Query
	}

	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return errorResultf("failed to encode search response: %v", err), nil
	}

	return ToolResult{Output: string(encoded)}, nil
}

func readWebSearchResponseBody(resp *http.Response) ([]byte, error) {
	reader := io.LimitReader(resp.Body, 1<<20)
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Content-Encoding")), "gzip") {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		reader = io.LimitReader(gz, 1<<20)
	}

	return io.ReadAll(reader)
}
