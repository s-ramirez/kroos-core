// Package telegram implements a channel.Channel backed by a Telegram bot.
// Incoming messages arrive by long-polling the Bot API's getUpdates (no
// public webhook URL needed); replies go out via sendMessage.
//
// Only messages from the configured OwnerIDs are forwarded, except in
// OpenChats where anyone may address the bot. In group chats the bot
// must be @mentioned, replied to, or the GroupTag must appear; the
// marker is stripped before dispatch. Private chats bypass that check
// but are owner-only.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/s-ramirez/kroos-core/internal/channel"
)

const channelName = "telegram"

// DefaultAPIBase is the Telegram Bot API root.
const DefaultAPIBase = "https://api.telegram.org"

// maxMessageLen is Telegram's per-message limit (4096 UTF-16 units).
// Chunking by UTF-8 bytes stays under it for any text.
const maxMessageLen = 4096

// Config holds all runtime parameters for the Telegram channel.
type Config struct {
	// Token is the bot token from @BotFather.
	Token string

	// OwnerIDs is the set of Telegram user IDs allowed to trigger the
	// agent. Messages from anyone else are dropped (outside OpenChats).
	OwnerIDs []string

	// GroupTag is an optional literal (e.g. "@kroos") that, besides an
	// @mention of the bot or a reply to it, addresses the bot in groups.
	GroupTag string

	// OpenChats lists group chat IDs where any member may address the
	// bot. Populated from profiles with senders: any.
	OpenChats []string

	// AllowAll lets any user address the bot, private chats included.
	// Mention/tag gating in groups still applies.
	AllowAll bool

	// HomeChannel is the default chat ID for deliveries that name no
	// recipient (e.g. heartbeat); HomeThreadID optionally picks a forum topic.
	HomeChannel  string
	HomeThreadID string

	// APIBase overrides DefaultAPIBase (tests).
	APIBase string

	// PollTimeout is the getUpdates long-poll duration. Zero = 30s.
	PollTimeout time.Duration
}

// ConfigFromEnv reads TELEGRAM_* environment variables. Returns
// (nil, "", nil) when no token is set, and a non-empty reason when
// explicitly disabled via TELEGRAM_ENABLED=false.
func ConfigFromEnv(getenv func(string) string, parseBool func(name string, def bool) bool) (*Config, string, error) {
	if !parseBool("TELEGRAM_ENABLED", true) {
		return nil, "TELEGRAM_ENABLED=false", nil
	}
	token := strings.TrimSpace(getenv("TELEGRAM_BOT_TOKEN"))
	if token == "" {
		return nil, "", nil
	}
	var owners []string
	if raw := getenv("TELEGRAM_ALLOWED_USERS"); raw != "" {
		for _, id := range strings.Split(raw, ",") {
			if id = strings.TrimSpace(id); id != "" {
				if _, err := strconv.ParseInt(id, 10, 64); err != nil {
					return nil, "", fmt.Errorf("invalid TELEGRAM_ALLOWED_USERS entry %q (want numeric user ID)", id)
				}
				owners = append(owners, id)
			}
		}
	}
	return &Config{
		Token:        token,
		OwnerIDs:     owners,
		GroupTag:     getenv("TELEGRAM_GROUP_TAG"),
		AllowAll:     parseBool("TELEGRAM_ALLOW_ALL_USERS", false),
		HomeChannel:  strings.TrimSpace(getenv("TELEGRAM_HOME_CHANNEL")),
		HomeThreadID: strings.TrimSpace(getenv("TELEGRAM_HOME_CHANNEL_THREAD_ID")),
	}, "", nil
}

// Channel implements channel.Channel for Telegram.
type Channel struct {
	cfg         Config
	client      *http.Client
	botID       int64
	botUsername string
}

// New returns a new Channel with the given configuration.
func New(cfg Config) *Channel {
	if cfg.APIBase == "" {
		cfg.APIBase = DefaultAPIBase
	}
	if cfg.PollTimeout == 0 {
		cfg.PollTimeout = 30 * time.Second
	}
	return &Channel{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.PollTimeout + 15*time.Second},
	}
}

func (c *Channel) Name() string { return channelName }

// Home returns the default delivery thread: "chatID" or "chatID:topicID".
// Topic 1 is a forum's General topic, which the Bot API addresses by
// omitting message_thread_id.
func (c *Channel) Home() string {
	if c.cfg.HomeChannel == "" || c.cfg.HomeThreadID == "" || c.cfg.HomeThreadID == "1" {
		return c.cfg.HomeChannel
	}
	return c.cfg.HomeChannel + ":" + c.cfg.HomeThreadID
}

