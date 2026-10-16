package channel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDropMetaLines(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"[tokens: prompt=10 completion=5 total=15]", ""},
		{"[compacted: 4000 → 800 tokens]", ""},
		{"[compaction failed: context error]", ""},
		{"Hello!\n[tokens: prompt=10 completion=5 total=15]", "Hello!"},
		{"[tokens: prompt=10 completion=5 total=15]\nSure, here you go.", "Sure, here you go."},
		// Mid-sentence brackets should not be stripped.
		{"Normal reply with [bracketed] word inside.", "Normal reply with [bracketed] word inside."},
		{"Hello!\n\nHow are you?", "Hello!\n\nHow are you?"},
	}
	for _, tt := range tests {
		got := DropMetaLines(tt.in)
		if got != tt.want {
			t.Errorf("DropMetaLines(%q)\n  got  %q\n  want %q", tt.in, got, tt.want)
		}
	}
}

func TestSaveAttachmentSniffsAndSanitizes(t *testing.T) {
	dir := t.TempDir()
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("x", 100)
	a, err := SaveAttachment(dir, "../../evil name.png", "", strings.NewReader(png))
	if err != nil {
		t.Fatal(err)
	}
	if a.MimeType != "image/png" || !a.IsImage() {
		t.Errorf("mime = %q", a.MimeType)
	}
	if filepath.Dir(a.Path) != dir || strings.Contains(filepath.Base(a.Path), " ") {
		t.Errorf("path = %q", a.Path)
	}
	if a.Name != "evil name.png" {
		t.Errorf("name = %q", a.Name)
	}
	if data, _ := os.ReadFile(a.Path); string(data) != png {
		t.Error("content mismatch")
	}
}

func TestSaveAttachmentRejectsOversize(t *testing.T) {
	dir := t.TempDir()
	r := io.LimitReader(zeroReader{}, MaxAttachmentBytes+10)
	if _, err := SaveAttachment(dir, "big.bin", "application/octet-stream", r); err == nil {
		t.Fatal("expected size error")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("partial file left behind: %v", entries)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func TestFetchAttachmentUsesHeaderType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = io.WriteString(w, "jpegdata")
	}))
	defer srv.Close()
	a, err := FetchAttachment(context.Background(), nil, srv.URL+"/x?secret=1", t.TempDir(), "p.jpg", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.MimeType != "image/jpeg" {
		t.Errorf("mime = %q", a.MimeType)
	}
}

func TestKeyContext(t *testing.T) {
	ctx := WithKey(context.Background(), Key{Channel: "discord", Thread: "42"})
	if k, ok := KeyFromContext(ctx); !ok || k.Thread != "42" {
		t.Errorf("got %+v %v", k, ok)
	}
	if _, ok := KeyFromContext(context.Background()); ok {
		t.Error("empty ctx should have no key")
	}
}
