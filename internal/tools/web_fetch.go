package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"
)

const (
	webFetchDefaultTimeout     = 20 * time.Second
	webFetchDefaultMaxChars    = 20000
	webFetchDefaultMaxBytes    = 2_000_000
	webFetchDefaultMaxRedirect = 3
)

var (
	webFetchHTTPClientFactory = newWebFetchHTTPClient
	webFetchTargetValidator   = validateFetchTarget
)

type WebFetchTool struct{}

var webFetchDefinition = ToolDefinition{
	Name:        "web_fetch",
	Description: "Fetch a web page over HTTP/HTTPS and extract readable text without browser automation.",
	Guidelines: []string{
		"Use web_fetch when you already know which page you want to read.",
		"Prefer web_fetch over bash for straightforward page retrieval and text extraction.",
		"Use web_search first when you need to discover relevant URLs.",
	},
	Parameters: JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"url":      map[string]any{"type": "string", "description": "HTTP or HTTPS URL to fetch"},
			"maxChars": map[string]any{"type": "number", "description": "Maximum characters to return (default: 20000)"},
		},
		"required": []string{"url"},
	},
	SelectionRules: []SelectionRule{
		{Text: "Use web_fetch to read a specific web page when you already have a URL."},
		{Text: "For web research, use web_search to find candidate pages and web_fetch to read the most relevant ones.", Requires: []string{"web_search"}},
	},
}

type WebFetchParams struct {
	URL      string `json:"url"`
	MaxChars int    `json:"maxChars"`
}

type webFetchPayload struct {
	URL         string `json:"url"`
	FinalURL    string `json:"finalUrl"`
	ContentType string `json:"contentType"`
	Status      int    `json:"status"`
	Title       string `json:"title,omitempty"`
	Content     string `json:"content"`
	Truncated   bool   `json:"truncated"`
}

func NewWebFetchTool() *WebFetchTool {
	return &WebFetchTool{}
}

func (t *WebFetchTool) Definition() ToolDefinition {
	return webFetchDefinition
}

func (t *WebFetchTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var params WebFetchParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return errorResult("invalid JSON arguments"), nil
	}
	if strings.TrimSpace(params.URL) == "" {
		return errorResult("url is required"), nil
	}

	maxChars := params.MaxChars
	if maxChars <= 0 {
		maxChars = webFetchDefaultMaxChars
	}

	parsed, err := normalizeWebURL(params.URL)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	if err := webFetchTargetValidator(ctx, parsed); err != nil {
		return errorResult(err.Error()), nil
	}

	client := webFetchHTTPClientFactory(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return errorResultf("failed to build request: %v", err), nil
	}
	req.Header.Set("User-Agent", "github.com/s-ramirez/kroos-core/1.0")
	req.Header.Set("Accept", "text/html, text/plain;q=0.9, application/xhtml+xml;q=0.8, */*;q=0.1")

	resp, err := client.Do(req)
	if err != nil {
		return errorResultf("fetch failed: %v", err), nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchDefaultMaxBytes+1))
	if err != nil {
		return errorResultf("failed to read response body: %v", err), nil
	}
	bodyTruncated := len(body) > webFetchDefaultMaxBytes
	if bodyTruncated {
		body = body[:webFetchDefaultMaxBytes]
	}

	contentType := resp.Header.Get("Content-Type")
	finalURL := resp.Request.URL.String()
	title, content := extractReadableText(contentType, body)
	content = strings.TrimSpace(content)
	if content == "" {
		return errorResult("fetched page contained no readable text"), nil
	}

	truncatedByChars := false
	if len(content) > maxChars {
		content = strings.TrimSpace(content[:maxChars])
		truncatedByChars = true
	}

	payload := webFetchPayload{
		URL:         parsed.String(),
		FinalURL:    finalURL,
		ContentType: contentType,
		Status:      resp.StatusCode,
		Title:       title,
		Content:     content,
		Truncated:   bodyTruncated || truncatedByChars,
	}

	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return errorResultf("failed to encode response: %v", err), nil
	}

	isError := resp.StatusCode < 200 || resp.StatusCode >= 300
	return ToolResult{Output: string(encoded), IsError: isError}, nil
}

