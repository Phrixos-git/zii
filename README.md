# Zii

Zii is a Go Discord bot that stores per-user conversation history in SQLite, asks an OpenAI-compatible LLM for replies, and uses tools exposed by Search MCP.

## Zii Ver1.2 identity and releases

Zii is the Discord AI assistant. The selected LLM is its underlying model. Ver1.2 adds authoritative self-description, user-facing capabilities, and release information while retaining conversation, search, model selection, and evaluation behavior.

Current capabilities include answering questions, writing and organizing information, using recent stored conversation history within configured limits and expiry, and explaining Zii's identity and releases. Search MCP is usable only when the selected profile enables Tools and the required tools are registered. Startup still requires Search MCP. Runtime availability describes configured support; network failures can still prevent searches. Recent conversation history is bounded and is not permanent User Memory.

The single source is [`internal/identity/manifest.yaml`](internal/identity/manifest.yaml). Ver1.0 and Ver1.2 are `released` application versions; Ver1.5 (DeepSeek Harness / Agent), Ver2.0 (User Memory / Personalized Context), Ver2.5 (memory optimization), and Ver3.0 (visual output / image generation) are `planned`. Planned dates are undecided. The manifest describes this binary's functionality, not whether a branch has been merged or deployed.

To update release information, edit that manifest, set `current_version` to a listed `released` version, and rebuild **both** `cmd/zii` and `cmd/zii-eval`. It is embedded in the binaries, requires no extra runtime file or environment variable, and does not reload while running. Unknown fields, malformed YAML, multiple documents, empty required fields, duplicate versions/capabilities/tools, unknown statuses, or an absent/planned current version cause startup to fail. Never put credentials, endpoints, database paths, or other private settings in the manifest.

Discord and Eval share `orchestrator.BuildSystemPrompt`. The existing search and security policies are combined with the manifest and public runtime facts: the configured API model identifier and effective search availability. The identifier does not verify exact model weights or developer. Eval records the assembled prompt hash and each sample's actual prompt hash, including cases that disable tools. Updating the manifest therefore changes evaluation reproducibility information. `ServiceConfig.SystemPrompt`, when supplied by an internal caller, adds trusted instructions to the shared prompt rather than replacing it.

Identity/release evaluation cases are in [`eval/testcases/identity.yaml`](eval/testcases/identity.yaml). See [`docs/ver1.2-implementation.md`](docs/ver1.2-implementation.md) for implementation decisions and validation results.

## Requirements

- Go 1.25 or newer to build or run from source.
- A Discord application and bot token. Enable the `Guilds`, `Guild Messages`, and `Direct Messages` Gateway intents. Zii does not request the privileged Message Content intent.
- Invite the bot with permission to view channels, send messages, send messages in threads, and read message history.
- An OpenAI-compatible LLM server with the selected model loaded and a Chat Completions endpoint. The endpoint is configured by the selected Model Profile. Tool calling is used only when that profile enables the `tools` capability.
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
LLM_MODEL=qwen
```

Create the database directory before starting Zii, for example:

```sh
mkdir -p /absolute/path/to/zii/data
```

Model IDs, LLM endpoints, capabilities, supported reasoning efforts, and generation defaults are configured in `config/model_profiles.yaml`. `LLM_MODEL` selects a key under `models:`; the profile's `model` value is sent to the API as the model name. Add or edit a profile there when changing the model or its endpoint. `.env` is excluded from Git and is not loaded automatically by Zii. The commands below export its values into the current shell before launch. Do not commit or share `.env`; it contains the Discord bot token. The legacy `LLM_BASE_URL` variable is ignored; the selected YAML profile endpoint is authoritative. If the legacy variable is set to a different endpoint, Zii logs a warning.

`config/gpt-oss-20b.profile.example.yaml` shows how to add another profile. It is an example and is not loaded automatically. Its endpoint is a placeholder and must be replaced with the actual server address before selecting it. Profile effort and capability values must be checked against the target model/runtime.

### Model Profiles and capabilities

The active `config/model_profiles.yaml` contains the profiles Zii can select. A profile is keyed by its ID under `models:` and contains the API model name, endpoint, six capabilities, supported reasoning efforts, and default generation settings. For example:

```yaml
models:
  qwen:
    model: qwen
    endpoint: http://127.0.0.1:8080
    capabilities:
      tools: true
      reasoning: true
      reasoning_content: true
      reasoning_effort: true
      thinking_budget: true
      parallel_tool_calls: true
    supported_reasoning_efforts:
      - low
      - medium
      - xhigh
    defaults:
      reasoning_effort: xhigh
      thinking_budget_tokens: 2048
