package imessage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/s-ramirez/kroos-core/internal/channel"
)

// Client is a minimal BlueBubbles REST client. Every request carries the
// server password as a URL-encoded query parameter. Exported for the
// imessage_send tool.
type Client struct {
	base     string
	password string
	hc       *http.Client
}

// NewClient returns a Client for the BlueBubbles server at serverURL.
func NewClient(serverURL, password string) *Client {
	return &Client{
		base:     normalizeServerURL(serverURL),
		password: password,
		hc:       &http.Client{Timeout: 2 * time.Minute},
	}
}

// normalizeServerURL adds a missing scheme and trims trailing slashes.
func normalizeServerURL(raw string) string {
	v := strings.TrimSpace(raw)
	lower := strings.ToLower(v)
	if v != "" && !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		v = "http://" + v
	}
	return strings.TrimRight(v, "/")
}

func (a *Client) url(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return a.base + path + sep + "password=" + url.QueryEscape(a.password)
}

// apiResponse is the common BlueBubbles response envelope.
type apiResponse struct {
	Status  int             `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// do performs a JSON request and decodes the envelope's data into out
// (if non-nil). Errors never include the password-bearing URL.
func (a *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	return a.doRaw(ctx, method, path, body, "application/json", out)
}

func (a *Client) doRaw(ctx context.Context, method, path string, body io.Reader, contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, a.url(path), body)
	if err != nil {
		return fmt.Errorf("bluebubbles %s %s: build request failed", method, path)
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		return fmt.Errorf("bluebubbles %s %s: %v", method, path, scrubURLError(err))
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := strings.TrimSpace(string(rb))
		if len(snippet) > 512 {
			snippet = snippet[:512]
		}
		return fmt.Errorf("bluebubbles %s %s: status %d: %s", method, path, resp.StatusCode, snippet)
	}
	if out == nil {
		return nil
	}
	var env apiResponse
	if err := json.Unmarshal(rb, &env); err != nil {
		return fmt.Errorf("bluebubbles %s %s: decode: %w", method, path, err)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// scrubURLError strips the request URL (which carries the password) from
// *url.Error values.
func scrubURLError(err error) error {
	if ue, ok := err.(*url.Error); ok {
		return fmt.Errorf("%s: %v", ue.Op, ue.Err)
	}
	return err
}

func tempGUID() string { return fmt.Sprintf("temp-%d", time.Now().UnixNano()) }

type serverInfo struct {
	PrivateAPI      bool `json:"private_api"`
	HelperConnected bool `json:"helper_connected"`
}

func (a *Client) ping(ctx context.Context) error {
	return a.do(ctx, http.MethodGet, "/api/v1/ping", nil, nil)
}

func (a *Client) serverInfo(ctx context.Context) (serverInfo, error) {
	var info serverInfo
	err := a.do(ctx, http.MethodGet, "/api/v1/server/info", nil, &info)
	return info, err
}

type webhookEntry struct {
	ID  json.Number `json:"id"`
	URL string      `json:"url"`
}

func (a *Client) listWebhooks(ctx context.Context) ([]webhookEntry, error) {
	var hooks []webhookEntry
	err := a.do(ctx, http.MethodGet, "/api/v1/webhook", nil, &hooks)
	return hooks, err
}

// registerWebhook registers hookURL for new-message events unless an
// identical registration already exists (e.g. after an unclean shutdown).
func (a *Client) registerWebhook(ctx context.Context, hookURL string) (created bool, err error) {
	hooks, err := a.listWebhooks(ctx)
	if err != nil {
		return false, err
	}
	for _, h := range hooks {
		if h.URL == hookURL {
			return false, nil
		}
	}
	body := map[string]any{"url": hookURL, "events": []string{"new-message"}}
	return true, a.do(ctx, http.MethodPost, "/api/v1/webhook", body, nil)
}

// unregisterWebhook removes every registration matching hookURL.
func (a *Client) unregisterWebhook(ctx context.Context, hookURL string) error {
	hooks, err := a.listWebhooks(ctx)
	if err != nil {
		return err
	}
	for _, h := range hooks {
		if h.URL != hookURL || h.ID == "" {
			continue
		}
		if err := a.do(ctx, http.MethodDelete, "/api/v1/webhook/"+url.PathEscape(h.ID.String()), nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// SendText posts text to chatGUID. A non-empty replyTo threads the
// message under that message GUID (requires the private API).
func (a *Client) SendText(ctx context.Context, chatGUID, text, replyTo string) error {
	body := map[string]any{
		"chatGuid": chatGUID,
		"tempGuid": tempGUID(),
		"message":  text,
	}
	if replyTo != "" {
		body["method"] = "private-api"
		body["selectedMessageGuid"] = replyTo
		body["partIndex"] = 0
	}
	return a.do(ctx, http.MethodPost, "/api/v1/message/text", body, nil)
}

// SendAttachment uploads a local file to chatGUID.
func (a *Client) SendAttachment(ctx context.Context, chatGUID string, att channel.Attachment) error {
	f, err := os.Open(att.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	name := att.Name
	if name == "" {
		name = filepath.Base(att.Path)
	}

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			for k, v := range map[string]string{"chatGuid": chatGUID, "tempGuid": tempGUID(), "name": name} {
				if err := mw.WriteField(k, v); err != nil {
					return err
				}
			}
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="attachment"; filename=%q`, name))
			ct := att.MimeType
			if ct == "" {
				ct = "application/octet-stream"
			}
			h.Set("Content-Type", ct)
			part, err := mw.CreatePart(h)
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, f); err != nil {
				return err
			}
			return mw.Close()
		}()
		pw.CloseWithError(err)
	}()
	return a.doRaw(ctx, http.MethodPost, "/api/v1/message/attachment", pr, mw.FormDataContentType(), nil)
}