// splitThread splits "chatID:topicID" into its parts.
func splitThread(thread string) (chatID, topicID string) {
	chatID, topicID, _ = strings.Cut(thread, ":")
	return chatID, topicID
}

// Listen resolves the bot identity, then long-polls getUpdates and
// forwards qualifying messages onto out until ctx is cancelled.
func (c *Channel) Listen(ctx context.Context, out chan<- channel.Inbound) error {
	var me user
	if err := c.call(ctx, "getMe", nil, &me); err != nil {
		return fmt.Errorf("telegram: getMe: %w", err)
	}
	c.botID, c.botUsername = me.ID, me.Username
	logf("connected as @%s (%d)", c.botUsername, c.botID)
	c.registerCommands(ctx)

	var offset int64
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var updates []update
		err := c.call(ctx, "getUpdates", map[string]any{
			"offset":          offset,
			"timeout":         int(c.cfg.PollTimeout / time.Second),
			"allowed_updates": []string{"message"},
		}, &updates)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			logf("getUpdates error: %v (retry in %s)", err, backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		for _, u := range updates {
			offset = u.UpdateID + 1
			if u.Message == nil {
				continue
			}
			c.handle(ctx, u.Message, out)
		}
	}
}

func (c *Channel) handle(ctx context.Context, m *message, out chan<- channel.Inbound) {
	if m.From == nil || m.From.IsBot {
		return
	}
	text, reason := c.accept(m)
	if reason != "" {
		logf("dropped message from %d (chat=%d): %s", m.From.ID, m.Chat.ID, reason)
		return
	}
	media := m.media()
	if text == "" && len(media) == 0 {
		return
	}
	chatID := strconv.FormatInt(m.Chat.ID, 10)
	logf("accepted message from %d (chat=%s, type=%s, chars=%d, attachments=%d)", m.From.ID, chatID, m.Chat.Type, len(text), len(media))
	atts := c.download(ctx, media)
	if text == "" && len(atts) == 0 {
		return
	}
	msg := channel.Inbound{
		Channel:     channelName,
		Thread:      chatID,
		Sender:      strconv.FormatInt(m.From.ID, 10),
		MessageID:   strconv.FormatInt(m.MessageID, 10),
		Attachments: atts,
		Content:     text,
		Kind:        channel.KindUser,
		ReceivedAt:  time.Unix(m.Date, 0),
	}
	select {
	case <-ctx.Done():
	case out <- msg:
	}
}

// accept applies sender and mention gating. It returns the cleaned text
// to dispatch, or a non-empty drop reason.
func (c *Channel) accept(m *message) (string, string) {
	text := strings.TrimSpace(m.Text)
	if text == "" {
		text = strings.TrimSpace(m.Caption)
	}
	hasMedia := len(m.media()) > 0
	if text == "" && !hasMedia {
		return "", ""
	}
	isPrivate := m.Chat.Type == "private"
	owner := c.isOwner(m.From.ID)
	open := !isPrivate && c.isOpen(m.Chat.ID)
	if !owner && !open && !c.cfg.AllowAll {
		return "", "non-owner"
	}
	if isPrivate {
		return text, ""
	}

	handle := ""
	if c.botUsername != "" {
		handle = "@" + c.botUsername
	}
	mentioned := handle != "" && containsFold(text, handle)
	repliedTo := m.ReplyTo != nil && m.ReplyTo.From != nil && c.botID != 0 && m.ReplyTo.From.ID == c.botID
	tagPresent := c.cfg.GroupTag != "" && strings.Contains(text, c.cfg.GroupTag)
	if !mentioned && !repliedTo && !tagPresent {
		return "", "missing mention/tag"
	}
	if mentioned {
		text = replaceFold(text, handle, "")
	}
	if tagPresent {
		text = strings.ReplaceAll(text, c.cfg.GroupTag, "")
	}
	text = strings.TrimSpace(text)
	if text == "" && !hasMedia {
		return "", ""
	}
	if open && text != "" {
		name := m.From.Username
		if name == "" {
			name = strings.TrimSpace(m.From.FirstName + " " + m.From.LastName)
		}
		role := ""
		if owner {
			role = " (owner)"
		}
		text = fmt.Sprintf("[%s%s]: %s", name, role, text)
	}
	return text, ""
}

// registerCommands sets kroos's command menu and clears scoped menus left
// by previous software using the same bot (they'd override the default).
func (c *Channel) registerCommands(ctx context.Context) {
	for _, scope := range []string{"all_private_chats", "all_group_chats", "all_chat_administrators"} {
		if err := c.call(ctx, "deleteMyCommands", map[string]any{"scope": map[string]string{"type": scope}}, nil); err != nil {
			logf("clear %s commands: %v", scope, err)
		}
	}
	var cmds []map[string]string
	for _, bc := range channel.BotCommands {
		desc := bc.Description
		if bc.Arg != "" {
			desc += " (/" + bc.Name + " <" + bc.Arg + ">)"
		}
		cmds = append(cmds, map[string]string{"command": bc.Name, "description": desc})
	}
	if err := c.call(ctx, "setMyCommands", map[string]any{"commands": cmds}, nil); err != nil {
		logf("register commands: %v", err)
		return
	}
	logf("registered %d commands", len(cmds))
}

