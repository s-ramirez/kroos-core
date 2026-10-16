package tools

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWebSearchToolReturnsNormalizedResults(t *testing.T) {
	previousEndpoint := webSearchEndpoint
	previousClient := webSearchHTTPClient
	t.Setenv("KROOS_BRAVE_API_KEY", "test-key")
	webSearchEndpoint = "https://search.example.test/res/v1/web/search"
	webSearchHTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if got := r.Header.Get("X-Subscription-Token"); got != "test-key" {
				t.Fatalf("unexpected API key header: %q", got)
			}
			if got := r.URL.Query().Get("q"); got != "kroos tool loop" {
				t.Fatalf("unexpected query: %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{
					"query": {"original": "kroos tool loop", "more_results_available": true},
					"web": {
						"results": [
							{
								"title": "Kroos docs",
								"url": "https://example.com/kroos",
								"description": "Primary result",
								"extra_snippets": ["Extra context"],
								"profile": {"name": "example.com"}
							}
						]
					}
				}`)),
				Request: r,
			}, nil
		}),
	}
	defer func() {
		webSearchEndpoint = previousEndpoint
		webSearchHTTPClient = previousClient
	}()

	tool := NewWebSearchTool()
	raw, _ := json.Marshal(WebSearchParams{Query: "kroos tool loop", Count: 3})

	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Output)
	}
	for _, fragment := range []string{
		`"provider": "brave"`,
		`"query": "kroos tool loop"`,
		`"moreResultsAvailable": true`,
		`"title": "Kroos docs"`,
		`"url": "https://example.com/kroos"`,
	} {
		if !strings.Contains(result.Output, fragment) {
			t.Fatalf("expected output to contain %q, got %q", fragment, result.Output)
		}
	}
}

func TestWebSearchToolHandlesGzipResponses(t *testing.T) {
	previousEndpoint := webSearchEndpoint
	previousClient := webSearchHTTPClient
	t.Setenv("KROOS_BRAVE_API_KEY", "test-key")
	webSearchEndpoint = "https://search.example.test/res/v1/web/search"
	webSearchHTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			_, err := gz.Write([]byte(`{
				"query": {"original": "kroos tool loop", "more_results_available": false},
				"web": {
					"results": [
						{
							"title": "Compressed result",
							"url": "https://example.com/compressed",
							"description": "Compressed payload",
							"profile": {"name": "example.com"}
						}
					]
				}
			}`))
			if err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type":     []string{"application/json"},
					"Content-Encoding": []string{"gzip"},
				},
				Body:    io.NopCloser(bytes.NewReader(buf.Bytes())),
				Request: r,
			}, nil
		}),
	}
	defer func() {
		webSearchEndpoint = previousEndpoint
		webSearchHTTPClient = previousClient
	}()

	tool := NewWebSearchTool()
	raw, _ := json.Marshal(WebSearchParams{Query: "kroos tool loop", Count: 1})

	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Output)
	}
	for _, fragment := range []string{
		`"title": "Compressed result"`,
		`"url": "https://example.com/compressed"`,
	} {
		if !strings.Contains(result.Output, fragment) {
			t.Fatalf("expected output to contain %q, got %q", fragment, result.Output)
		}
	}
}

func TestWebSearchToolRequiresAPIKey(t *testing.T) {
	t.Setenv("KROOS_BRAVE_API_KEY", "")
	t.Setenv("BRAVE_API_KEY", "")

	tool := NewWebSearchTool()
	raw, _ := json.Marshal(WebSearchParams{Query: "kroos"})

	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("expected error result, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "requires KROOS_BRAVE_API_KEY or BRAVE_API_KEY") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

func TestWebSearchToolRejectsMissingQuery(t *testing.T) {
	tool := NewWebSearchTool()
	raw, _ := json.Marshal(WebSearchParams{})

	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("expected error result, got %q", result.Output)
	}
	if result.Output != "error: query is required" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}
