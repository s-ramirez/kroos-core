# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build and Test

```sh
go build ./...              # Build all packages
go test ./...               # Run full test suite
go test ./internal/tools/   # Run tests for a single package
go test ./internal/tools/ -run TestGrepToolFindsPattern  # Run a single test
go test -v ./...            # Verbose output
```

Run the CLI: `go run ./cmd/kroos` (defaults to the OpenAI API).

Bootstrap a project workspace: `go run ./cmd/kroos init` scaffolds `.kroos/` with default `AGENTS.md`, `IDENTITY.md`, `USER.md`, and memory/skills directories from the embedded templates under `cmd/kroos/templates/`.

**Config CLI** (`internal/config/`, `cmd/kroos/config.go`): every setting below has a dotted key (`kroos config keys`). Set via `kroos config set <key> <val> [--project]` (writes `~/.kroos/config.json` or `.kroos/config.json`), or per-run flags (`kroos --model x --heartbeat.interval 15m`, `--set key=val`). Precedence: flag > env > `.env` > project config > global config (> agent config for model/base_url/api_key, except flags). Config values are exported as the env vars below; new settings must be added to `config.Keys`.

Environment variables: `KROOS_BASE_URL` (API endpoint, default `https://api.openai.com`), `KROOS_MODEL` (model name, default `"gpt-5.4"`), `KROOS_LLM_TIMEOUT` (default `5m`), `KROOS_SUBAGENT_LLM_TIMEOUT` (default: same as `KROOS_LLM_TIMEOUT`), `KROOS_ATTACHMENT_DIR` (where channels save inbound media; default `$TMPDIR/kroos-attachments`, one subdir per channel), `KROOS_IMAGE_INPUT` (default `true`; inline image attachments as vision input — set `false` for non-vision models).

Heartbeat environment variables (all optional; heartbeat is disabled unless `KROOS_HEARTBEAT_INTERVAL` is set):
`KROOS_HEARTBEAT_INTERVAL` (Go duration, e.g. `15m`), `KROOS_HEARTBEAT_TARGET` (default `cli`; `cli` | `imessage` | `discord` | `telegram` | `none`), `KROOS_HEARTBEAT_TO` (recipient handle/channel ID for non-CLI targets), `KROOS_HEARTBEAT_MODEL` (model override for heartbeat turns; empty = same as default), `KROOS_HEARTBEAT_PROMPT` (custom prompt text), `KROOS_HEARTBEAT_ACTIVE_HOURS_START` / `KROOS_HEARTBEAT_ACTIVE_HOURS_END` (`HH:MM` 24h window; both must be set), `KROOS_HEARTBEAT_TIMEZONE` (IANA name; empty = local). When the model has nothing to report it should respond with `HEARTBEAT_OK` — this suppresses delivery and prunes the turn from history.

iMessage channel environment variables (all optional; channel is disabled unless both URL and password are set):
`KROOS_IMESSAGE_ENABLED` (default `true`; set to `false`/`0` to force-disable even when creds are present), `KROOS_IMESSAGE_SERVER_URL`, `KROOS_IMESSAGE_PASSWORD`, `KROOS_IMESSAGE_WEBHOOK_HOST` (default `127.0.0.1`; use `0.0.0.0` if BlueBubbles runs on another machine), `KROOS_IMESSAGE_WEBHOOK_PORT` (default `8421`), `KROOS_IMESSAGE_WEBHOOK_PATH` (default `/bluebubbles`), `KROOS_IMESSAGE_WEBHOOK_URL` (URL BlueBubbles should call; default derived from host/port/path), `KROOS_IMESSAGE_REGISTER_WEBHOOK` (default `true`), `KROOS_IMESSAGE_OWNER_HANDLES` (comma-separated), `KROOS_IMESSAGE_GROUP_TAG` (e.g. `@kroos`), `KROOS_IMESSAGE_READ_RECEIPTS` (default `true`; read receipts + typing indicator, needs BlueBubbles private API).

Discord channel environment variables (channel is disabled unless `KROOS_DISCORD_TOKEN` is set):
`KROOS_DISCORD_ENABLED` (default `true`; set to `false`/`0` to force-disable even when a token is present), `KROOS_DISCORD_TOKEN` (bot token), `KROOS_DISCORD_OWNER_IDS` (comma-separated Discord user snowflakes), `KROOS_DISCORD_GUILD_TAG` (e.g. `@kroos` — required in guild channels unless the bot is @mentioned; DMs bypass the check).

Telegram channel environment variables (channel is disabled unless `KROOS_TELEGRAM_TOKEN` is set):
`KROOS_TELEGRAM_ENABLED` (default `true`), `KROOS_TELEGRAM_TOKEN` (BotFather token), `KROOS_TELEGRAM_OWNER_IDS` (comma-separated numeric user IDs), `KROOS_TELEGRAM_GROUP_TAG` (optional extra trigger; @mention of the bot or a reply to it always works in groups).

