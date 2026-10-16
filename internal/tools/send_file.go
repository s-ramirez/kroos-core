package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/s-ramirez/kroos-core/internal/channel"
)

// maxSendFileBytes caps outbound attachments (iMessage/Discord/Telegram
// all reject much larger uploads anyway).
const maxSendFileBytes = 25 << 20

// FileSender delivers an Outbound carrying attachments to its channel.
type FileSender func(ctx context.Context, out channel.Outbound) error

// SendFileTool sends a local file to the conversation the current turn
// came from (iMessage, Discord, or Telegram).
type SendFileTool struct {
	send FileSender
}

var sendFileDefinition = ToolDefinition{
	Name:        "send_file",
	Description: "Send a local file (image, document, audio, video) to the current chat as an attachment.",
	Guidelines: []string{
		"Use send_file to deliver images or files you created or downloaded to the user in the current chat; just mentioning a path does not send it.",
		"Only works in messaging channels (iMessage, Discord, Telegram); it fails in the CLI and scheduled turns.",
	},
	Parameters: JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the local file to send.",
			},
			"caption": map[string]any{
				"type":        "string",
				"description": "Optional text sent with the file.",
			},
		},
		"required": []string{"path"},
	},
}

// SendFileParams is the parameter struct for send_file.
type SendFileParams struct {
	Path    string `json:"path"`
	Caption string `json:"caption"`
}

// NewSendFileTool returns a SendFileTool that delivers via send.
func NewSendFileTool(send FileSender) *SendFileTool {
	return &SendFileTool{send: send}
}

func (t *SendFileTool) Definition() ToolDefinition { return sendFileDefinition }

func (t *SendFileTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var params SendFileParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return errorResult("invalid JSON arguments"), nil
	}
	if strings.TrimSpace(params.Path) == "" {
		return errorResult("'path' is required"), nil
	}
	key, ok := channel.KeyFromContext(ctx)
	if !ok || key.Channel == "" || key.Channel == "cli" || key.Channel == "sched" {
		return errorResult("send_file only works in a messaging channel conversation"), nil
	}
	path, err := filepath.Abs(params.Path)
	if err != nil {
		return errorResultf("bad path: %v", err), nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return errorResultf("cannot read file: %v", err), nil
	}
	if info.IsDir() {
		return errorResult("path is a directory"), nil
	}
	if info.Size() > maxSendFileBytes {
		return errorResultf("file is %d bytes; limit is %d", info.Size(), maxSendFileBytes), nil
	}
	att := channel.Attachment{Path: path, Name: filepath.Base(path), MimeType: detectMime(path)}
	err = t.send(ctx, channel.Outbound{
		Channel:     key.Channel,
		Thread:      key.Thread,
		Content:     params.Caption,
		Attachments: []channel.Attachment{att},
	})
	if err != nil {
		return errorResultf("send failed: %v", err), nil
	}
	return ToolResult{Output: fmt.Sprintf("Sent %s to %s", att.Name, key.Channel)}, nil
}

// detectMime guesses from the extension, falling back to content sniffing.
func detectMime(path string) string {
	if mt := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); mt != "" {
		if base, _, err := mime.ParseMediaType(mt); err == nil {
			return base
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	base, _, _ := mime.ParseMediaType(http.DetectContentType(buf[:n]))
	return base
}
