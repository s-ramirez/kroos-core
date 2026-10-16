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

## Instance home

Everything specific to one kroos instance lives in **`$KROOS_HOME`** (default
`~/.kroos`), never in the code checkout:

```
$KROOS_HOME/
  config.json      settings (kroos config set …)
  .env             secrets and env overrides (also where the agent's secret tool writes)
  auth.json        ChatGPT tokens
  AGENTS.md  IDENTITY.md  USER.md
  skills/  subagents/  profiles/  memory/
  state/           conversations
  schedule/  tasks/  workspaces/  logs/
  workspace/       working directory when run as a service
```

So anyone can run their own instance from the same code. Clone the repo and
point `KROOS_HOME` wherever you like (one directory per instance). Pulling new
code never touches instance data. `KROOS_HOME` is read from the real
environment only, not from `.env` or `config.json`.

## Prerequisites

- Go 1.26.1+
- A ChatGPT account and/or an oMLX server

## Quick start

```sh
go run ./cmd/kroos init    # scaffold $KROOS_HOME and run the setup wizard
go run ./cmd/kroos
```

`kroos init` is an interactive, menu-driven setup you can re-run any time:

```
== Setup ==
  1. Providers          default chatgpt · chatgpt: logged in · gpt-5.4 · omlx: http://host:8000 · auto model
  2. Channels           discord: enabled · telegram: not configured · imessage: disabled
  3. Heartbeat          every 30m → discord
  4. Extras             web UI :8420
  5. Done
```

- **Providers:** log in to ChatGPT, set the oMLX URL and key (the connection
  is tested), pick each default model from the live list, choose the default
  provider and fallback.
- **Channels:** enable or disable Discord, Telegram or iMessage and fill in
  their settings. Disabling keeps credentials for later.
- **Heartbeat** and **Extras:** web UI, web search key, logging.

It starts from your current `$KROOS_HOME/config.json`. Enter keeps a value,
`-` clears it, secrets are masked (and hidden while typing), and every change
is saved immediately. Templates (`AGENTS.md`, `IDENTITY.md`, `USER.md`) are
written only if missing.

Or install the binary: `go install ./cmd/kroos` (lands in `~/go/bin`).

## Providers

| Provider  | Auth                                                     | Endpoint                                |
|-----------|----------------------------------------------------------|-----------------------------------------|
| `chatgpt` | `kroos login` (device code; tokens in `$KROOS_HOME/auth.json`, auto-refreshed) | `chatgpt.com/backend-api/codex` (Responses API) |
| `omlx`    | `omlx.api_key`                                           | `omlx.url` + `/v1/chat/completions`     |

`kroos logout` removes the ChatGPT tokens.

**Interchangeable and fault-tolerant.** Either provider works on its own:

- At startup, if the selected provider isn't ready (no login, no URL), kroos
  warns and starts on the other one. It never exits for this.
- Every request tries the selected provider first, then the other one on *its*
  default model. A failed provider is skipped for 1 minute. Turn off with
  `kroos config set fallback false`.
- Logging in mid-run is picked up without a restart.

**Remote oMLX** (e.g. over Tailscale): set `omlx.url` to the host's address.
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
kroos models select omlx:gemma-4-31b-it-4bit
```

In a session:

```
/model                       list models and show the current one
/model 2                     switch this conversation (number, name, or provider:model)
/model default               back to the default
```

The `/model` choice is per conversation and persists with its history.

## Self-healing

Kroos keeps running when parts of it fail, and can fix itself with your approval:

- **Isolation:** a channel that can't connect (bad token, unreachable server)
  is retried with backoff while everything else keeps working. Providers fall
  back to each other.
- **Alerts:** after 3 consecutive failures kroos opens an incident and posts it
  to a working channel's home thread (or the CLI):
  `⚠️ kroos: channel:telegram is failing (3 attempts): … Reply /doctor to let me investigate and fix it.`
- **`/doctor [note]`:** starts an isolated investigation and reports back in the
  same chat (Problem / Fixed / You need to). The doctor reads logs and config,
  tests connectivity, and then:
  - changes non-secret settings and restarts channels or the whole process (as a service)
  - for code bugs, fixes them in a git worktree on a `doctor/<slug>` branch,
    runs the tests, and opens a PR with `gh pr create`. It never pushes to main
    or merges.
  - it can't set credentials. It tells you exactly which key to set.

Guardrails block the doctor from changing the live checkout, pushing anything
but `doctor/*` branches, force-pushing, merging, or writing `auth.json` or
`.env`. PRs need `git push` (SSH key without a passphrase, or a credential
helper) and an authenticated `gh` on the machine running kroos.

## Configuration

Every setting has a dotted key. `kroos config keys` lists them all.

```sh
kroos config set provider omlx           # $KROOS_HOME/config.json
kroos config show
kroos --provider omlx --model qwen ...   # per-run flags
kroos --set heartbeat.interval=15m
```

Precedence: flag > env > `$KROOS_HOME/.env` > `$KROOS_HOME/config.json`. See
`.env.example`.

Core settings:

| Key                    | Env                         | Default                      |
|------------------------|-----------------------------|------------------------------|
| `provider`             | `KROOS_PROVIDER`            | `chatgpt`                    |
| `model`                | `KROOS_MODEL`               | provider default             |
| `chatgpt.model`        | `KROOS_CHATGPT_MODEL`       | `gpt-5.4`                    |
| `omlx.model`           | `KROOS_OMLX_MODEL`          | first served                 |
| `omlx.url`             | `KROOS_OMLX_URL`            | unset                        |
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

Sub-agents (`$KROOS_HOME/subagents/*.md`) and profiles (`$KROOS_HOME/profiles/*.md`) pick
a provider in frontmatter:

| Field      | Description                                              |
|------------|----------------------------------------------------------|
| `model`    | Model name passed verbatim                               |
| `provider` | `chatgpt` or `omlx`. Omitted = the main agent's provider |

Fallback applies to them too. Shipped examples: `gpt.md` (ChatGPT) and
`local.md` (oMLX).

## Customizing the main agent

`$KROOS_HOME/AGENTS.md` uses the same
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

`deploy/macos/` runs kroos as launchd agents on an always-on Mac (e.g. a Mac
mini). The code checkout holds only source and `bin/`. The agent runs with
`KROOS_HOME` set and its working directory in `$KROOS_HOME/workspace`.

- `com.kroos.agent`: runs `bin/kroos` and keeps it alive
- `com.kroos.deploy`: every 5 min, pulls `origin/main`, then tests, builds and
  restarts if it changed. The old binary keeps running if tests fail.
- `com.kroos.menubar`: menu bar status with restart, deploy-now and log shortcuts

Setup on the target machine:

```sh
git clone git@github.com:s-ramirez/kroos-core.git ~/kroos-core && cd ~/kroos-core
deploy/macos/install.sh                  # KROOS_HOME=/path deploy/macos/install.sh for a custom home
bin/kroos init                           # wizard: ChatGPT login, oMLX, channels, heartbeat
launchctl kickstart -k gui/$(id -u)/com.kroos.agent
```

Run `kroos login` on each machine: ChatGPT rotates refresh tokens, so machines
sharing one token log each other out. Use the same `KROOS_HOME` for these
`bin/kroos` commands as you gave `install.sh`.

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
