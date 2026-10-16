// Package imessage implements a channel.Channel backed by the BlueBubbles
// macOS server. Incoming messages arrive via an HTTP webhook that
// BlueBubbles POSTs to; outgoing messages are sent through the BlueBubbles
// REST API.
//
// Only messages from the configured OwnerHandles are forwarded to the
// dispatcher, except in OpenChats where any participant may address the
// agent. In group chats an additional GroupTag must appear in the
// message body (e.g. "@kroos"); that tag is stripped before dispatch.
package imessage

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/s-ramirez/kroos-core/internal/channel"
)

const channelName = "imessage"

const (
	defaultWebhookHost = "127.0.0.1"
	defaultWebhookPort = 8421
	defaultWebhookPath = "/bluebubbles"

	// maxMessageLen is the per-bubble cap for outbound text.
	maxMessageLen = 4000
)

// Config holds all runtime parameters for the iMessage channel.
type Config struct {
	// ServerURL is the base URL of the BlueBubbles server, e.g.
	// "http://192.168.1.10:1234". A missing scheme defaults to http.
	ServerURL string

	// Password is the BlueBubbles API password used for both authenticating
	// incoming webhooks and signing outgoing API requests.
	Password string

	// WebhookPath is the URL path that BlueBubbles should POST events to.
	// Defaults to "/bluebubbles" if empty.
	WebhookPath string

	// WebhookHost is the interface the webhook server binds to. Defaults
	// to 127.0.0.1; set to 0.0.0.0 when BlueBubbles runs on another host.
	WebhookHost string

	// WebhookPort is the local TCP port the webhook HTTP server listens on.
	WebhookPort int

	// WebhookURL is the externally reachable webhook URL registered with
	// BlueBubbles (without the password). Empty derives it from
	// WebhookHost/Port/Path, mapping wildcard binds to localhost.
	WebhookURL string

	// SkipWebhookRegistration disables automatic webhook registration.
	SkipWebhookRegistration bool

	// OwnerHandles is the set of normalized phone numbers or email addresses
	// that are allowed to trigger agent responses. Messages from any other
	// sender are silently dropped.
	OwnerHandles []string

	// GroupTag is the string that must appear in a group-chat message for the
	// agent to respond. Leave empty to respond to all group messages from
	// owner handles (not recommended).
	GroupTag string

	// OpenChats lists group chat GUIDs where any participant (not just
	// owners) may address the agent. GroupTag must be set and present.
	// Populated from profiles with senders: any. DMs are never open.
	OpenChats []string

	// AttachmentDir is where inbound attachments are saved. Empty uses
	// channel.AttachmentDir("imessage").
	AttachmentDir string

	// ReadReceipts marks accepted chats as read and shows a typing
	// indicator while the agent works. Requires the BlueBubbles private API.
	ReadReceipts bool

	// DebounceDelay overrides the default 10-second wait used to group
	// consecutive messages before dispatching. Zero uses the default.
	DebounceDelay time.Duration

	// HomeChannel is the default chat GUID for deliveries that name no
	// recipient (e.g. heartbeat).
	HomeChannel string
}

// msgPart is a single inbound message fragment waiting to be debounced.
type msgPart struct {
	text         string
	sender       string
	messageID    string
	attachments  []channel.Attachment
	participants []string
}

// defaultDebounceDelay is how long to wait for follow-up messages before dispatching.
const defaultDebounceDelay = 10 * time.Second

// Channel implements channel.Channel for iMessage via BlueBubbles.
type Channel struct {
	cfg     Config
	api     *Client
	seenMu  sync.Mutex
	seenIDs map[string]struct{} // message GUIDs already dispatched this session
	pendMu  sync.Mutex
	pending map[string]chan msgPart // per-thread debounce channels

	privMu     sync.RWMutex
	privateAPI bool // private API enabled and helper connected
}

