package channel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MaxAttachmentBytes caps a single inbound attachment.
const MaxAttachmentBytes = 25 << 20

// Attachment is a file received from, or destined for, a channel. Path
// is a local file; channels download inbound media before dispatch.
type Attachment struct {
	Path     string
	MimeType string
	Name     string
}

// IsImage reports whether the attachment's MIME type is an image.
func (a Attachment) IsImage() bool { return strings.HasPrefix(a.MimeType, "image/") }

// AttachmentDir returns the directory inbound attachments for channelName
// are saved under: $KROOS_ATTACHMENT_DIR/<channel>, defaulting to
// $TMPDIR/kroos-attachments/<channel>.
func AttachmentDir(channelName string) string {
	root := strings.TrimSpace(os.Getenv("KROOS_ATTACHMENT_DIR"))
	if root == "" {
		root = filepath.Join(os.TempDir(), "kroos-attachments")
	}
	return filepath.Join(root, channelName)
}

var reUnsafeFilename = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// SaveAttachment streams r into dir under a unique, filesystem-safe name
// derived from name. The MIME type is declaredMime when set, otherwise
// sniffed from the content. Files over MaxAttachmentBytes are rejected.
func SaveAttachment(dir, name, declaredMime string, r io.Reader) (Attachment, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Attachment{}, err
	}
	base := strings.Trim(reUnsafeFilename.ReplaceAllString(filepath.Base(name), "_"), "._")
	if base == "" {
		base = "attachment"
		if exts, _ := mime.ExtensionsByType(declaredMime); len(exts) > 0 {
			base += exts[0]
		}
	}
	f, err := os.CreateTemp(dir, fmt.Sprintf("%d-*-%s", time.Now().Unix(), base))
	if err != nil {
		return Attachment{}, err
	}
	path := f.Name()
	head := make([]byte, 512)
	n, _ := io.ReadFull(r, head)
	head = head[:n]
	written, err := f.Write(head)
	if err == nil {
		var rest int64
		rest, err = io.Copy(f, io.LimitReader(r, MaxAttachmentBytes+1-int64(written)))
		if err == nil && int64(written)+rest > MaxAttachmentBytes {
			err = fmt.Errorf("attachment exceeds %d bytes", MaxAttachmentBytes)
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return Attachment{}, err
	}
	mt, _, _ := mime.ParseMediaType(declaredMime)
	if mt == "" || mt == "application/octet-stream" {
		mt, _, _ = mime.ParseMediaType(http.DetectContentType(head))
	}
	if name == "" {
		name = base
	}
	return Attachment{Path: path, MimeType: mt, Name: filepath.Base(name)}, nil
}

// FetchAttachment downloads rawURL with GET and saves it via
// SaveAttachment. Errors never include the URL, which may carry secrets.
func FetchAttachment(ctx context.Context, client *http.Client, rawURL, dir, name, declaredMime string) (Attachment, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Attachment{}, errors.New("fetch attachment: bad request")
	}
	resp, err := client.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return Attachment{}, fmt.Errorf("fetch attachment: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Attachment{}, fmt.Errorf("fetch attachment: status %d", resp.StatusCode)
	}
	if declaredMime == "" {
		declaredMime = resp.Header.Get("Content-Type")
	}
	return SaveAttachment(dir, name, declaredMime, resp.Body)
}

// DropMetaLines removes lines that are purely runner/system annotations
// (surrounded by square brackets, e.g. "[tokens: ...]", "[compacted: ...]").
// These are CLI output, not conversational replies.
func DropMetaLines(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

type keyCtx struct{}

// WithKey returns ctx carrying the conversation key of the current turn,
// so tools (e.g. send_file) can address the originating thread.
func WithKey(ctx context.Context, k Key) context.Context {
	return context.WithValue(ctx, keyCtx{}, k)
}

// KeyFromContext returns the conversation key set by WithKey.
func KeyFromContext(ctx context.Context) (Key, bool) {
	k, ok := ctx.Value(keyCtx{}).(Key)
	return k, ok
}
