package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/s-ramirez/kroos-core/internal/channel/imessage"
)

// IMessageSendConfig holds the BlueBubbles connection details needed to send
// a message. It mirrors the subset of imessage.Config required for sending.
type IMessageSendConfig struct {
	ServerURL string
	Password  string
}

// IMessageSendTool lets the agent proactively send an iMessage to any
// recipient reachable via the local BlueBubbles server.
type IMessageSendTool struct {
	client *imessage.Client
}

var iMessageSendDefinition = ToolDefinition{
	Name:        "imessage_send",
	Description: "Send an iMessage to a phone number, email address, or existing chat via BlueBubbles.",
	Guidelines: []string{
		"Use imessage_send to proactively notify the user or reply outside the current conversation thread.",
		"The 'to' field accepts a phone number (+15551234567), email address, or a full chatGuid (e.g. iMessage;-;+15551234567). Phone/email resolve to the existing DM, or start a new one.",
		"Keep messages concise — iMessage does not render Markdown, so avoid using it.",
	},
	Parameters: JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"to": map[string]any{
				"type":        "string",
				"description": "Recipient: phone number, email, or BlueBubbles chatGuid",
			},
			"message": map[string]any{
				"type":        "string",
				"description": "Message text to send (plain text; Markdown will be stripped)",
			},
		},
		"required": []string{"to", "message"},
	},
}

// IMessageSendParams is the parameter struct for imessage_send.
type IMessageSendParams struct {
	To      string `json:"to"`
	Message string `json:"message"`
}

// NewIMessageSendTool returns a new IMessageSendTool wired to the given config.
func NewIMessageSendTool(cfg IMessageSendConfig) *IMessageSendTool {
	return &IMessageSendTool{client: imessage.NewClient(cfg.ServerURL, cfg.Password)}
}

func (t *IMessageSendTool) Definition() ToolDefinition {
	return iMessageSendDefinition
}

func (t *IMessageSendTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var params IMessageSendParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return errorResult("invalid JSON arguments"), nil
	}
	to := strings.TrimSpace(params.To)
	if to == "" {
		return errorResult("'to' is required"), nil
	}
	if strings.TrimSpace(params.Message) == "" {
		return errorResult("'message' is required"), nil
	}

	text := imessage.StripMarkdown(params.Message)
	if text == "" {
		return errorResult("message is empty after stripping Markdown"), nil
	}

	chatGUID := to
	if !strings.Contains(to, ";") {
		guid, err := t.client.ResolveChat(ctx, to)
		if err != nil {
			return errorResultf("failed to look up chat: %v", err), nil
		}
		if guid == "" {
			// No existing chat: start one with this message.
			if err := t.client.CreateChat(ctx, to, text); err != nil {
				return errorResultf("no existing chat with %s and creating one failed: %v", to, err), nil
			}
			return ToolResult{Output: fmt.Sprintf("Started new chat and sent message to %s", to)}, nil
		}
		chatGUID = guid
	}

	if err := t.client.SendText(ctx, chatGUID, text, ""); err != nil {
		return errorResultf("send failed: %v", err), nil
	}
	return ToolResult{Output: fmt.Sprintf("Message sent to %s", to)}, nil
}