// ConfigFromEnv reads BLUEBUBBLES_* environment variables and returns a
// populated Config along with a status reason. The channel is disabled
// when BLUEBUBBLES_ENABLED is false, when the URL is missing, or when
// the password is missing (in which case an error is returned so the
// caller can surface a warning).
func ConfigFromEnv(getenv func(string) string, parseBool func(name string, def bool) bool) (*Config, string, error) {
	if !parseBool("BLUEBUBBLES_ENABLED", true) {
		return nil, "BLUEBUBBLES_ENABLED=false", nil
	}
	serverURL := strings.TrimSpace(getenv("BLUEBUBBLES_SERVER_URL"))
	if serverURL == "" {
		return nil, "", nil
	}
	password := getenv("BLUEBUBBLES_PASSWORD")
	if password == "" {
		return nil, "", fmt.Errorf("BLUEBUBBLES_SERVER_URL set but BLUEBUBBLES_PASSWORD is empty")
	}
	port := defaultWebhookPort
	if raw := getenv("BLUEBUBBLES_WEBHOOK_PORT"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			port = n
		} else {
			return nil, "", fmt.Errorf("invalid BLUEBUBBLES_WEBHOOK_PORT %q", raw)
		}
	}
	var owners []string
	if raw := getenv("BLUEBUBBLES_ALLOWED_USERS"); raw != "" {
		for _, h := range strings.Split(raw, ",") {
			if h = strings.TrimSpace(h); h != "" {
				owners = append(owners, h)
			}
		}
	}
	return &Config{
		ServerURL:               serverURL,
		Password:                password,
		WebhookPath:             getenv("BLUEBUBBLES_WEBHOOK_PATH"),
		WebhookHost:             strings.TrimSpace(getenv("BLUEBUBBLES_WEBHOOK_HOST")),
		WebhookPort:             port,
		WebhookURL:              strings.TrimSpace(getenv("BLUEBUBBLES_WEBHOOK_URL")),
		SkipWebhookRegistration: !parseBool("BLUEBUBBLES_REGISTER_WEBHOOK", true),
		OwnerHandles:            owners,
		GroupTag:                getenv("BLUEBUBBLES_GROUP_TAG"),
		ReadReceipts:            parseBool("BLUEBUBBLES_READ_RECEIPTS", true),
		HomeChannel:             strings.TrimSpace(getenv("BLUEBUBBLES_HOME_CHANNEL")),
	}, "", nil
}

// New returns a new Channel with the given configuration.
func New(cfg Config) *Channel {
	if cfg.WebhookPath == "" {
		cfg.WebhookPath = defaultWebhookPath
	}
	if !strings.HasPrefix(cfg.WebhookPath, "/") {
		cfg.WebhookPath = "/" + cfg.WebhookPath
	}
	if cfg.WebhookHost == "" {
		cfg.WebhookHost = defaultWebhookHost
	}
	if cfg.WebhookPort == 0 {
		cfg.WebhookPort = defaultWebhookPort
	}
	if cfg.AttachmentDir == "" {
		cfg.AttachmentDir = channel.AttachmentDir(channelName)
	}
	// Normalize all owner handles once at startup.
	normalized := make([]string, 0, len(cfg.OwnerHandles))
	for _, h := range cfg.OwnerHandles {
		normalized = append(normalized, normalizeHandle(h))
	}
	cfg.OwnerHandles = normalized
	return &Channel{
		cfg:     cfg,
		api:     NewClient(cfg.ServerURL, cfg.Password),
		seenIDs: make(map[string]struct{}),
		pending: make(map[string]chan msgPart),
	}
}

func (c *Channel) Name() string { return channelName }

// Home returns the default delivery chat GUID.
func (c *Channel) Home() string { return c.cfg.HomeChannel }

// ListenAddr returns the host:port the webhook server binds to.
func (c *Channel) ListenAddr() string {
	return net.JoinHostPort(c.cfg.WebhookHost, strconv.Itoa(c.cfg.WebhookPort))
}

// webhookURL is the URL registered with BlueBubbles, without the password.
func (c *Channel) webhookURL() string {
	if c.cfg.WebhookURL != "" {
		return c.cfg.WebhookURL
	}
	host := c.cfg.WebhookHost
	switch host {
	case "", "0.0.0.0", "::", "127.0.0.1":
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(c.cfg.WebhookPort)) + c.cfg.WebhookPath
}

// webhookRegisterURL carries the password as a query param: BlueBubbles
// cannot send custom headers, so this is how inbound webhooks authenticate.
func (c *Channel) webhookRegisterURL() string {
	u := c.webhookURL()
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "password=" + url.QueryEscape(c.cfg.Password)
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[imessage] "+format+"\n", args...)
}