```

The Qwen profile currently shipped in the active file enables all six capabilities. Its supported effort list contains `low`, `medium`, and `xhigh`, with `xhigh` as the current default. `medium` was previously verified against the configured Qwen endpoint. Add other effort values only after confirming that the model and serving runtime accept them.

| Capability | Effect when enabled |
| --- | --- |
| `tools` | Sends Zii's Search MCP tool definitions and allows the existing tool-call flow. When disabled, tool definitions are omitted and tool execution is not started. |
| `reasoning` | Allows reasoning-related request settings when their individual capabilities are also enabled. |
| `reasoning_content` | Reads and keeps the API's `reasoning_content` separate from the user-facing answer content. |
| `reasoning_effort` | Allows the configured reasoning effort to be sent. The value must be listed in `supported_reasoning_efforts`. |
| `thinking_budget` | Allows `thinking_budget_tokens` to be sent. This is independent of `reasoning_effort`. |
| `parallel_tool_calls` | Allows the request to accept multiple tool calls in one assistant response. Zii uses its existing tool-call handling; this setting does not add a parallel execution engine. |

Capability flags control whether a feature or request field may be used; they do not supply its value. A reasoning field whose capability is disabled is omitted from the API request. An effort value that is not listed in `supported_reasoning_efforts` is rejected. Capability flags and other profile fields are validated at startup: invalid YAML, unknown fields, incomplete profiles, and an unknown `LLM_MODEL` cause startup to fail rather than being silently corrected.

To add a model without changing or rebuilding Go code, copy the `gpt-oss-20b` entry from `config/gpt-oss-20b.profile.example.yaml` into the active file's `models:` mapping. Set its real endpoint and API model name, and confirm its capabilities, supported effort values, and defaults against that model/runtime. Then set `LLM_MODEL` to the profile key (for example, `gpt-oss-20b`) and restart Zii. To use another profile file, set `LLM_PROFILE_CONFIG`; relative paths are resolved from the process working directory.

### Environment settings

All settings can be left at the defaults shown in `.env.example`, except the three required values above.

| Variable | Default | Purpose |
| --- | --- | --- |
| `DISCORD_BOT_TOKEN` | required | Discord bot token. |
| `BOT_DB_PATH` | required | Absolute path to the SQLite database; the parent directory must be writable and exist. |
| `LLM_MODEL` | required | Key of the selected model under `models:` in the profile YAML file. |
| `LLM_PROFILE_CONFIG` | `config/model_profiles.yaml` | YAML file containing model profiles. Relative paths are resolved from the working directory. |
| `LLM_TIMEOUT` | `180s` | Per-request LLM HTTP timeout. |
| `LLM_MAX_TOKENS` | `4096` | Maximum tokens for a normal LLM request. |
| `LLM_REASONING_EFFORT` | empty | Optional reasoning effort value, sent only when enabled by the model profile. |
| `LLM_THINKING_BUDGET_TOKENS` | `0` | Optional positive token budget, sent only when enabled by the model profile. |
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
| `APP_TIMEZONE` | `Asia/Tokyo` | Timezone used for relative date and time answers. |
| `DISCORD_REPLY_MAX_CHARS` | `1900` | Maximum characters per Discord reply chunk; must not exceed 2000. |
| `DISCORD_SEND_MAX_RETRIES` | `3` | Maximum retry count for sending a reply. |
| `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, or `error`. Unknown values use `info`. |

