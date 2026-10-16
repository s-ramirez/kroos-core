# github.com/s-ramirez/kroos-core

## Features

- Interactive CLI loop with an agentic tool-use cycle
- OpenAI-compatible `/v1/chat/completions` client
- Optional bearer-token auth for OpenAI-compatible endpoints
- Dynamic system prompt with environment info and tool-selection rules
- Built-in tools: `read`, `write`, `edit`, `bash`, `grep`, `ls`, `find`, `web_fetch`, `web_search`
- Plugin system with startup-registered plugins; `tilldone` is currently disabled
- Multi-provider sub-agents and customizable main agent via `.kroos/agent.md`

## Prerequisites

- Go 1.26.1 or later
- An OpenAI API key, or another OpenAI-compatible chat completions API

## Running

```sh
go run ./cmd/kroos
```

By default the CLI connects to OpenAI using `gpt-5.4`. Configure with
environment variables. The CLI also loads a local `.env` file automatically,
which is useful for development defaults:

```env
KROOS_WEBUI_PORT=8420
KROOS_LOG_DIR=.kroos/logs
```

Available settings:

| Variable            | Default                     | Description                                           |
|---------------------|-----------------------------|-------------------------------------------------------|
| `KROOS_BASE_URL`      | `https://api.openai.com`    | Base URL of the chat completions API                  |
| `KROOS_MODEL`         | `gpt-5.4`                   | Model name to use in API requests                     |
| `KROOS_API_KEY`       | unset                       | Optional bearer token for authenticated APIs          |
| `KROOS_LLM_TIMEOUT`   | `5m`                        | Timeout for primary and heartbeat LLM requests        |
| `KROOS_SUBAGENT_LLM_TIMEOUT` | `KROOS_LLM_TIMEOUT` | Timeout for sub-agent LLM requests                    |
| `KROOS_WEBUI_PORT`    | `8420`                      | Enables the web UI plugin on the given port           |
| `KROOS_LOG_DIR`       | unset                       | Writes session logs under a timestamped subdirectory  |
| `KROOS_BRAVE_API_KEY` | unset                       | Brave Search API key for the `web_search` tool        |
| `BRAVE_API_KEY`     | unset                       | Alternate Brave Search API key env var                |

Example with a custom endpoint:

```sh
KROOS_BASE_URL=http://localhost:8080 KROOS_MODEL=my-model go run ./cmd/kroos
```

Example with OpenAI:

```sh
KROOS_BASE_URL=https://api.openai.com \
KROOS_MODEL=gpt-4o-mini \
KROOS_API_KEY=your-openai-api-key \
go run ./cmd/kroos
```

## Multi-provider sub-agents

Each sub-agent under `.kroos/subagents/` or `~/.kroos/subagents/` can target its
own provider via three frontmatter fields:

| Field         | Description                                                              |
|---------------|--------------------------------------------------------------------------|
| `model`       | Model name passed to the API verbatim                                    |
| `base_url`    | API base URL (the client appends `/v1/chat/completions`)                 |
| `api_key_env` | Name of an env var holding the API key (resolved at spawn time)          |

If `base_url` is omitted, the global `KROOS_BASE_URL` is used. If `api_key_env`
is set but the env var is empty, no `Authorization` header is sent. Put
secrets in `.env` and reference them by env var name —
never commit keys.

Example sub-agents shipped in `.kroos/subagents/`: `gpt.md` (OpenAI),
`fireworks.md` (Fireworks), `local.md` (OMLX).

`.env`:

```env
OPENAI_API_KEY=sk-...
FIREWORKS_API_KEY=fw-...
```

## Customizing the main agent

Drop a `.kroos/agent.md` (project) or `~/.kroos/agent.md` (user) to customize the
orchestrator. Same frontmatter as sub-agents, plus the body becomes a
`# Personality` section appended to the auto-built system prompt.

`tools` and `model` are required. `tools` filters the available builtin and
plugin tools; `plugins` (optional) restricts which plugins load. When
`api_key_env` is omitted, the main agent sends no `Authorization` header.

```yaml
---
model: gpt-4.1
base_url: https://api.openai.com
api_key_env: OPENAI_API_KEY
tools: [read, write, edit, bash, grep, ls, find]
plugins: [webui]
---

You are a senior Go engineer. Prefer minimal diffs and be terse.
```

## Testing

Run the full test suite:

```sh
go test ./...
```

Run tests for a specific package:

```sh
go test ./internal/tools/...
go test ./internal/prompt/...
go test ./internal/cli/...
```

Run a single test by name:

```sh
go test ./internal/tools/ -run TestGrepToolFindsPattern
```

Run with verbose output:

```sh
go test -v ./...
```

## Project structure

```
cmd/kroos/             CLI entrypoint
internal/
  cli/               Interactive runner and agent loop
  llm/               Chat completions client
  prompt/            System prompt generation
  tools/             Tool implementations (read, write, edit, bash, grep, ls, find, web_fetch, web_search)
  types/             Shared request/response types
```

## Notes

The implementation intentionally stays close to the current TypeScript behavior.
Future passes can add bounded history, transports, scheduling, and persistent memory.
