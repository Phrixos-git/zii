package main

import (
	"testing"
)

func TestBlockedOutputValuesIncludesSecretsAndRuntimeLocations(t *testing.T) {
	t.Setenv("DISCORD_BOT_TOKEN", "test-discord-token")
	t.Setenv("MEILI_MASTER_KEY", "test-meili-master-key")
	t.Setenv("THIRD_PARTY_API_KEY", "test-third-party-api-key")
	t.Setenv("LLM_BASE_URL", "https://llm.private.example/v1")
	t.Setenv("SEARCH_MCP_URL", "http://search-mcp.internal/mcp")
	values := blockedOutputValues("/srv/zii/data/bot.db")
	for _, want := range []string{
		"/srv/zii/data/bot.db",
		"https://llm.private.example/v1",
		"http://search-mcp.internal/mcp",
		"test-discord-token",
		"test-meili-master-key",
		"test-third-party-api-key",
	} {
		found := false
		for _, value := range values {
			if value == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("blocked output values do not include %q", want)
		}
	}
}
