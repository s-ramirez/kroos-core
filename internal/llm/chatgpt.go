package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/s-ramirez/kroos-core/internal/types"
)

// ChatGPTBaseURL is the Codex backend that serves ChatGPT-subscription traffic.
const ChatGPTBaseURL = "https://chatgpt.com/backend-api/codex"

const defaultInstructions = "You are a helpful assistant."

// ChatGPTClient speaks the Responses API (streaming, store=false) against the
// Codex backend and adapts it to the chat-completions shapes the runner uses.
type ChatGPTClient struct {
	baseURL    string
	model      string
	auth       *ChatGPTAuth
	sessionID  string
	httpClient *http.Client
}

func NewChatGPTClient(model string, auth *ChatGPTAuth, timeout time.Duration) *ChatGPTClient {
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return &ChatGPTClient{
		baseURL:    ChatGPTBaseURL,
		model:      model,
		auth:       auth,
		sessionID:  hex.EncodeToString(b),
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (c *ChatGPTClient) Chat(ctx context.Context, messages []types.Message, tools []types.ChatRequestTool) (*types.ChatResponse, error) {
	body, err := json.Marshal(buildResponsesRequest(c.model, messages, tools))
	if err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, body)
	var ce *ClientError
	if errors.As(err, &ce) && ce.Status == http.StatusUnauthorized {
		if rerr := c.auth.Refresh(ctx); rerr != nil {
			return nil, rerr
		}
		resp, err = c.post(ctx, body)
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return parseResponsesStream(resp.Body)
}

func (c *ChatGPTClient) post(ctx context.Context, body []byte) (*http.Response, error) {
	token, err := c.auth.Token(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("session_id", c.sessionID)
	for k, v := range chatgptAccountHeaders(token) {
		req.Header.Set(k, v)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		return nil, &ClientError{Status: resp.StatusCode, Body: string(raw)}
	}
	return resp, nil
}

type responsesRequest struct {
	Model             string `json:"model"`
	Instructions      string `json:"instructions"`
	Input             []any  `json:"input"`
	Tools             []any  `json:"tools,omitempty"`
	ToolChoice        string `json:"tool_choice,omitempty"`
	ParallelToolCalls bool   `json:"parallel_tool_calls"`
	Store             bool   `json:"store"`
	Stream            bool   `json:"stream"`
}

func buildResponsesRequest(model string, messages []types.Message, tools []types.ChatRequestTool) responsesRequest {
	req := responsesRequest{Model: model, Input: []any{}, ParallelToolCalls: true, Stream: true}
	var instructions []string
	for _, m := range messages {
		switch m.Role {
		case "system":
			// Leading system messages become instructions; later ones stay in-line as developer turns.
			if len(req.Input) == 0 {
				instructions = append(instructions, contentText(m.Content))
				continue
			}
			req.Input = append(req.Input, map[string]any{
				"type": "message", "role": "developer", "content": inputParts(m.Content),
			})
		case "user":
			req.Input = append(req.Input, map[string]any{
				"type": "message", "role": "user", "content": inputParts(m.Content),
			})
		case "assistant":
			if text := contentText(m.Content); text != "" {
				req.Input = append(req.Input, map[string]any{
					"type": "message", "role": "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": text}},
				})
			}
			for _, tc := range m.ToolCalls {
				req.Input = append(req.Input, map[string]any{
					"type": "function_call", "call_id": tc.ID,
					"name": tc.Function.Name, "arguments": tc.Function.Arguments,
				})
			}
		case "tool":
			req.Input = append(req.Input, map[string]any{
				"type": "function_call_output", "call_id": m.ToolCallID, "output": contentText(m.Content),
			})
		}
	}
	req.Instructions = strings.TrimSpace(strings.Join(instructions, "\n\n"))
	if req.Instructions == "" {
		req.Instructions = defaultInstructions
	}
	for _, t := range tools {
		params := t.Function.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		req.Tools = append(req.Tools, map[string]any{
			"type": "function", "name": t.Function.Name, "description": t.Function.Description,
			"parameters": params, "strict": false,
		})
	}
	if len(req.Tools) > 0 {
		req.ToolChoice = "auto"
	}
	return req
}

type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

// chatParts normalises message content (string or chat-completions parts) into parts.
func chatParts(content any) []contentPart {
	switch v := content.(type) {
	case nil:
		return nil
	case string:
		return []contentPart{{Type: "text", Text: v}}
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return nil
	}
	var parts []contentPart
	if json.Unmarshal(raw, &parts) != nil {
		return []contentPart{{Type: "text", Text: string(raw)}}
	}
	return parts
}

func contentText(content any) string {
	var b strings.Builder
	for _, p := range chatParts(content) {
		if p.Type == "text" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func inputParts(content any) []any {
	out := []any{}
	for _, p := range chatParts(content) {
		switch p.Type {
		case "text":
			out = append(out, map[string]any{"type": "input_text", "text": p.Text})
		case "image_url":
			out = append(out, map[string]any{"type": "input_image", "image_url": p.ImageURL.URL})
		}
	}
	return out
}

type responsesItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type responsesEvent struct {
	Type     string        `json:"type"`
	Item     responsesItem `json:"item"`
	Message  string        `json:"message"`
	Response struct {
		Output []responsesItem `json:"output"`
		Usage  *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
}

// parseResponsesStream reads SSE until response.completed and converts the output.
func parseResponsesStream(r io.Reader) (*types.ChatResponse, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	var items []responsesItem
	var data strings.Builder
	for {
		line, err := br.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		} else if line == "" && data.Len() > 0 {
			payload := data.String()
			data.Reset()
			if payload == "[DONE]" {
				break
			}
			var ev responsesEvent
			if json.Unmarshal([]byte(payload), &ev) != nil {
				continue
			}
			switch ev.Type {
			case "response.output_item.done":
				items = append(items, ev.Item)
			case "response.completed", "response.done", "response.incomplete":
				out := ev.Response.Output
				if len(out) == 0 {
					out = items // backend sometimes omits output on completion
				}
				resp := toChatResponse(out)
				if u := ev.Response.Usage; u != nil {
					resp.Usage = &types.Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens, TotalTokens: u.TotalTokens}
				}
				return resp, nil
			case "response.failed":
				msg := "response failed"
				if ev.Response.Error != nil && ev.Response.Error.Message != "" {
					msg = ev.Response.Error.Message
				}
				return nil, fmt.Errorf("chatgpt: %s", msg)
			case "error":
				return nil, fmt.Errorf("chatgpt: %s", ev.Message)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}
	if len(items) > 0 {
		return toChatResponse(items), nil
	}
	return nil, errors.New("chatgpt: stream ended without response.completed")
}

func toChatResponse(items []responsesItem) *types.ChatResponse {
	msg := types.ResponseMessage{Role: "assistant"}
	var text strings.Builder
	for _, it := range items {
		switch it.Type {
		case "message":
			for _, c := range it.Content {
				if c.Type == "output_text" {
					text.WriteString(c.Text)
				}
			}
		case "function_call":
			msg.ToolCalls = append(msg.ToolCalls, types.ToolCall{
				ID: it.CallID, Type: "function",
				Function: types.ToolCallFunction{Name: it.Name, Arguments: it.Arguments},
			})
		}
	}
	if text.Len() > 0 {
		s := text.String()
		msg.Content = &s
	}
	return &types.ChatResponse{Choices: []types.Choice{{Message: msg}}}
}
