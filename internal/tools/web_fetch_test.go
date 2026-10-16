package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestWebFetchToolExtractsReadableHTML(t *testing.T) {
	previousValidator := webFetchTargetValidator
	previousFactory := webFetchHTTPClientFactory
	webFetchTargetValidator = func(context.Context, *url.URL) error { return nil }
	defer func() {
		webFetchTargetValidator = previousValidator
		webFetchHTTPClientFactory = previousFactory
	}()
	webFetchHTTPClientFactory = func(context.Context) *http.Client {
		return &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
					Body: io.NopCloser(strings.NewReader(
						`<!doctype html><html><head><title>Kroos</title></head><body><article><h1>Kroos</h1><p>Hello <b>world</b>.</p></article></body></html>`,
					)),
					Request: r,
				}, nil
			}),
		}
	}

	tool := NewWebFetchTool()
	raw, _ := json.Marshal(WebFetchParams{URL: "https://example.com/kroos"})

	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Output)
	}
	if !strings.Contains(result.Output, `"title": "Kroos"`) {
		t.Fatalf("expected title in output, got %q", result.Output)
	}
	if !strings.Contains(result.Output, `Hello world`) {
		t.Fatalf("expected readable content in output, got %q", result.Output)
	}
}

func TestWebFetchToolRejectsMissingURL(t *testing.T) {
	tool := NewWebFetchTool()
	raw, _ := json.Marshal(WebFetchParams{})

	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("expected error result, got %q", result.Output)
	}
	if result.Output != "error: url is required" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

func TestWebFetchToolRejectsLoopbackHost(t *testing.T) {
	tool := NewWebFetchTool()
	raw, _ := json.Marshal(WebFetchParams{URL: "http://127.0.0.1/private"})

	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("expected error result, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "refusing to fetch private or loopback address") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}
