package cli

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/s-ramirez/kroos-core/internal/channel"
)

// maxInlineImageBytes caps images embedded in a request; larger ones are
// referenced by path only.
const maxInlineImageBytes = 5 << 20

// inlineImageTypes are the formats OpenAI-compatible vision APIs accept.
var inlineImageTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

// buildUserContent returns the user message content for a turn. Without
// attachments it is the plain text. Otherwise each attachment is listed by
// local path (so file tools can reach it) and, when imageInput is set,
// supported images are inlined as base64 image_url parts.
func buildUserContent(text string, atts []channel.Attachment, imageInput bool) any {
	if len(atts) == 0 {
		return text
	}
	lines := []string{}
	if strings.TrimSpace(text) != "" {
		lines = append(lines, text)
	}
	var images []any
	for _, a := range atts {
		lines = append(lines, fmt.Sprintf("[attachment %q (%s) saved at %s]", a.Name, mimeOr(a.MimeType), a.Path))
		if !imageInput || !inlineImageTypes[a.MimeType] {
			continue
		}
		if uri, ok := imageDataURI(a); ok {
			images = append(images, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": uri},
			})
		}
	}
	joined := strings.Join(lines, "\n")
	if len(images) == 0 {
		return joined
	}
	return append([]any{map[string]any{"type": "text", "text": joined}}, images...)
}

func imageDataURI(a channel.Attachment) (string, bool) {
	info, err := os.Stat(a.Path)
	if err != nil || info.Size() > maxInlineImageBytes {
		return "", false
	}
	data, err := os.ReadFile(a.Path)
	if err != nil {
		return "", false
	}
	return "data:" + a.MimeType + ";base64," + base64.StdEncoding.EncodeToString(data), true
}

func mimeOr(m string) string {
	if m == "" {
		return "unknown type"
	}
	return m
}
