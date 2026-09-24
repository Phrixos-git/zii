# Zii

Zii is a Go Discord bot that stores per-user conversation history in SQLite, asks an OpenAI-compatible LLM for replies, and uses tools exposed by Search MCP.

## Requirements

- Go 1.25 or newer to build or run from source.
- A Discord application and bot token. Enable the `Guilds`, `Guild Messages`, and `Direct Messages` Gateway intents. Zii does not request the privileged Message Content intent.
- Invite the bot with permission to view channels, send messages, send messages in threads, and read message history.
- An OpenAI-compatible LLM server with the selected model loaded. The configured endpoint must support Chat Completions and tool calling. The default base URL is `http://127.0.0.1:8080`.
- Search MCP serving Streamable HTTP and providing the tools Zii uses (`search_local`, `search_web`, and `fetch_page`). The default endpoint is `http://127.0.0.1:8081/mcp`.
- A writable directory for the SQLite database. The database path must be absolute, and its parent directory must already exist.

Zii connects to Search MCP and loads its tool definitions during startup. It exits if Search MCP is unavailable or the required tools cannot be loaded. The LLM client configuration is checked at startup; the LLM endpoint is contacted when a request is handled, so make sure the endpoint and model are ready before testing a Discord message.

## Configure

From the repository root, create a private environment file and edit the required values:

```sh
cp .env.example .env
chmod 600 .env
```

Set these required values in `.env`:

```dotenv
DISCORD_BOT_TOKEN=your-discord-bot-token
BOT_DB_PATH=/absolute/path/to/zii/data/bot.db
LLM_MODEL=model-id-reported-by-your-llm-server
```

Create the database directory before starting Zii, for example:

```sh
mkdir -p /absolute/path/to/zii/data
```

Change `LLM_BASE_URL` and `SEARCH_MCP_URL` in `.env` if the services use different addresses. `.env` is excluded from Git and is not loaded automatically by Zii. The commands below export its values into the current shell before launch. Do not commit or share `.env`; it contains the Discord bot token.

### Environment settings

All settings can be left at the defaults shown in `.env.example`, except the three required values above.

| Variable | Default | Purpose |
| --- | --- | --- |
| `DISCORD_BOT_TOKEN` | required | Discord bot token. |
| `BOT_DB_PATH` | required | Absolute path to the SQLite database; the parent directory must be writable and exist. |
| `LLM_MODEL` | required | Model identifier accepted by the LLM server. |
| `LLM_BASE_URL` | `http://127.0.0.1:8080` | OpenAI-compatible API base URL. |
| `LLM_TIMEOUT` | `180s` | Per-request LLM HTTP timeout. |
| `LLM_MAX_TOKENS` | `4096` | Maximum tokens for a normal LLM request. |
| `LLM_MAX_CONCURRENCY` | `2` | Maximum concurrent LLM requests. |
| `SEARCH_MCP_URL` | `http://127.0.0.1:8081/mcp` | Search MCP Streamable HTTP endpoint. |
| `SEARCH_MCP_TIMEOUT` | `30s` | Search MCP request timeout. |
| `MCP_MAX_CONCURRENCY` | `4` | Maximum concurrent Search MCP calls. |
| `CONVERSATION_TTL` | `168h` | Conversation retention duration. |
| `CONVERSATION_MAX_TURNS` | `5` | Maximum prior conversation turns included in context. |
| `CONVERSATION_MAX_TOKENS` | `8192` | Token budget for conversation history. |
| `TOOL_MAX_CALLS` | `3` | Maximum tool calls for one user request. |
| `SEARCH_LOCAL_MAX_CALLS` | `2` | Maximum `search_local` calls per request. |
| `SEARCH_WEB_MAX_CALLS` | `1` | Maximum `search_web` calls per request. |
| `FETCH_PAGE_MAX_CALLS` | `2` | Maximum `fetch_page` calls per request. |
| `REQUEST_QUEUE_SIZE` | `10` | Maximum number of queued Discord requests. |
| `QUEUE_WAIT_TIMEOUT` | `180s` | Maximum time a request waits in the queue. |
| `REQUEST_TIMEOUT` | `300s` | Maximum processing time for one request. |
| `DISCORD_REPLY_MAX_CHARS` | `1900` | Maximum characters per Discord reply chunk; must not exceed 2000. |
| `DISCORD_SEND_MAX_RETRIES` | `3` | Maximum retry count for sending a reply. |
| `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, or `error`. Unknown values use `info`. |

Duration values use Go duration syntax such as `30s`, `5m`, or `168h`. Numeric environment limits must be positive integers. `DISCORD_SEND_MAX_RETRIES` accepts values from `1` through `10`.

## Start in the foreground

Run from the repository root. The process writes structured JSON logs to standard output. Press Ctrl+C to request graceful shutdown; Zii stops accepting new requests, drains active work, and closes its connections and database.

For development, run directly from source:

```sh
set -a
. ./.env
set +a
go run ./cmd/zii
```

To build and run a binary instead:

```sh
go build -o ./zii ./cmd/zii
set -a
. ./.env
set +a
./zii
```

## Start in the background (Linux)

Build the binary, then launch it with `nohup`. The example keeps the log and PID file under the repository root; `logs/` and `tmp/` are ignored by Git.

```sh
go build -o ./zii ./cmd/zii
mkdir -p logs tmp
set -a
. ./.env
set +a
nohup ./zii >> logs/zii.log 2>&1 < /dev/null &
echo $! > tmp/zii.pid
```

Check the process and follow its logs:

```sh
ps -p "$(cat tmp/zii.pid)" -o pid=,stat=,cmd=
tail -f logs/zii.log
```

Stop it gracefully with `SIGTERM`:

```sh
kill -TERM "$(cat tmp/zii.pid)"
```

After the process has exited, remove the PID file:

```sh
rm -f tmp/zii.pid
```

Zii handles `SIGTERM` and allows up to 60 seconds for in-flight requests and the request queue to shut down. For long-running production deployments, run the binary under a service manager such as systemd so it can be restarted and monitored automatically.

## Verify and troubleshoot

Run the automated checks from the repository root:

```sh
go test ./...
go vet ./...
```

The tests use fake LLM, MCP, and Discord clients; they do not contact your runtime services. For live startup, confirm that the LLM server has loaded the model named by `LLM_MODEL` and that Search MCP is reachable at `SEARCH_MCP_URL`. If Zii exits before connecting to Discord, check its first startup error and confirm the required environment variables, database directory permissions, and Search MCP tool list.

In Discord, Zii responds when mentioned in a server channel and to ordinary text messages in a DM. It ignores bot, webhook, and system messages. The bot must be able to view and send messages in the target channel; replies are sent as replies to the triggering message.
