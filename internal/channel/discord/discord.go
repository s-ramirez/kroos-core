// Package discord implements a channel.Channel backed by a Discord bot.
// Incoming messages arrive via the Discord gateway (websocket); outgoing
// messages are sent via the Discord REST API.
//
// Only messages from the configured OwnerIDs are forwarded to the
// dispatcher, except in OpenChannels where anyone may address the bot.
// In guild channels an additional GuildTag (or a mention of the bot)
// must appear in the message; that marker is stripped before dispatch.
package discord

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/s-ramirez/kroos-core/internal/channel"
)

const channelName = "discord"

// Config holds all runtime parameters for the Discord channel.
type Config struct {
	// Token is the Discord bot token (without the "Bot " prefix).
	Token string

	// OwnerIDs is the set of Discord user IDs (snowflakes) that are allowed
	// to trigger agent responses. Messages from any other user are dropped.
	OwnerIDs []string

	// GuildTag is a literal string that must appear in a guild-channel
	// message for the agent to respond (e.g. "@kroos"). A mention of the bot
	// also satisfies this requirement. DMs bypass the check entirely.
	GuildTag string

	// OpenChannels lists guild channel IDs where any user (not just
	// owners) may address the bot. The mention/tag requirement still
	// applies. Populated from profiles with senders: any.
	OpenChannels []string

	// AllowAll lets any user address the bot, DMs included. Mention/tag
	// gating in guild channels still applies.
	AllowAll bool

	// HomeChannel is the default channel ID for deliveries that name no
	// recipient (e.g. heartbeat).
	HomeChannel string
}

// Channel implements channel.Channel for Discord.
type Channel struct {
	cfg     Config
	session *discordgo.Session
	botID   string
	http    *http.Client

	// guildChannels records channel IDs seen as guild channels; replies
	// there are threaded under the triggering message (DMs are not).
	guildChannels sync.Map
}

// New returns a new Channel with the given configuration.
func New(cfg Config) *Channel {
	return &Channel{cfg: cfg, http: &http.Client{Timeout: time.Minute}}
}

// ConfigFromEnv reads DISCORD_* environment variables and returns a
// populated Config along with a reason string when the channel should be
// disabled. Returns (nil, "", nil) if disabled without explanation (e.g.
// no token). Errors only when a value is present but malformed.
//
// The second return (disabledReason) is non-empty when the channel is
// explicitly disabled via DISCORD_ENABLED=false so the caller can log it.
func ConfigFromEnv(getenv func(string) string, parseBool func(name string, def bool) bool) (*Config, string, error) {
	if !parseBool("DISCORD_ENABLED", true) {
		return nil, "DISCORD_ENABLED=false", nil
	}
	token := strings.TrimSpace(getenv("DISCORD_BOT_TOKEN"))
	if token == "" {
		return nil, "", nil
	}
	var owners []string
	if raw := getenv("DISCORD_ALLOWED_USERS"); raw != "" {
		for _, id := range strings.Split(raw, ",") {
			if id = strings.TrimSpace(id); id != "" {
				owners = append(owners, id)
			}
		}
	}
	return &Config{
		Token:       token,
		OwnerIDs:    owners,
		GuildTag:    getenv("DISCORD_GUILD_TAG"),
		AllowAll:    parseBool("DISCORD_ALLOW_ALL_USERS", false),
		HomeChannel: strings.TrimSpace(getenv("DISCORD_HOME_CHANNEL")),
	}, "", nil
}

func (c *Channel) Name() string { return channelName }

// Home returns the default delivery channel ID.
func (c *Channel) Home() string { return c.cfg.HomeChannel }

// Listen opens the Discord gateway connection and forwards qualifying
// messages onto out. It blocks until ctx is cancelled.
func (c *Channel) Listen(ctx context.Context, out chan<- channel.Inbound) error {
	s, err := discordgo.New("Bot " + c.cfg.Token)
	if err != nil {
		return fmt.Errorf("discord: new session: %w", err)
	}
	s.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsDirectMessages |
		discordgo.IntentsMessageContent

	s.AddHandler(c.makeHandler(ctx, out))

	if err := s.Open(); err != nil {
		return fmt.Errorf("discord: open gateway: %w", err)
	}
	c.session = s
	if s.State != nil && s.State.User != nil {
		c.botID = s.State.User.ID
	}
	fmt.Fprintf(os.Stderr, "[discord] gateway connected as %s\n", c.botID)

	<-ctx.Done()
	_ = s.Close()
	return ctx.Err()
}