// Send delivers a reply as plain text (no parse_mode, so Markdown in the
// model output can't trigger entity-parsing errors). Runner meta-lines
// like "[tokens: ...]" are filtered.
// Attachments are uploaded first; in groups (negative chat IDs) the first
// text chunk replies to msg.ReplyTo.
func (c *Channel) Send(ctx context.Context, msg channel.Outbound) error {
	for _, att := range msg.Attachments {
		if err := c.sendFile(ctx, msg.Thread, att); err != nil {
			logf("send attachment error: %v", err)
			return err
		}
	}
	text := channel.DropMetaLines(msg.Content)
	if text == "" {
		return nil
	}
	replyTo := ""
	if strings.HasPrefix(msg.Thread, "-") {
		replyTo = msg.ReplyTo
	}
	if err := sendText(ctx, c.client, c.cfg.APIBase, c.cfg.Token, msg.Thread, text, replyTo); err != nil {
		logf("send error: %v", err)
		return err
	}
	return nil
}

// SendMessage posts text to chatID, chunking to Telegram's size limit.
// Exported for the telegram_send tool.
func SendMessage(ctx context.Context, client *http.Client, apiBase, token, chatID, text string) error {
	return sendText(ctx, client, apiBase, token, chatID, text, "")
}

// sendText posts text to thread ("chatID" or "chatID:topicID").
func sendText(ctx context.Context, client *http.Client, apiBase, token, thread, text, replyTo string) error {
	chatID, topicID := splitThread(thread)
	for _, chunk := range chunkMessage(text, maxMessageLen) {
		params := map[string]any{"chat_id": chatID, "text": chunk}
		if id, err := strconv.ParseInt(topicID, 10, 64); err == nil {
			params["message_thread_id"] = id
		}
		if id, err := strconv.ParseInt(replyTo, 10, 64); err == nil {
			params["reply_parameters"] = map[string]any{"message_id": id, "allow_sending_without_reply": true}
			replyTo = ""
		}
		if err := callAPI(ctx, client, apiBase, token, "sendMessage", params, nil); err != nil {
			return err
		}
	}
	return nil
}

// photoTypes are formats sendPhoto accepts; others go via sendDocument.
var photoTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}

// maxPhotoBytes is sendPhoto's upload limit.
const maxPhotoBytes = 10 << 20

// sendFile uploads a local file as a photo (when possible) or document.
func (c *Channel) sendFile(ctx context.Context, thread string, att channel.Attachment) error {
	chatID, topicID := splitThread(thread)
	method, field := "sendDocument", "document"
	if info, err := os.Stat(att.Path); err == nil && photoTypes[att.MimeType] && info.Size() <= maxPhotoBytes {
		method, field = "sendPhoto", "photo"
	}
	f, err := os.Open(att.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	name := att.Name
	if name == "" {
		name = path.Base(att.Path)
	}

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			if err := mw.WriteField("chat_id", chatID); err != nil {
				return err
			}
			if topicID != "" {
				if err := mw.WriteField("message_thread_id", topicID); err != nil {
					return err
				}
			}
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, name))
			h.Set("Content-Type", "application/octet-stream")
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
	u := strings.TrimRight(c.cfg.APIBase, "/") + "/bot" + c.cfg.Token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, pr)
	if err != nil {
		return fmt.Errorf("%s: build request failed", method)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return doRequest(c.client, method, req, nil)
}

// maxDownloadBytes is the Bot API's getFile download limit.
const maxDownloadBytes = 20 << 20

// download fetches each media item via getFile, skipping failures and
// files over the Bot API's 20 MB download limit.
func (c *Channel) download(ctx context.Context, media []mediaFile) []channel.Attachment {
	var saved []channel.Attachment
	for _, m := range media {
		if m.FileSize > maxDownloadBytes {
			logf("attachment too large (%d bytes), skipped", m.FileSize)
			continue
		}
		dctx, cancel := context.WithTimeout(ctx, time.Minute)
		var f struct {
			FilePath string `json:"file_path"`
		}
		err := c.call(dctx, "getFile", map[string]any{"file_id": m.FileID}, &f)
		if err == nil && f.FilePath == "" {
			err = errors.New("getFile: empty file_path")
		}
		var att channel.Attachment
		if err == nil {
			name := m.FileName
			if name == "" {
				name = path.Base(f.FilePath)
			}
			u := strings.TrimRight(c.cfg.APIBase, "/") + "/file/bot" + c.cfg.Token + "/" + f.FilePath
			att, err = channel.FetchAttachment(dctx, c.client, u, channel.AttachmentDir(channelName), name, m.MimeType)
		}
		cancel()
		if err != nil {
			logf("attachment: %v", err)
			continue
		}
		saved = append(saved, att)
	}
	return saved
}

