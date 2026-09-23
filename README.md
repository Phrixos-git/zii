# Zii

Zii is a Go Discord bot that stores per-user conversation history in SQLite, asks an OpenAI-compatible local LLM for replies, and exposes the configured Search MCP tools through the model tool-calling loop.

## Run

Use Go 1.25 or newer. Create a private runtime environment file from `.env.example`, set `DISCORD_BOT_TOKEN`, `LLM_MODEL`, and an absolute `BOT_DB_PATH`, then export its values before starting Zii:

```sh
cp .env.example .env
# Edit .env and set the required values.
set -a
. ./.env
set +a
go run ./cmd/zii
```

The LLM and Search MCP URLs are runtime settings. The defaults are `http://127.0.0.1:8080` and `http://127.0.0.1:8081/mcp`; Zii connects to them only when the application runs. Zii requires Search MCP to be available at startup and loads its allowlisted tool schemas before opening the Discord Gateway.

In Discord, enable the `Guilds`, `Guild Messages`, and `Direct Messages` Gateway intents. Do not enable the privileged Message Content intent. Invite Zii with View Channels, Send Messages, Send Messages in Threads, and Read Message History permissions. In servers, Zii handles messages that mention the bot; in DMs, it handles ordinary text messages.

## Verify

```sh
go test ./...
go vet ./...
```

Runtime endpoints are not contacted by the tests; LLM and MCP behavior is exercised with fake servers and clients.
