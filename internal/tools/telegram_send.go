package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/s-ramirez/kroos-core/internal/channel/telegram"
)

// TelegramSendConfig holds the bot token used for sends.
type TelegramSendConfig struct {
	Token   string
	APIBase string // empty = telegram.DefaultAPIBase
}

// TelegramSendTool lets the agent proactively send a Telegram message.
type TelegramSendTool struct {
	cfg    TelegramSendConfig
	client *http.Client
}

var telegramSendDefinition = ToolDefinition{
	Name:        "telegram_send",
	Description: "Send a message to a Telegram chat via the configured bot.",
	Guidelines: []string{
		"Use telegram_send to proactively notify the user or post to a chat outside the current conversation thread.",
		"'to' is a numeric chat ID. For a private chat it equals the user's ID; the user must have messaged the bot at least once. Group IDs are negative.",
		"Messages are sent as plain text and split automatically at Telegram's 4096-character limit.",
	},
	Parameters: JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"to": map[string]any{
				"type":        "string",
				"description": "Telegram chat ID (numeric, negative for groups).",
			},
			"message": map[string]any{
				"type":        "string",
				"description": "Message text to send.",
			},
		},
		"required": []string{"to", "message"},
	},
}

// TelegramSendParams is the parameter struct for telegram_send.
type TelegramSendParams struct {
	To      string `json:"to"`
	Message string `json:"message"`
}

// NewTelegramSendTool returns a new TelegramSendTool.
func NewTelegramSendTool(cfg TelegramSendConfig) *TelegramSendTool {
	if cfg.APIBase == "" {
		cfg.APIBase = telegram.DefaultAPIBase
	}
	return &TelegramSendTool{cfg: cfg, client: &http.Client{Timeout: 30 * time.Second}}
}

func (t *TelegramSendTool) Definition() ToolDefinition {
	return telegramSendDefinition
}

func (t *TelegramSendTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var params TelegramSendParams
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
	if err := telegram.SendMessage(ctx, t.client, t.cfg.APIBase, t.cfg.Token, to, params.Message); err != nil {
		return errorResultf("send failed: %v", err), nil
	}
	return ToolResult{Output: fmt.Sprintf("Message sent to chat %s", to)}, nil
}
