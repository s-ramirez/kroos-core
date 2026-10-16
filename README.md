# kroos

A personal agent in Go: a tool-using agent loop that talks to you from the
terminal, iMessage, Discord and Telegram, runs scheduled background tasks, and
manages its own skills and memory.

## Features

- Agent loop with built-in tools (`read`, `write`, `edit`, `bash`, `grep`, `ls`, `find`, `web_fetch`, `web_search`, `memory`, `task`, `send_file`, …)
- Two interchangeable LLM providers, each usable alone, with automatic fallback:
  - **ChatGPT** via your ChatGPT subscription (`kroos login`)
  - **oMLX**, a self-hosted OpenAI-compatible server
- Model listing and switching: `kroos models`, `/model` in-session
- Channels: CLI, iMessage (BlueBubbles), Discord, Telegram
- Background tasks and heartbeat on a scheduler, with delivery to any channel
- Skills (`SKILL.md`) the agent can create, patch and curate itself
- Sub-agents, channel-bound profiles, persistent memory
- macOS launchd deployment that auto-deploys `main`

## Prerequisites

- Go 1.26.1+
- A ChatGPT account and/or an oMLX server

## Quick start

```sh
go run ./cmd/kroos init                         # scaffold .kroos/ (AGENTS.md, IDENTITY.md, USER.md, …)
go run ./cmd/kroos login                        # ChatGPT sign-in (optional)
go run ./cmd/kroos config set omlx.api_key <key> # oMLX key (optional)
go run ./cmd/kroos
```

Or install the binary: `go install ./cmd/kroos` (lands in `~/go/bin`).

## Providers

| Provider  | Auth                                                     | Endpoint                                |
|-----------|----------------------------------------------------------|-----------------------------------------|
| `chatgpt` | `kroos login` (device code; tokens in `~/.kroos/auth.json`, auto-refreshed) | `chatgpt.com/backend-api/codex` (Responses API) |
| `omlx`    | `omlx.api_key`                                           | `omlx.url` + `/v1/chat/completions`     |

`kroos logout` removes the ChatGPT tokens.

**Interchangeable and fault-tolerant.** Either provider works on its own:

- At startup, if the selected provider isn't ready (no login, no URL), kroos
  warns and starts on the other one. It never exits for this.
- Every request tries the selected provider first, then the other one on *its*
  default model. A failed provider is skipped for 1 minute. Turn off with
  `kroos config set fallback false`.
- Logging in mid-run is picked up without a restart.

**oMLX over Tailscale.** The default `omlx.url` is the Mac Studio's Tailscale IP.
oMLX must listen on all interfaces (`--host 0.0.0.0`), not just `127.0.0.1`.

## Models

Each provider has its own default model, so switching providers never sends a
model name to the wrong one:

| Key             | Default        | Meaning                                       |
|-----------------|----------------|-----------------------------------------------|
| `chatgpt.model` | `gpt-5.4`      | ChatGPT default                               |
| `omlx.model`    | first served   | oMLX default (empty = first model listed)     |
| `model`         | unset          | Override for the selected provider            |

```sh
kroos models                 # list models from both providers (* = current)
kroos models omlx            # one provider
kroos models select          # numbered picker; saves provider + <provider>.model
kroos models select 3        # or pick by number, name, or provider:model
kroos models select --project omlx:gemma-4-31b-it-4bit
```

In a session:

```
/model                       list models and show the current one
/model 2                     switch this conversation (number, name, or provider:model)
/model default               back to the default
```

The `/model` choice is per conversation and persists with its history.

## Configuration

Every setting has a dotted key. `kroos config keys` lists them all.

```sh
kroos config set provider omlx           # ~/.kroos/config.json
kroos config set model gpt-5.4 --project # .kroos/config.json
kroos config show
kroos --provider omlx --model qwen ...   # per-run flags
kroos --set heartbeat.interval=15m
```

Precedence: flag > env > `.env` > project config > global config. A local
`.env` is loaded automatically; see `.env.example`.

Core settings:

