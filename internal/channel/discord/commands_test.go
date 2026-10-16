package discord

import (
	"regexp"
	"testing"

	"github.com/bwmarrin/discordgo"
)

var discordNameRE = regexp.MustCompile(`^[-_a-z0-9]{1,32}$`)

func TestApplicationCommandsAreValid(t *testing.T) {
	cmds := applicationCommands()
	if len(cmds) == 0 {
		t.Fatal("no commands")
	}
	for _, c := range cmds {
		if !discordNameRE.MatchString(c.Name) || c.Description == "" || len(c.Description) > 100 {
			t.Errorf("invalid command %+v", c)
		}
		for _, o := range c.Options {
			if !discordNameRE.MatchString(o.Name) || o.Description == "" || len(o.Description) > 100 || o.Required {
				t.Errorf("invalid option on %s: %+v", c.Name, o)
			}
		}
	}
}

func interaction(userID, guildID, name, arg string) *discordgo.Interaction {
	data := discordgo.ApplicationCommandInteractionData{Name: name}
	if arg != "" {
		data.Options = []*discordgo.ApplicationCommandInteractionDataOption{{Name: "choice", Type: discordgo.ApplicationCommandOptionString, Value: arg}}
	}
	i := &discordgo.Interaction{Type: discordgo.InteractionApplicationCommand, GuildID: guildID, Data: data}
	if guildID != "" {
		i.Member = &discordgo.Member{User: &discordgo.User{ID: userID}}
	} else {
		i.User = &discordgo.User{ID: userID}
	}
	return i
}

func TestAcceptInteraction(t *testing.T) {
	c := New(Config{OwnerIDs: []string{"owner"}})
	if text, uid, ok := c.acceptInteraction(interaction("owner", "", "model", "3")); text != "/model 3" || uid != "owner" || !ok {
		t.Fatalf("owner DM: %q %q %v", text, uid, ok)
	}
	if text, _, ok := c.acceptInteraction(interaction("owner", "g1", "doctor", "")); text != "/doctor" || !ok {
		t.Fatalf("owner guild: %q %v", text, ok)
	}
	if _, _, ok := c.acceptInteraction(interaction("stranger", "g1", "doctor", "")); ok {
		t.Fatal("non-owner allowed")
	}
	c.cfg.AllowAll = true
	if _, _, ok := c.acceptInteraction(interaction("stranger", "g1", "new", "")); !ok {
		t.Fatal("allow_all should permit commands")
	}
}
