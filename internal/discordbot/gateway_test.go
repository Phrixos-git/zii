package discordbot

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestNewGatewayUsesOnlyRequiredNonPrivilegedIntents(t *testing.T) {
	client, err := NewGateway("test-token")
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	gateway, ok := client.(*Gateway)
	if !ok {
		t.Fatalf("NewGateway returned %T, want concrete discordgo Gateway behind its interface", client)
	}
	want := discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages
	if got := gateway.session.Identify.Intents; got != want {
		t.Fatalf("Gateway intents = %b, want exactly %b", got, want)
	}
	if gateway.session.Identify.Intents&discordgo.IntentMessageContent != 0 {
		t.Fatal("privileged MESSAGE_CONTENT intent was enabled")
	}
	if gateway.session.Identify.Intents&(discordgo.IntentsGuildMembers|discordgo.IntentsGuildPresences) != 0 {
		t.Fatal("unneeded privileged guild intent was enabled")
	}
}
