package discord

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

const testBot = "999"

func msg(author, guild, channel, content string, mentionBot bool) *discordgo.Message {
	m := &discordgo.Message{
		Author:    &discordgo.User{ID: author, Username: "user" + author},
		GuildID:   guild,
		ChannelID: channel,
		Content:   content,
	}
	if mentionBot {
		m.Mentions = []*discordgo.User{{ID: testBot}}
	}
	return m
}

func TestAcceptGating(t *testing.T) {
	c := New(Config{OwnerIDs: []string{"1"}, GuildTag: "@kroos", OpenChannels: []string{"open"}})

	cases := []struct {
		label    string
		m        *discordgo.Message
		wantText string
		wantDrop bool
	}{
		{"owner DM", msg("1", "", "dm", "hi", false), "hi", false},
		{"stranger DM", msg("2", "", "dm", "hi", false), "", true},
		{"owner guild no mention", msg("1", "g", "c", "hi", false), "", true},
		{"owner guild mention", msg("1", "g", "c", "<@999> hi", true), "hi", false},
		{"owner guild tag", msg("1", "g", "c", "@kroos hi", false), "hi", false},
		{"stranger closed channel", msg("2", "g", "c", "<@999> hi", true), "", true},
		{"stranger open no mention", msg("2", "g", "open", "hi", false), "", true},
		{"stranger open mention", msg("2", "g", "open", "<@999> hi", true), "[user2]: hi", false},
		{"owner open mention", msg("1", "g", "open", "<@999> hi", true), "[user1 (owner)]: hi", false},
	}
	for _, tc := range cases {
		text, reason := c.accept(tc.m, testBot)
		if (reason != "") != tc.wantDrop {
			t.Errorf("%s: drop reason %q, wantDrop %v", tc.label, reason, tc.wantDrop)
		}
		if text != tc.wantText {
			t.Errorf("%s: text %q, want %q", tc.label, text, tc.wantText)
		}
	}
}

func TestAcceptMediaOnly(t *testing.T) {
	c := New(Config{OwnerIDs: []string{"1"}})
	m := msg("1", "g", "c", "<@999>", true)
	m.Attachments = []*discordgo.MessageAttachment{{URL: "u", Filename: "a.png"}}
	text, reason := c.accept(m, testBot)
	if reason != "" || text != "" {
		t.Errorf("text %q reason %q", text, reason)
	}
	dm := msg("1", "", "dm", "", false)
	dm.Attachments = []*discordgo.MessageAttachment{{URL: "u"}}
	if _, reason := c.accept(dm, testBot); reason != "" {
		t.Errorf("media DM dropped: %q", reason)
	}
}