// Send delivers a reply via the Discord REST API. Attachments are
// uploaded first. Runner meta-lines like "[tokens: ...]" are filtered.
// In guild channels the first chunk replies to msg.ReplyTo.
func (c *Channel) Send(ctx context.Context, msg channel.Outbound) error {
	if c.session == nil {
		return fmt.Errorf("discord: session not initialized")
	}
	if len(msg.Attachments) > 0 {
		if err := c.sendFiles(msg.Thread, msg.Attachments); err != nil {
			fmt.Fprintf(os.Stderr, "[discord] send attachment error: %v\n", err)
			return err
		}
	}
	text := channel.DropMetaLines(msg.Content)
	if text == "" {
		return nil
	}
	var ref *discordgo.MessageReference
	if _, guild := c.guildChannels.Load(msg.Thread); guild && msg.ReplyTo != "" {
		fail := false
		ref = &discordgo.MessageReference{MessageID: msg.ReplyTo, ChannelID: msg.Thread, FailIfNotExists: &fail}
	}
	for _, chunk := range chunkMessage(text, 2000) {
		if _, err := c.session.ChannelMessageSendComplex(msg.Thread, &discordgo.MessageSend{
			Content:   chunk,
			Reference: ref,
		}, discordgo.WithContext(ctx)); err != nil {
			fmt.Fprintf(os.Stderr, "[discord] send error: %v\n", err)
			return err
		}
		ref = nil
	}
	return nil
}

// SendEditable posts text and returns its message ID (channel.Editor).
func (c *Channel) SendEditable(ctx context.Context, thread, text, replyTo string) (string, error) {
	if c.session == nil {
		return "", fmt.Errorf("discord: session not initialized")
	}
	var ref *discordgo.MessageReference
	if _, guild := c.guildChannels.Load(thread); guild && replyTo != "" {
		fail := false
		ref = &discordgo.MessageReference{MessageID: replyTo, ChannelID: thread, FailIfNotExists: &fail}
	}
	m, err := c.session.ChannelMessageSendComplex(thread, &discordgo.MessageSend{Content: text, Reference: ref}, discordgo.WithContext(ctx))
	if err != nil {
		return "", err
	}
	return m.ID, nil
}

// Edit replaces a message's text (channel.Editor).
func (c *Channel) Edit(ctx context.Context, thread, id, text string) error {
	if c.session == nil {
		return fmt.Errorf("discord: session not initialized")
	}
	_, err := c.session.ChannelMessageEdit(thread, id, text, discordgo.WithContext(ctx))
	return err
}

// sendFiles uploads local files in one message (Discord allows 10).
func (c *Channel) sendFiles(channelID string, atts []channel.Attachment) error {
	for len(atts) > 0 {
		batch := atts[:min(len(atts), 10)]
		atts = atts[len(batch):]
		var files []*discordgo.File
		for _, a := range batch {
			f, err := os.Open(a.Path)
			if err != nil {
				return err
			}
			defer f.Close()
			files = append(files, &discordgo.File{Name: a.Name, ContentType: a.MimeType, Reader: f})
		}
		if _, err := c.session.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{Files: files}); err != nil {
			return err
		}
	}
	return nil
}

// download saves the message's attachments locally, skipping failures
// and anything over channel.MaxAttachmentBytes.
func (c *Channel) download(ctx context.Context, atts []*discordgo.MessageAttachment) []channel.Attachment {
	var saved []channel.Attachment
	for _, a := range atts {
		if a == nil || a.URL == "" || a.Size > channel.MaxAttachmentBytes {
			continue
		}
		dctx, cancel := context.WithTimeout(ctx, time.Minute)
		att, err := channel.FetchAttachment(dctx, c.http, a.URL, channel.AttachmentDir(channelName), a.Filename, a.ContentType)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[discord] attachment %s: %v\n", a.ID, err)
			continue
		}
		saved = append(saved, att)
	}
	return saved
}