## Architecture

**Agent loop** (`internal/cli/runner.go`): The core cycle is `Runner.agentLoop()`. It calls the LLM, dispatches any tool calls through the `Registry`, appends results as `"tool"` role messages, and loops until the model responds with text. If the model returns empty content after tool calls, the runner retries up to 2 times with a follow-up nudge message.

**Tool interface** (`internal/tools/types.go`): Every tool implements `Tool` — `Definition()` returns metadata and JSON schema, `Execute(ctx, json.RawMessage)` returns `(ToolResult, error)`. Tool-level errors (bad args, file not found) are returned as `ToolResult{IsError: true}` via the `errorResult`/`errorResultf` helpers, not as Go errors.

**Tool hooks** (`internal/tools/types.go`): `ToolHook` interface with `BeforeToolCall` (can block execution by returning a reason string) and `AfterToolCall`. Hooks are registered on the `Registry` and invoked by the runner around each tool execution.

**Filesystem abstraction** (`internal/tools/fs.go`): Tools that touch files (read, write, edit, ls) take an `FS` interface. Production uses `OSFS{}`. Tools that walk directories (grep, find, bash) use `os` directly.

**Skills** (`internal/skills/`): Skills are markdown files (`SKILL.md`) with YAML frontmatter that provide specialized instructions to the LLM. `LoadSkills(dirs...)` scans directories for `SKILL.md` files, parses frontmatter (name, description, disable-model-invocation), and returns validated `Skill` structs. Skills are discovered from `~/.kroos/skills/` (user-level) and `.kroos/skills/` (project-level). First skill with a given name wins on collision. Users invoke skills with `/skill:<name> [args]`, which the runner expands to the skill file content before sending to the LLM. Skills with `disable-model-invocation: true` are excluded from the system prompt but remain invocable via `/skill:`.

**System prompt** (`internal/prompt/system.go`): `BuildSystemPrompt(tools, skills, env)` assembles the prompt from sections: environment info (OS, arch, workspace, time), per-tool guidelines, conditional tool-selection rules, and an `<available_skills>` XML block listing visible skills with their name, description, and file location.

**LLM client** (`internal/llm/client.go`): `ChatClient` interface with a single `Chat()` method. The concrete `Client` posts to `/v1/chat/completions` (OpenAI-compatible format). The runner accepts a `ClientForModel func(model string) ChatClient` factory for per-turn model overrides (used by heartbeat).

**Heartbeat system** (`internal/heartbeat/`, `internal/channel/sched/`): `HeartbeatDelivery` on `sched.Channel` adds active-hours gating, `HEARTBEAT_OK` token suppression (prunes the turn from history), outbound routing to iMessage/Discord/Telegram/none, 24 h duplicate suppression, and per-turn model override. `channel.Inbound` carries `InterceptReply` (reply routing hook) and `ModelOverride` (per-turn model); `channel.Conversation` carries `ModelOverride` set by the dispatcher for the duration of each turn.

**Channel interface** (`internal/channel/channel.go`): `Channel` has `Name() string`, `Listen(ctx, out chan<- Inbound) error`, and `Send(ctx, Outbound) error`. The `Dispatcher` fans out to all registered channels, routes each inbound message to a conversation keyed by `(Channel, Thread)`, and calls back `Send` on the originating channel with the reply.

**Attachments** (`internal/channel/attachment.go`): Channel-agnostic media plumbing. Channels download inbound media with `channel.FetchAttachment`/`SaveAttachment` (25 MB cap, safe filenames, MIME sniffing) into `channel.AttachmentDir(name)` and set `Inbound.Attachments`; `Inbound.MessageID` becomes `Outbound.ReplyTo` so channels can thread replies (groups only). The dispatcher copies attachments onto `Conversation.Attachments` for the turn; the runner (`internal/cli/attachments.go`) lists each by path and, when `ImageInput` is set, inlines png/jpeg/gif/webp ≤5 MB as base64 `image_url` parts. The runner puts the conversation `Key` on the context (`channel.WithKey`) so the `send_file` tool (`internal/tools/send_file.go`) can send a local file to the current chat as `Outbound.Attachments`; each messaging channel's `Send` uploads attachments before text. `channel.DropMetaLines` is shared by all channels.

**iMessage channel** (`internal/channel/imessage/`): BlueBubbles only. On `Listen` it binds the webhook server, pings `/api/v1/ping`, reads `/api/v1/server/info` (private API + helper → enables typing/read receipts), and registers its webhook URL (password in query) via `/api/v1/webhook`, unregistering on shutdown. Only `new-message` events are handled; tapbacks are dropped; message GUIDs are deduped. Only forwards messages from `OwnerHandles`; group chats (`;+;` in chatGuid) additionally require `GroupTag`. Inbound attachments are downloaded into `Inbound.Attachments`. Outbound attachments upload via `/api/v1/message/attachment`; in group chats the first bubble is threaded under `Outbound.ReplyTo` when the private API is on. Outbound text has Markdown stripped (`stripMarkdown`) and meta-lines like `[tokens: ...]` filtered (`dropMetaLines`), then is sent one bubble per paragraph (split past 4000 chars). The REST client (`api.go`) URL-encodes the password and keeps it out of errors; logs redact phone numbers/emails. Handle normalization (`normalizeHandle`) strips service prefixes and normalises phone formatting. The `imessage_send` tool (`internal/tools/imessage_send.go`) reuses the exported `imessage.Client`: phone/email recipients resolve to an existing chat by `chatIdentifier` (never by participant, to avoid leaking DMs into groups), else a new chat is created via `/api/v1/chat/new`.

