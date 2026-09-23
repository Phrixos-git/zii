# Zii

Zii is a Go Discord bot that stores per-user conversation history in SQLite, asks an OpenAI-compatible local LLM for replies, and exposes the configured Search MCP tools through the model tool-calling loop.

## Run

Before starting Zii, have these services ready:

- A Discord bot application and token. Enable the `Guilds`, `Guild Messages`, and `Direct Messages` Gateway intents. Do not enable the privileged Message Content intent. Invite the bot with View Channels, Send Messages, Send Messages in Threads, and Read Message History permissions.
- An OpenAI-compatible LLM server with the selected model loaded. By default, Zii connects to `http://127.0.0.1:8080`.
- Search MCP serving its Streamable HTTP endpoint. By default, Zii connects to `http://127.0.0.1:8081/mcp`. Zii connects to Search MCP during startup and exits if it is unavailable.

Use Go 1.25 or newer. From the repository root, create a private runtime environment file from `.env.example`. Set `DISCORD_BOT_TOKEN`, `LLM_MODEL` to the model identifier accepted by your LLM server, and `BOT_DB_PATH` to an absolute path where Zii can create its SQLite database. Adjust the endpoint URLs in `.env` if your services use different addresses. The `.env` file is not loaded automatically; export its values and start Zii with:

```sh
cp .env.example .env
# Edit .env and set the required values.
set -a
. ./.env
set +a
go run ./cmd/zii
```

Zii loads its Search MCP tool schemas before opening the Discord Gateway. In servers, it handles messages that mention the bot; in DMs, it handles ordinary text messages. Press Ctrl+C to stop Zii; it stops accepting requests and shuts down gracefully.

## Verify

```sh
go test ./...
go vet ./...
```

Runtime endpoints are not contacted by the tests; LLM and MCP behavior is exercised with fake servers and clients.