`LLM_REASONING_EFFORT` and `LLM_THINKING_BUDGET_TOKENS` optionally override the selected profile's defaults. An effort value must appear in that profile's `supported_reasoning_efforts` list. A zero budget means “use the profile default”; set the YAML value to `null` for no default budget. Each value is sent only when both `reasoning` and its corresponding `reasoning_effort` or `thinking_budget` capability are enabled. A disabled capability causes the field to be omitted even when an environment override or profile default is present.

Duration values use Go duration syntax such as `30s`, `5m`, or `168h`. Numeric environment limits must be positive integers. `DISCORD_SEND_MAX_RETRIES` accepts values from `1` through `10`.

Relative date and time answers refer to the Discord message creation time in `APP_TIMEZONE`. The default is `Asia/Tokyo`; `UTC` and IANA names are allowed. `Local` and invalid names abort startup. Zii does not use web search for the date or time itself.

## Start in the foreground

Run from the repository root. The process writes structured JSON logs to standard output. Press Ctrl+C to request graceful shutdown; Zii stops accepting new requests, drains active work, and closes its connections and database.

Discord request and delivery failures retain their existing `event` and `error_code` fields and add an `error_detail` object. It contains a redacted `message`, `type`, and `cause_type`, plus `http_status`, `discord_code`, `cause_code` (for example, SQLite code 5), or `context_error` when available. Use `request_id` to correlate failures with processing and final-answer events; `processing_message_id` on `request_failed` is empty if no receipt was recorded. Discord still receives only the generic error text. Configured sensitive values and recognized sensitive patterns are redacted, Discord API response bodies are omitted, and diagnostic messages are limited to 2048 characters plus a truncation marker.

With `LOG_LEVEL=DEBUG`, each Discord message HTTP attempt emits `event=discord_http_attempt`. `send_count` starts at 1 for each reply or edit and includes both DiscordGo internal retries and adapter retries. Correlate attempts with `request_id` and `delivery_id`; each chunk or edit has its own `delivery_id`. Logs include `operation`, `method`, `http_status`, `transport_error`, and `duration_ms`. `http_status=0` means no HTTP response was received. Headers, URLs, bodies, tokens, and error text are not logged. These records describe HTTP attempts, not proof that Discord created a message.

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

### Repeatable LLM evaluation

`go build -o bin/zii-eval ./cmd/zii-eval` builds the evaluation CLI. It runs
YAML question sets through the production ToolLoop without Discord or the
conversation database, records anomalies and timings, and writes JSON, CSV,
and Markdown reports. Use fixed tool responses for repeatable comparisons or
live Search MCP for integration checks. See [eval/README.md](eval/README.md)
for profiles, warmup/repetitions, metrics, and the `compare` command.

### Application checks

Run the automated checks from the repository root:

```sh
go test ./...
go vet ./...
```

The tests use fake LLM, MCP, and Discord clients; they do not contact your runtime services. For live startup, confirm that the LLM server has loaded the model named by `LLM_MODEL` and that Search MCP is reachable at `SEARCH_MCP_URL`. If Zii exits before connecting to Discord, check its first startup error and confirm the required environment variables, database directory permissions, and Search MCP tool list.

In Discord, Zii responds when mentioned in a server channel and to ordinary text messages in a DM. It ignores bot, webhook, and system messages. The bot must be able to view and send messages in the target channel; replies are sent as replies to the triggering message.

When a queued request starts, Zii atomically stores the question's Discord message ID and user message in SQLite before sending the processing receipt. Only the request that commits this registration sends a receipt and generates an answer. Duplicate questions and failed registrations do not send a processing receipt or call the LLM. Queued requests wait for their turn before receiving a receipt, keeping conversation history and answer delivery ordered. A committed question stays registered even if receipt delivery or later processing fails; retry by posting a new question. Receipt and error messages are not stored as conversation messages.