// Listen starts the webhook HTTP server, connects to BlueBubbles, and
// forwards qualifying messages onto out. It blocks until ctx is cancelled.
func (c *Channel) Listen(ctx context.Context, out chan<- channel.Inbound) error {
	mux := http.NewServeMux()
	mux.HandleFunc(c.cfg.WebhookPath, c.makeHandler(ctx, out))
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})

	ln, err := net.Listen("tcp", c.ListenAddr())
	if err != nil {
		return fmt.Errorf("imessage webhook: listen: %w", err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	registered := c.connect(ctx)

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if registered {
			if err := c.api.unregisterWebhook(shutCtx, c.webhookRegisterURL()); err != nil {
				logf("unregister webhook: %v", err)
			}
		}
		_ = srv.Shutdown(shutCtx)
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// connect pings the server, records private-API availability, and
// registers the webhook. Failures are logged, not fatal: the webhook
// server keeps running so a later-started BlueBubbles can still deliver.
// It reports whether this process created the webhook registration.
func (c *Channel) connect(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := c.api.ping(cctx); err != nil {
		logf("cannot reach BlueBubbles server: %v", err)
		return false
	}
	if info, err := c.api.serverInfo(cctx); err != nil {
		logf("server info: %v", err)
	} else {
		c.privMu.Lock()
		c.privateAPI = info.PrivateAPI && info.HelperConnected
		c.privMu.Unlock()
		logf("connected (private_api=%v, helper=%v)", info.PrivateAPI, info.HelperConnected)
	}
	if c.cfg.SkipWebhookRegistration {
		return false
	}
	created, err := c.api.registerWebhook(cctx, c.webhookRegisterURL())
	if err != nil {
		logf("register webhook %s: %v", c.webhookURL(), err)
		return false
	}
	if created {
		logf("webhook registered: %s", c.webhookURL())
	} else {
		logf("webhook already registered: %s", c.webhookURL())
	}
	return true
}

func (c *Channel) privateAPIEnabled() bool {
	c.privMu.RLock()
	defer c.privMu.RUnlock()
	return c.privateAPI
}

// chatAction fires a best-effort private-API action (typing/read).
func (c *Channel) chatAction(method, chatGUID, action string) {
	if !c.cfg.ReadReceipts || !c.privateAPIEnabled() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.api.chatAction(ctx, method, chatGUID, action); err != nil {
			logf("%s %s: %v", action, redact(chatGUID), err)
		}
	}()
}

// Send delivers a reply via the BlueBubbles REST API. Attachments are
// uploaded first. The text is stripped of Markdown, meta-lines (e.g.
// "[tokens: ...]") are dropped, and each paragraph is sent as its own
// bubble (split further past 4000 chars). In group chats the first bubble
// is threaded under msg.ReplyTo when the private API is available.
func (c *Channel) Send(ctx context.Context, msg channel.Outbound) error {
	c.chatAction(http.MethodDelete, msg.Thread, "typing")
	for _, att := range msg.Attachments {
		if err := c.api.SendAttachment(ctx, msg.Thread, att); err != nil {
			logf("send attachment error: %v", err)
			return err
		}
	}
	text := channel.DropMetaLines(StripMarkdown(msg.Content))
	if text == "" {
		return nil
	}
	replyTo := ""
	if msg.ReplyTo != "" && strings.Contains(msg.Thread, ";+;") && c.privateAPIEnabled() {
		replyTo = msg.ReplyTo
	}
	for _, chunk := range splitMessage(text, maxMessageLen) {
		if err := c.api.SendText(ctx, msg.Thread, chunk, replyTo); err != nil {
			logf("send error: %v", err)
			return err
		}
		replyTo = ""
	}
	return nil
}

var reParagraph = regexp.MustCompile(`\n\s*\n`)

// splitMessage splits text into paragraph bubbles, hard-splitting any
// paragraph longer than max runes (preferring newline or space boundaries).
func splitMessage(text string, max int) []string {
	var out []string
	for _, p := range reParagraph.Split(text, -1) {
		p = strings.TrimSpace(p)
		for p != "" {
			if utf8.RuneCountInString(p) <= max {
				out = append(out, p)
				break
			}
			cut := runeOffset(p, max)
			if i := strings.LastIndex(p[:cut], "\n"); i > cut/2 {
				cut = i
			} else if i := strings.LastIndex(p[:cut], " "); i > cut/2 {
				cut = i
			}
			out = append(out, strings.TrimSpace(p[:cut]))
			p = strings.TrimSpace(p[cut:])
		}
	}
	return out
}

// runeOffset returns the byte offset of the n-th rune in s.
func runeOffset(s string, n int) int {
	i := 0
	for off := range s {
		if i == n {
			return off
		}
		i++
	}
	return len(s)
}

var (
	rePhoneLog = regexp.MustCompile(`\+?\d{7,15}`)
	reEmailLog = regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.]+`)
)

// redact masks phone numbers and email addresses in log output.
func redact(s string) string {
	return reEmailLog.ReplaceAllString(rePhoneLog.ReplaceAllString(s, "[redacted]"), "[redacted]")
}

// makeHandler returns the http.HandlerFunc that processes BlueBubbles
// webhook events.
func (c *Channel) makeHandler(ctx context.Context, out chan<- channel.Inbound) http.HandlerFunc {
	passwordBytes := []byte(c.cfg.Password)

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Authenticate before reading the body.
		if !checkPassword(r, passwordBytes) {
			logf("webhook: rejected request with wrong or missing password")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		var payload webhookPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			logf("webhook: could not parse JSON: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}

		// Only handle new inbound messages. updated-message events
		// (delivery/read state, edits) would re-trigger the agent.
		if payload.Type != "new-message" || payload.Data == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		m := payload.Data
		if m.IsFromMe {
			w.WriteHeader(http.StatusOK)
			return
		}
		if m.isTapback() {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Deduplicate: BlueBubbles replays recent messages on reconnect.
		// Drop any message whose GUID we have already dispatched this session.
		if m.GUID != "" {
			c.seenMu.Lock()
			_, already := c.seenIDs[m.GUID]
			if !already {
				c.seenIDs[m.GUID] = struct{}{}
			}
			c.seenMu.Unlock()
			if already {
				logf("webhook: dropped duplicate message guid=%s", m.GUID)
				w.WriteHeader(http.StatusOK)
				return
			}
		}

		text := m.text()
		if text == "" && len(m.Attachments) == 0 {
			logf("webhook: dropped message with no text or attachments")
			w.WriteHeader(http.StatusOK)
			return
		}

		sender := normalizeHandle(m.senderHandle())
		if sender == "" {
			logf("webhook: dropped message — could not determine sender handle")
			w.WriteHeader(http.StatusOK)
			return
		}

		chatGUID := m.chatGUID()
		if chatGUID == "" {
			logf("webhook: dropped message — could not determine chatGuid")
			w.WriteHeader(http.StatusOK)
			return
		}

		isGroup := strings.Contains(chatGUID, ";+;")

		// Security gate: only owner handles may trigger the agent, except
		// in OpenChats (group chats bound to a senders: any profile).
		owner := c.isOwner(sender)
		open := isGroup && c.isOpen(chatGUID)
		if !owner && !open {
			logf("webhook: dropped message from non-owner %s (chat=%s)", redact(sender), redact(chatGUID))
			w.WriteHeader(http.StatusOK)
			return
		}
		if open && c.cfg.GroupTag == "" {
			logf("webhook: dropped message in open chat %s — GroupTag not configured", redact(chatGUID))
			w.WriteHeader(http.StatusOK)
			return
		}

		// In group chats require the GroupTag. Checked before attachment
		// downloads so untagged group traffic is never fetched.
		if isGroup && c.cfg.GroupTag != "" {
			if !strings.Contains(text, c.cfg.GroupTag) {
				w.WriteHeader(http.StatusOK)
				return
			}
			// Strip the tag from the forwarded content.
			text = strings.TrimSpace(strings.ReplaceAll(text, c.cfg.GroupTag, ""))
			if text == "" && len(m.Attachments) == 0 {
				w.WriteHeader(http.StatusOK)
				return
			}
		}

		var participants []string
		if isGroup {
			// Exclude the sender from the roster — callers typically want
			// "who else is here" for DM fan-out or mentions.
			for _, h := range m.participants() {
				if h == sender {
					continue
				}
				participants = append(participants, h)
			}
		}

		logf("webhook: accepted message (chat=%s, group=%v, participants=%d, chars=%d, attachments=%d)",
			redact(chatGUID), isGroup, len(participants), len(text), len(m.Attachments))
		c.chatAction(http.MethodPost, chatGUID, "read")

		finish := func(text string, atts []channel.Attachment) {
			if text == "" && len(atts) == 0 {
				return
			}
			// Multi-user chats: tell the model who is speaking. Applied per
			// part so debounced messages from different senders stay attributed.
			if open && text != "" {
				role := ""
				if owner {
					role = " (owner)"
				}
				text = fmt.Sprintf("[%s%s]: %s", sender, role, text)
			}
			c.enqueue(ctx, chatGUID, msgPart{
				text: text, sender: sender, messageID: m.GUID,
				attachments: atts, participants: participants,
			}, out)
		}
		if len(m.Attachments) == 0 {
			finish(text, nil)
		} else {
			// Download off the webhook request so BlueBubbles isn't held up.
			go func() { finish(text, c.downloadAll(ctx, m.Attachments)) }()
		}
		w.WriteHeader(http.StatusOK)
	}
}

// downloadAll saves each attachment locally, skipping failures.
func (c *Channel) downloadAll(ctx context.Context, atts []attachmentData) []channel.Attachment {
	var saved []channel.Attachment
	for _, att := range atts {
		if att.GUID == "" {
			continue
		}
		dctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		a, err := c.api.downloadAttachment(dctx, c.cfg.AttachmentDir, att)
		cancel()
		if err != nil {
			logf("attachment %s: %v", att.GUID, err)
			continue
		}
		saved = append(saved, a)
	}
	return saved
}

// enqueue routes an inbound message through the per-thread debounce worker.
// Messages that arrive within debounceDelay of each other are accumulated and
// dispatched as a single combined message.
func (c *Channel) enqueue(ctx context.Context, chatGUID string, part msgPart, out chan<- channel.Inbound) {
	c.pendMu.Lock()
	ch, exists := c.pending[chatGUID]
	if !exists {
		ch = make(chan msgPart, 16)
		c.pending[chatGUID] = ch
		go c.debounceWorker(ctx, chatGUID, ch, out)
	}
	c.pendMu.Unlock()

	select {
	case ch <- part:
	case <-ctx.Done():
	}
}

func (c *Channel) debounce() time.Duration {
	if c.cfg.DebounceDelay > 0 {
		return c.cfg.DebounceDelay
	}
	return defaultDebounceDelay
}

// debounceWorker accumulates message parts for chatGUID, then dispatches them
// as a single Inbound after the configured debounce delay of silence.
func (c *Channel) debounceWorker(ctx context.Context, chatGUID string, ch <-chan msgPart, out chan<- channel.Inbound) {
	defer func() {
		c.pendMu.Lock()
		if c.pending[chatGUID] == ch {
			delete(c.pending, chatGUID)
		}
		c.pendMu.Unlock()
	}()

	var parts []string
	var sender, messageID string
	var attachments []channel.Attachment
	var participants []string
	var timer <-chan time.Time

	for {
		select {
		case part := <-ch:
			if sender == "" {
				sender = part.sender
			}
			if len(part.participants) > 0 {
				participants = part.participants
			}
			if part.text != "" {
				parts = append(parts, part.text)
			}
			if part.messageID != "" {
				messageID = part.messageID
			}
			attachments = append(attachments, part.attachments...)
			timer = time.After(c.debounce())
		case <-timer:
			body := strings.Join(parts, "\n")
			content := body
			if len(participants) > 0 {
				content = fmt.Sprintf("[group participants: %s]\n%s", strings.Join(participants, ", "), body)
			}
			c.chatAction(http.MethodPost, chatGUID, "typing")
			select {
			case <-ctx.Done():
			case out <- channel.Inbound{
				Channel:      channelName,
				Thread:       chatGUID,
				Sender:       sender,
				Content:      content,
				MessageID:    messageID,
				Attachments:  attachments,
				Kind:         channel.KindUser,
				ReceivedAt:   time.Now(),
				Participants: participants,
			}:
			}
			return
		case <-ctx.Done():
			return
		}
	}
}

func (c *Channel) isOpen(chatGUID string) bool {
	for _, g := range c.cfg.OpenChats {
		if g == chatGUID {
			return true
		}
	}
	return false
}

func (c *Channel) isOwner(handle string) bool {
	for _, h := range c.cfg.OwnerHandles {
		if h == handle {
			return true
		}
	}
	return false
}

// checkPassword verifies the request carries the correct BlueBubbles password.
// It checks, in order: the "password" query param, "guid" query param, and
// the "x-password" / "x-guid" / "x-bluebubbles-guid" headers.
// The comparison is constant-time to prevent timing attacks.
func checkPassword(r *http.Request, want []byte) bool {
	candidates := []string{
		r.URL.Query().Get("password"),
		r.URL.Query().Get("guid"),
		r.Header.Get("x-password"),
		r.Header.Get("x-guid"),
		r.Header.Get("x-bluebubbles-guid"),
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(c), want) == 1 {
			return true
		}
	}
	return false
}

// webhookPayload is the JSON envelope BlueBubbles POSTs for each event.
type webhookPayload struct {
	Type string       `json:"type"`
	Data *messageData `json:"data"`
}

type messageData struct {
	GUID     string      `json:"guid"`
	Text     string      `json:"text"`
	IsFromMe bool        `json:"isFromMe"`
	Handle   *handleData `json:"handle"`
	Chats    []chatData  `json:"chats"`
	// AssociatedMessageType is an int code (2000-2005 add, 3000-3005
	// remove) on older servers and a string ("love", "-love") on newer ones.
	AssociatedMessageType json.RawMessage  `json:"associatedMessageType"`
	Attachments           []attachmentData `json:"attachments"`
}

type handleData struct {
	Address string `json:"address"`
}

type chatData struct {
	GUID         string       `json:"guid"`
	Participants []handleData `json:"participants"`
}

type attachmentData struct {
	GUID         string `json:"guid"`
	MimeType     string `json:"mimeType"`
	TransferName string `json:"transferName"`
}

// isTapback reports whether the message is a tapback reaction.
func (m *messageData) isTapback() bool {
	raw := m.AssociatedMessageType
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var code int
	if err := json.Unmarshal(raw, &code); err == nil {
		return (code >= 2000 && code <= 2005) || (code >= 3000 && code <= 3005)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s) != ""
	}
	return false
}

// text returns the message body with attachment placeholders removed.
func (m *messageData) text() string {
	return strings.TrimSpace(strings.ReplaceAll(m.Text, "￼", ""))
}

func (m *messageData) chatGUID() string {
	if len(m.Chats) > 0 {
		return m.Chats[0].GUID
	}
	return ""
}

func (m *messageData) senderHandle() string {
	if m.Handle == nil {
		return ""
	}
	return m.Handle.Address
}

// participants returns the normalized handles of every group member listed
// on the message's chat entry. Order is preserved; duplicates are removed.
func (m *messageData) participants() []string {
	if len(m.Chats) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, p := range m.Chats[0].Participants {
		h := normalizeHandle(p.Address)
		if h == "" {
			continue
		}
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	return out
}

// rePhone matches a string that looks like a phone number (digits, spaces,
// dashes, parens, optional leading +).
var rePhone = regexp.MustCompile(`^\+?[\d\s\-().]+$`)

// normalizeHandle strips service prefixes and normalizes the formatting of an
// iMessage handle so that comparisons with OwnerHandles are reliable.
func normalizeHandle(h string) string {
	// Strip known service prefixes.
	for _, prefix := range []string{"imessage:", "sms:", "iMessage:", "SMS:"} {
		h = strings.TrimPrefix(h, prefix)
	}
	h = strings.TrimSpace(h)

	// Emails: lowercase only.
	if strings.Contains(h, "@") {
		return strings.ToLower(h)
	}

	// Phone numbers: remove everything except digits and leading +.
	if rePhone.MatchString(h) {
		var b strings.Builder
		for i, r := range h {
			if r == '+' && i == 0 {
				b.WriteRune(r)
				continue
			}
			if r >= '0' && r <= '9' {
				b.WriteRune(r)
			}
		}
		return b.String()
	}

	return h
}
