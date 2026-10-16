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
| `KROOS_PROVIDER`      | `chatgpt`                   | `chatgpt` (subscription, `kroos login`) or `omlx`     |
| `KROOS_MODEL`         | `gpt-5.4`                   | Model name to use in API requests                     |
| `KROOS_OMLX_URL`      | `http://100.111.194.53:8000`| oMLX server (chat completions)                        |
| `KROOS_OMLX_API_KEY`  | unset                       | oMLX API key                                          |
| `KROOS_LLM_TIMEOUT`   | `5m`                        | Timeout for primary and heartbeat LLM requests        |
| `KROOS_SUBAGENT_LLM_TIMEOUT` | `KROOS_LLM_TIMEOUT` | Timeout for sub-agent LLM requests                    |
| `KROOS_WEBUI_PORT`    | `8420`                      | Enables the web UI plugin on the given port           |
| `KROOS_LOG_DIR`       | unset                       | Writes session logs under a timestamped subdirectory  |
| `KROOS_BRAVE_API_KEY` | unset                       | Brave Search API key for the `web_search` tool        |
| `BRAVE_API_KEY`     | unset                       | Alternate Brave Search API key env var                |

ChatGPT (default): sign in once with your ChatGPT account, then run.

```sh
go run ./cmd/kroos login      # device-code flow; tokens in ~/.kroos/auth.json
go run ./cmd/kroos
```

oMLX:

```sh
kroos config set omlx.api_key <key>
kroos --provider omlx --model mlx-community/gemma-4-12B-it-8bit
```

Models: `kroos models` lists what each provider offers; `kroos models select`
picks one and saves it. Inside a session, `/model` lists models and
`/model <number|name|provider:model>` switches the current conversation
(`/model default` reverts).

## Sub-agent providers

Each sub-agent under `.kroos/subagents/` or `~/.kroos/subagents/` picks its
provider with frontmatter:

| Field      | Description                                                 |
|------------|-------------------------------------------------------------|
| `model`    | Model name passed to the API verbatim                       |
| `provider` | `chatgpt` or `omlx`; omitted = the main agent's provider    |

Example sub-agents shipped in `.kroos/subagents/`: `gpt.md` (ChatGPT),
`local.md` (oMLX).

## Customizing the main agent

Drop a `.kroos/agent.md` (project) or `~/.kroos/agent.md` (user) to customize the
orchestrator. Same frontmatter as sub-agents, plus the body becomes a
`# Personality` section appended to the auto-built system prompt.

`tools` and `model` are required. `tools` filters the available builtin and
plugin tools; `plugins` (optional) restricts which plugins load. `provider`
(optional) overrides `KROOS_PROVIDER`.

```yaml
---
model: gpt-5.4
provider: chatgpt
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