**Discord channel** (`internal/channel/discord/`): Uses `github.com/bwmarrin/discordgo` to receive messages via the Discord gateway (websocket) and send via REST. Only forwards messages from `OwnerIDs`; guild channels additionally require either an @mention of the bot or the literal `GuildTag` in the message body, which is stripped before dispatch. DMs bypass the tag check. The `Thread` is the Discord channel ID. Outbound meta-lines (`[tokens: ...]`) are filtered and messages over 2000 chars are chunked. Attachments are downloaded from the CDN on receive and uploaded via `ChannelMessageSendComplex`; replies in guild channels reference the triggering message. The `discord_send` tool (`internal/tools/discord_send.go`) lets the agent proactively post to any channel or DM a user (it opens a DM channel automatically via `UserChannelCreate` when `kind=user`).

**Telegram channel** (`internal/channel/telegram/`): Stdlib-only Bot API client. `Listen` calls `getMe`, then long-polls `getUpdates` (exponential backoff on error). Only forwards messages from `OwnerIDs` (private chats are owner-only); groups additionally require an `@botusername` mention, a reply to the bot, or `GroupTag`, which is stripped. `Thread` is the chat ID. Replies are plain text (no `parse_mode`), meta-lines filtered, chunked UTF-8-safely at 4096. Photos (largest size), documents, voice, audio and video are fetched via `getFile` (20 MB Bot API limit), with `caption` used as text; outbound images go via `sendPhoto`, other files via `sendDocument`; group replies use `reply_parameters`. API errors never include the token. `telegram_send` (`internal/tools/telegram_send.go`) reuses `telegram.SendMessage`. In groups the bot receives @mentions and replies even with BotFather privacy mode on (the default).

**Profiles** (`internal/subagents/profile.go`, `internal/channel/router.go`): Top-level agents bound to specific channel threads, loaded from `~/.kroos/profiles/*.md` and `.kroos/profiles/*.md`. Frontmatter: `name`, `description`, `channel` (`discord` | `imessage` | `telegram`), `threads` (list of Discord channel IDs / iMessage chat GUIDs / Telegram chat IDs), `senders` (`owners` default | `any`), `tools`, optional `model`/`base_url`/`api_key_env`/`context_size` (empty model = main agent's client). Body is the system prompt (replaces AGENTS.md body; no IDENTITY/USER context files, no skills listing). `channel.Router` sends bound `(Channel, Thread)` keys to the profile's `Runner`, everything else to main. `senders: any` adds the threads to the channel's open list (Discord `OpenChannels`, iMessage/Telegram `OpenChats`): anyone may address the bot there, but only in group threads (DMs stay owner-only), the mention/tag is still required (iMessage open chats need `GroupTag` set), and content is prefixed `[sender]: `. It also restricts tools to `subagents.PublicSafeTools` (`web_search`, `web_fetch`, `memory`) — the profile is rejected at load otherwise.

## Conventions

- **Minimal external dependencies.** Prefer the standard library. External dependencies are allowed when they earn their keep — pick focused, small, well-maintained packages over reimplementing non-trivial logic. Avoid dependencies that pull large transitive trees, force CGo on the build, or reimplement something the stdlib already does well.
- **Tool parameter structs** are named `<ToolName>Params` with JSON tags matching the schema field names exactly. Unmarshal with `json.Unmarshal(raw, &params)`.
- **Tool definitions** are module-level vars (`var bashDefinition = ToolDefinition{...}`), not generated at runtime.
- **Directory skipping**: grep and find skip `.git`, `node_modules`, `__pycache__`, `.cache` via `filepath.SkipDir`.
- **Output truncation**: Tools enforce default limits (read: 2000 lines, grep: 100 matches, ls: 500 entries, find: 1000 results) and append a `[... limit reached]` marker when truncated.
- **Skill files** are `SKILL.md` with YAML frontmatter (`name`, `description`, `disable-model-invocation`). Name must be lowercase alphanumeric with hyphens, max 64 chars. Description is required.
- **Tests** use `t.TempDir()` for filesystem tests, `httptest.NewServer` for HTTP tests, and `json.Marshal(Params{...})` to build tool input. A shared `writeFile` helper exists in `grep_test.go` and `loader_test.go` for creating temp files.