// makeHandler returns a discordgo MessageCreate handler that filters and
// forwards qualifying messages to the dispatcher.
func (c *Channel) makeHandler(ctx context.Context, out chan<- channel.Inbound) func(*discordgo.Session, *discordgo.MessageCreate) {
	log := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "[discord] "+format+"\n", args...)
	}

	return func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author == nil || m.Author.Bot {
			return
		}
		if s.State != nil && s.State.User != nil && m.Author.ID == s.State.User.ID {
			return
		}

		botID := c.botID
		if botID == "" && s.State != nil && s.State.User != nil {
			botID = s.State.User.ID
		}
		text, reason := c.accept(m.Message, botID)
		if reason != "" {
			log("dropped message from %s (channel=%s): %s", m.Author.ID, m.ChannelID, reason)
			return
		}
		if text == "" && len(m.Attachments) == 0 {
			return
		}
		isDM := m.GuildID == ""
		if !isDM {
			c.guildChannels.Store(m.ChannelID, struct{}{})
		}

		log("accepted message from %s (channel=%s, dm=%v, chars=%d, attachments=%d)", m.Author.ID, m.ChannelID, isDM, len(text), len(m.Attachments))

		atts := c.download(ctx, m.Attachments)
		if text == "" && len(atts) == 0 {
			return
		}
		msg := channel.Inbound{
			Channel:     channelName,
			Thread:      m.ChannelID,
			Sender:      m.Author.ID,
			MessageID:   m.ID,
			Attachments: atts,
			Content:     text,
			Kind:        channel.KindUser,
			ReceivedAt:  m.Timestamp,
		}
		select {
		case <-ctx.Done():
		case out <- msg:
		}
	}
}

// accept applies sender and mention gating to m. It returns the cleaned
// text to dispatch, or a non-empty drop reason. Owners may talk in any
// guild channel or DM; non-owners only in OpenChannels, never DMs. Guild
// messages always need an @mention of the bot or the GuildTag.
func (c *Channel) accept(m *discordgo.Message, botID string) (string, string) {
	text := strings.TrimSpace(m.Content)
	hasMedia := len(m.Attachments) > 0
	if text == "" && !hasMedia {
		return "", ""
	}
	isDM := m.GuildID == ""
	owner := c.isOwner(m.Author.ID)
	open := !isDM && c.isOpen(m.ChannelID)
	if !owner && !open && !c.cfg.AllowAll {
		return "", "non-owner"
	}
	if isDM {
		return text, ""
	}

	mentioned := false
	for _, u := range m.Mentions {
		if u != nil && botID != "" && u.ID == botID {
			mentioned = true
			break
		}
	}
	tagPresent := c.cfg.GuildTag != "" && strings.Contains(text, c.cfg.GuildTag)
	if !mentioned && !tagPresent {
		return "", "missing mention/tag"
	}
	if mentioned {
		text = strings.ReplaceAll(text, "<@"+botID+">", "")
		text = strings.ReplaceAll(text, "<@!"+botID+">", "")
	}
	if tagPresent {
		text = strings.ReplaceAll(text, c.cfg.GuildTag, "")
	}
	text = strings.TrimSpace(text)
	if text == "" && !hasMedia {
		return "", ""
	}
	// Multi-user channels: tell the model who is speaking.
	if open && text != "" {
		role := ""
		if owner {
			role = " (owner)"
		}
		text = fmt.Sprintf("[%s%s]: %s", m.Author.Username, role, text)
	}
	return text, ""
}

func (c *Channel) isOpen(channelID string) bool {
	for _, id := range c.cfg.OpenChannels {
		if id == channelID {
			return true
		}
	}
	return false
}

func (c *Channel) isOwner(id string) bool {
	for _, o := range c.cfg.OwnerIDs {
		if o == id {
			return true
		}
	}
	return false
}

// chunkMessage splits text into chunks no larger than limit bytes, trying
// to break on newlines. Discord's per-message limit is 2000 characters.
func chunkMessage(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}
	var chunks []string
	for len(text) > limit {
		cut := strings.LastIndex(text[:limit], "\n")
		if cut <= 0 {
			cut = limit
		}
		chunks = append(chunks, strings.TrimRight(text[:cut], "\n"))
		text = strings.TrimLeft(text[cut:], "\n")
	}
	if text != "" {
		chunks = append(chunks, text)
	}
	return chunks
}
