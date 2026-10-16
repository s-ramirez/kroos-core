package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/channel"
)

func TestSendFileTool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nxx"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got channel.Outbound
	tool := NewSendFileTool(func(_ context.Context, out channel.Outbound) error {
		got = out
		return nil
	})
	raw, _ := json.Marshal(SendFileParams{Path: path, Caption: "here"})

	res, _ := tool.Execute(context.Background(), raw)
	if !res.IsError {
		t.Error("expected error without a conversation key")
	}
	res, _ = tool.Execute(channel.WithKey(context.Background(), channel.Key{Channel: "cli", Thread: "default"}), raw)
	if !res.IsError {
		t.Error("expected error in CLI conversation")
	}

	ctx := channel.WithKey(context.Background(), channel.Key{Channel: "telegram", Thread: "-5"})
	res, _ = tool.Execute(ctx, raw)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if got.Channel != "telegram" || got.Thread != "-5" || got.Content != "here" || len(got.Attachments) != 1 {
		t.Fatalf("outbound = %+v", got)
	}
	if a := got.Attachments[0]; a.MimeType != "image/png" || a.Name != "chart.png" {
		t.Errorf("attachment = %+v", a)
	}

	missing, _ := json.Marshal(SendFileParams{Path: filepath.Join(t.TempDir(), "nope")})
	if res, _ := tool.Execute(ctx, missing); !res.IsError {
		t.Error("expected error for missing file")
	}
}