func (c *Channel) call(ctx context.Context, method string, params map[string]any, result any) error {
	return callAPI(ctx, c.client, c.cfg.APIBase, c.cfg.Token, method, params, result)
}

// callAPI POSTs params as JSON to a Bot API method and decodes the
// "result" field into result (if non-nil).
func callAPI(ctx context.Context, client *http.Client, apiBase, token, method string, params map[string]any, result any) error {
	if params == nil {
		params = map[string]any{}
	}
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	u := strings.TrimRight(apiBase, "/") + "/bot" + token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return doRequest(client, method, req, result)
}

// doRequest sends req and decodes the Bot API envelope. Errors never
// include the token-bearing URL.
func doRequest(client *http.Client, method string, req *http.Request, result any) error {
	resp, err := client.Do(req)
	if err != nil {
		// Don't leak the token embedded in the URL.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return fmt.Errorf("%s: %w", method, uerr.Err)
		}
		return fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("%s: read body: %w", method, err)
	}
	var env struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%s: status %d: bad JSON", method, resp.StatusCode)
	}
	if !env.OK {
		return fmt.Errorf("%s: status %d: %s", method, resp.StatusCode, env.Description)
	}
	if result != nil {
		if err := json.Unmarshal(env.Result, result); err != nil {
			return fmt.Errorf("%s: decode result: %w", method, err)
		}
	}
	return nil
}

func (c *Channel) isOwner(id int64) bool {
	s := strconv.FormatInt(id, 10)
	for _, o := range c.cfg.OwnerIDs {
		if o == s {
			return true
		}
	}
	return false
}

func (c *Channel) isOpen(chatID int64) bool {
	s := strconv.FormatInt(chatID, 10)
	for _, id := range c.cfg.OpenChats {
		if id == s {
			return true
		}
	}
	return false
}

// Bot API types (only the fields we use).
type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message"`
}

type message struct {
	MessageID int64       `json:"message_id"`
	From      *user       `json:"from"`
	Chat      chat        `json:"chat"`
	Date      int64       `json:"date"`
	Text      string      `json:"text"`
	Caption   string      `json:"caption"`
	ReplyTo   *message    `json:"reply_to_message"`
	Photo     []mediaFile `json:"photo"`
	Document  *mediaFile  `json:"document"`
	Voice     *mediaFile  `json:"voice"`
	Audio     *mediaFile  `json:"audio"`
	Video     *mediaFile  `json:"video"`
}

// mediaFile covers PhotoSize, Document, Voice, Audio and Video.
type mediaFile struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

// media returns the downloadable files on m. For photos only the largest
// size is used.
func (m *message) media() []mediaFile {
	var out []mediaFile
	if n := len(m.Photo); n > 0 {
		p := m.Photo[n-1]
		p.MimeType = "image/jpeg"
		out = append(out, p)
	}
	for _, f := range []*mediaFile{m.Document, m.Voice, m.Audio, m.Video} {
		if f != nil && f.FileID != "" {
			out = append(out, *f)
		}
	}
	return out
}

type user struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // private | group | supergroup | channel
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[telegram] "+format+"\n", args...)
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// replaceFold removes every case-insensitive occurrence of old (ASCII
// usernames, so byte offsets from the lowered string are valid).
func replaceFold(s, old, repl string) string {
	lower, lold := strings.ToLower(s), strings.ToLower(old)
	if len(lower) != len(s) {
		return strings.ReplaceAll(s, old, repl)
	}
	var b strings.Builder
	for {
		i := strings.Index(lower, lold)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(repl)
		s, lower = s[i+len(old):], lower[i+len(old):]
	}
}

// chunkMessage splits text into chunks of at most limit bytes, preferring
// newline boundaries and never splitting a UTF-8 sequence.
func chunkMessage(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}
	var chunks []string
	for len(text) > limit {
		cut := strings.LastIndex(text[:limit], "\n")
		if cut <= 0 {
			cut = limit
			for cut > 0 && !utf8.RuneStart(text[cut]) {
				cut--
			}
		}
		chunks = append(chunks, strings.TrimRight(text[:cut], "\n"))
		text = strings.TrimLeft(text[cut:], "\n")
	}
	if text != "" {
		chunks = append(chunks, text)
	}
	return chunks
}