| Key                    | Env                         | Default                      |
|------------------------|-----------------------------|------------------------------|
| `provider`             | `KROOS_PROVIDER`            | `chatgpt`                    |
| `model`                | `KROOS_MODEL`               | provider default             |
| `chatgpt.model`        | `KROOS_CHATGPT_MODEL`       | `gpt-5.4`                    |
| `omlx.model`           | `KROOS_OMLX_MODEL`          | first served                 |
| `omlx.url`             | `KROOS_OMLX_URL`            | `http://100.111.194.53:8000` |
| `omlx.api_key`         | `KROOS_OMLX_API_KEY`        | unset                        |
| `fallback`             | `KROOS_FALLBACK`            | `true`                       |
| `llm.timeout`          | `KROOS_LLM_TIMEOUT`         | `5m`                         |
| `llm.subagent_timeout` | `KROOS_SUBAGENT_LLM_TIMEOUT`| `llm.timeout`                |
| `webui.port`           | `KROOS_WEBUI_PORT`          | unset                        |
| `log_dir`              | `KROOS_LOG_DIR`             | unset                        |
| `web_search.brave_api_key` | `KROOS_BRAVE_API_KEY`   | unset                        |

Channel (`imessage.*`, `discord.*`, `telegram.*`), heartbeat and skill settings
are listed by `kroos config keys`.

## Sub-agents and profiles

Sub-agents (`.kroos/subagents/*.md`) and profiles (`.kroos/profiles/*.md`) pick
a provider in frontmatter:

| Field      | Description                                              |
|------------|----------------------------------------------------------|
| `model`    | Model name passed verbatim                               |
| `provider` | `chatgpt` or `omlx`. Omitted = the main agent's provider |

Fallback applies to them too. Shipped examples: `gpt.md` (ChatGPT) and
`local.md` (oMLX).

## Customizing the main agent

`.kroos/agent.md` (project) or `~/.kroos/agent.md` (user) uses the same
frontmatter. Its body becomes a `# Personality` section of the system prompt.
`tools` and `model` are required. `plugins` and `provider` are optional.

```yaml
---
model: gpt-5.4
provider: chatgpt
tools: [read, write, edit, bash, grep, ls, find]
plugins: [webui]
---

You are a senior Go engineer. Prefer minimal diffs and be terse.
```

## Deploying (macOS)

`deploy/macos/` runs kroos as launchd agents on an always-on Mac (e.g. a Mac mini):

- `com.kroos.agent`: runs `bin/kroos` and keeps it alive
- `com.kroos.deploy`: every 5 min, pulls `origin/main`, then tests, builds and
  restarts if it changed. The old binary keeps running if tests fail.
- `com.kroos.menubar`: menu bar status with restart, deploy-now and log shortcuts

Setup on the target machine:

```sh
git clone <repo> ~/kroos-prod && cd ~/kroos-prod
deploy/macos/install.sh
go build -o bin/kroos ./cmd/kroos
bin/kroos login                          # run on this machine; don't copy auth.json
bin/kroos config set omlx.api_key <key>
launchctl kickstart -k gui/$(id -u)/com.kroos.agent
```

Credentials live in `~/.kroos/`. Run `kroos login` on each machine: ChatGPT
rotates refresh tokens, so machines sharing one token log each other out.

Check it:

```sh
bin/kroos models
tail -f ~/Library/Logs/kroos/agent.log    # also deploy.log
```

In `agent.log`, `warning: chatgpt unavailable … using omlx` means the ChatGPT
login is missing. `[llm] … falling back to omlx` marks a live fallback.

## Testing

```sh
go test ./...
go test ./internal/llm/
go test ./internal/tools/ -run TestGrepToolFindsPattern
```

## Project structure

```
cmd/kroos/          CLI entrypoint (run, init, login, models, config)
deploy/macos/       launchd agents, deploy script, menu bar app
internal/
  channel/          Channel interface, dispatcher, CLI/iMessage/Discord/Telegram
  cli/              Agent loop runner, skill review, curator
  compaction/       Context compaction
  config/           Config keys and files
  heartbeat/        Heartbeat task registration
  llm/              Providers: ChatGPT (Responses API), oMLX, fallback, model listing
  memory/           Persistent memory
  plugin/           Plugins (web UI, …)
  prompt/           System prompt
  schedule/         Scheduler
  skills/           Skill loading, registry, usage
  subagents/        Sub-agents and profiles
  tasks/            Background task executor
  tools/            Tool implementations
  types/            Shared message types
```
