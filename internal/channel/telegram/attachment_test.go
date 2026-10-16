package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/s-ramirez/kroos-core/internal/channel"
)

// mediaAPI serves getFile, file downloads, sendPhoto/sendDocument and sendMessage.
type mediaAPI struct {
	mu      sync.Mutex
	methods []string
	params  []map[string]any
	uploads map[string]string // form field -> file body
}

func (f *mediaAPI) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/file/botTOKEN/") {
		_, _ = io.WriteString(w, "PHOTOBYTES")
		return
	}
	method := strings.TrimPrefix(r.URL.Path, "/botTOKEN/")
	f.methods = append(f.methods, method)
	var result any = map[string]any{}
	switch method {
	case "getFile":
		result = map[string]string{"file_path": "photos/file_1.jpg"}
	case "sendPhoto", "sendDocument":
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		for field, fhs := range r.MultipartForm.File {
			fh, _ := fhs[0].Open()
			b, _ := io.ReadAll(fh)
			f.uploads[field] = string(b)
		}
	case "sendMessage":
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		f.params = append(f.params, p)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func TestHandleDownloadsPhotoWithCaption(t *testing.T) {
	t.Setenv("KROOS_ATTACHMENT_DIR", t.TempDir())
	api := &mediaAPI{uploads: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(api.handler))
	defer srv.Close()
	c := newTestChannel(Config{Token: "TOKEN", OwnerIDs: []string{"1"}, APIBase: srv.URL})

	m := tmsg(1, 1, "private", "")
	m.MessageID = 77
	m.Caption = "what is this?"
	m.Photo = []mediaFile{{FileID: "small"}, {FileID: "big", FileSize: 10}}
	out := make(chan channel.Inbound, 1)
	c.handle(context.Background(), m, out)

	msg := <-out
	if msg.Content != "what is this?" || msg.MessageID != "77" || len(msg.Attachments) != 1 {
		t.Fatalf("inbound = %+v", msg)
	}
	a := msg.Attachments[0]
	if a.MimeType != "image/jpeg" || a.Name != "file_1.jpg" {
		t.Errorf("attachment = %+v", a)
	}
	if b, _ := os.ReadFile(a.Path); string(b) != "PHOTOBYTES" {
		t.Errorf("content = %q", b)
	}
}

func TestSendUploadsPhotoAndThreadsGroupReply(t *testing.T) {
	api := &mediaAPI{uploads: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(api.handler))
	defer srv.Close()
	c := newTestChannel(Config{Token: "TOKEN", APIBase: srv.URL})

	img := filepath.Join(t.TempDir(), "x.png")
	_ = os.WriteFile(img, []byte("IMG"), 0o600)
	err := c.Send(context.Background(), channel.Outbound{
		Thread: "-100", Content: "done", ReplyTo: "55",
		Attachments: []channel.Attachment{{Path: img, MimeType: "image/png", Name: "x.png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if api.uploads["photo"] != "IMG" {
		t.Errorf("uploads = %v (methods %v)", api.uploads, api.methods)
	}
	if len(api.params) != 1 {
		t.Fatalf("params = %v", api.params)
	}
	rp, _ := api.params[0]["reply_parameters"].(map[string]any)
	if rp["message_id"] != float64(55) {
		t.Errorf("reply_parameters = %v", api.params[0])
	}

	// Private chats are not threaded.
	api.params = nil
	_ = c.Send(context.Background(), channel.Outbound{Thread: "1", Content: "hi", ReplyTo: "55"})
	if _, ok := api.params[0]["reply_parameters"]; ok {
		t.Error("private reply should not be threaded")
	}
}