// ResolveChat returns the GUID of the existing chat whose identifier is
// handle (a phone number or email), or "" when none exists. Participant
// membership is deliberately not used: the same contact appears in DMs
// and groups, and matching on it could leak a DM into a group.
func (a *Client) ResolveChat(ctx context.Context, handle string) (string, error) {
	want := normalizeHandle(handle)
	var chats []struct {
		GUID           string `json:"guid"`
		ChatIdentifier string `json:"chatIdentifier"`
	}
	if err := a.do(ctx, http.MethodPost, "/api/v1/chat/query", map[string]any{"limit": 1000, "offset": 0}, &chats); err != nil {
		return "", err
	}
	for _, c := range chats {
		if c.GUID != "" && normalizeHandle(c.ChatIdentifier) == want {
			return c.GUID, nil
		}
	}
	return "", nil
}

// CreateChat starts a new DM with handle by sending text as its first
// message.
func (a *Client) CreateChat(ctx context.Context, handle, text string) error {
	body := map[string]any{"addresses": []string{handle}, "message": text, "tempGuid": tempGUID()}
	return a.do(ctx, http.MethodPost, "/api/v1/chat/new", body, nil)
}

// chatAction calls a private-API chat endpoint (typing, read).
func (a *Client) chatAction(ctx context.Context, method, chatGUID, action string) error {
	return a.do(ctx, method, "/api/v1/chat/"+url.PathEscape(chatGUID)+"/"+action, nil, nil)
}

// downloadAttachment saves an inbound attachment into dir.
func (a *Client) downloadAttachment(ctx context.Context, dir string, att attachmentData) (channel.Attachment, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.url("/api/v1/attachment/"+url.PathEscape(att.GUID)+"/download"), nil)
	if err != nil {
		return channel.Attachment{}, fmt.Errorf("attachment: build request failed")
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		return channel.Attachment{}, fmt.Errorf("attachment: %v", scrubURLError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return channel.Attachment{}, fmt.Errorf("attachment: status %d", resp.StatusCode)
	}
	mt := att.MimeType
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "image/") {
		// BlueBubbles may convert images (e.g. HEIC → JPEG) on download.
		mt = ct
	}
	return channel.SaveAttachment(dir, att.TransferName, mt, resp.Body)
}