func newWebFetchHTTPClient(ctx context.Context) *http.Client {
	return &http.Client{
		Timeout: webFetchDefaultTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= webFetchDefaultMaxRedirect {
				return fmt.Errorf("stopped after %d redirects", webFetchDefaultMaxRedirect)
			}
			if err := webFetchTargetValidator(ctx, req.URL); err != nil {
				return err
			}
			return nil
		},
	}
}

func normalizeWebURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid url: %v", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("invalid url: only http and https are allowed")
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("invalid url: host is required")
	}
	return parsed, nil
}

func validateFetchTarget(ctx context.Context, target *url.URL) error {
	host := strings.TrimSpace(target.Hostname())
	if host == "" {
		return fmt.Errorf("invalid url: host is required")
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("refusing to fetch localhost")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if blockedIP(ip) {
			return fmt.Errorf("refusing to fetch private or loopback address %s", ip.String())
		}
		return nil
	}

	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("failed to resolve host: %v", err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("failed to resolve host: no addresses found")
	}
	for _, addr := range addrs {
		if blockedIP(addr) {
			return fmt.Errorf("refusing to fetch private or loopback address for %s", host)
		}
	}
	return nil
}

func blockedIP(ip netip.Addr) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}

func extractReadableText(contentType string, body []byte) (title string, text string) {
	raw := string(body)
	if strings.Contains(strings.ToLower(contentType), "html") || strings.Contains(strings.ToLower(raw), "<html") {
		return extractHTMLText(body)
	}
	text = strings.TrimSpace(bytesToUTF8(body))
	return "", text
}

func extractHTMLText(body []byte) (string, string) {
	doc, err := xhtml.Parse(strings.NewReader(string(body)))
	if err != nil {
		return "", strings.TrimSpace(string(body))
	}

	var title string
	var builder strings.Builder
	walkHTML(doc, &builder, &title)
	text := normalizeHTMLExtractedText(builder.String())
	return strings.TrimSpace(title), text
}

var htmlSkipTags = []string{"script", "style", "noscript", "svg"}
var htmlBlockTags = []string{
	"article",
	"aside",
	"blockquote",
	"br",
	"div",
	"figcaption",
	"figure",
	"footer",
	"form",
	"h1",
	"h2",
	"h3",
	"h4",
	"h5",
	"h6",
	"header",
	"hr",
	"li",
	"main",
	"nav",
	"ol",
	"p",
	"pre",
	"section",
	"table",
	"td",
	"th",
	"tr",
	"ul",
}

func walkHTML(node *xhtml.Node, builder *strings.Builder, title *string) {
	if node == nil {
		return
	}

	switch node.Type {
	case xhtml.ElementNode:
		tag := strings.ToLower(node.Data)
		if slices.Contains(htmlSkipTags, tag) {
			return
		}
		if tag == "title" && *title == "" {
			*title = strings.TrimSpace(extractNodeText(node))
		}
		if tag == "br" || tag == "hr" {
			builder.WriteString("\n")
			return
		}
		if slices.Contains(htmlBlockTags, tag) {
			builder.WriteString("\n")
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walkHTML(child, builder, title)
		}
		if slices.Contains(htmlBlockTags, tag) {
			builder.WriteString("\n")
		}
	case xhtml.TextNode:
		text := strings.TrimSpace(node.Data)
		if text == "" {
			return
		}
		if builder.Len() > 0 {
			lastByte := builder.String()[builder.Len()-1]
			if lastByte != '\n' && lastByte != ' ' {
				builder.WriteString(" ")
			}
		}
		builder.WriteString(text)
	default:
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walkHTML(child, builder, title)
		}
	}
}

func extractNodeText(node *xhtml.Node) string {
	var builder strings.Builder
	var walk func(*xhtml.Node)
	walk = func(current *xhtml.Node) {
		if current == nil {
			return
		}
		if current.Type == xhtml.TextNode {
			text := strings.TrimSpace(current.Data)
			if text != "" {
				if builder.Len() > 0 {
					builder.WriteString(" ")
				}
				builder.WriteString(text)
			}
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return builder.String()
}

func normalizeHTMLExtractedText(raw string) string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r", "\n"), "\n")
	out := make([]string, 0, len(lines))
	previousBlank := false
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if previousBlank {
				continue
			}
			previousBlank = true
			out = append(out, "")
			continue
		}
		previousBlank = false
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func bytesToUTF8(body []byte) string {
	return strings.TrimSpace(string(body))
}
