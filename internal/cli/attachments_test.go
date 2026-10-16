package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/channel"
)

func TestBuildUserContent(t *testing.T) {
	if got := buildUserContent("hi", nil, true); got != "hi" {
		t.Fatalf("no attachments: %v", got)
	}

	dir := t.TempDir()
	img := filepath.Join(dir, "a.png")
	doc := filepath.Join(dir, "b.pdf")
	_ = os.WriteFile(img, []byte("PNGDATA"), 0o600)
	_ = os.WriteFile(doc, []byte("PDF"), 0o600)
	atts := []channel.Attachment{
		{Path: img, MimeType: "image/png", Name: "a.png"},
		{Path: doc, MimeType: "application/pdf", Name: "b.pdf"},
	}

	parts, ok := buildUserContent("look", atts, true).([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("want text + 1 image part, got %#v", parts)
	}
	text := parts[0].(map[string]any)["text"].(string)
	for _, want := range []string{"look", img, doc, "application/pdf"} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q: %s", want, text)
		}
	}
	url := parts[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Errorf("image url = %q", url)
	}

	// Image input disabled: plain text with paths only.
	if s, ok := buildUserContent("look", atts, false).(string); !ok || !strings.Contains(s, img) {
		t.Errorf("disabled: %#v", s)
	}
}
