---
context_size: 400000
tools: ["*"]
---

# Repository Guidelines

- Repo: https://github.com/s-ramirez/github.com/s-ramirez/kroos-core
- In chat replies, file references must be repo-root relative only (example: `internal/channel/discord/channel.go:80`); never absolute paths or `~/...`.
- Companion bootstrap files: `IDENTITY.md` captures the agent's persona and working style, `USER.md` captures stable collaborator preferences. Keep them aligned with this file when conventions evolve.

## Project Structure & Module Organization

- CLI entrypoint: `cmd/kroos/` (`main.go` wires dependencies; `init.go` bootstraps `$KROOS_HOME`; embedded templates under `cmd/kroos/templates/`).
- Internal packages under `internal/`:
  - `cli/` — interactive runner and agent loop (`runner.go:Runner.agentLoop`).
  - `llm/` — OpenAI-compatible chat completions client.
  - `prompt/` — system prompt assembly.
  - `tools/` — tool implementations (`read`, `write`, `edit`, `bash`, `grep`, `ls`, `find`, `web_fetch`, `web_search`, `imessage_send`, `discord_send`).
  - `channel/` — inbound/outbound messaging channels (`imessage/`, `discord/`).
  - `skills/` — `SKILL.md` loader for user-invocable skills.
  - `subagents/` — multi-provider sub-agent definitions.
  - `memory/` — persistent memory store.
  - `compaction/`, `schedule/`, `plugin/`, `types/` — supporting packages.
- Instance state: `$KROOS_HOME` (default `~/.kroos`) holds `config.json`, `auth.json`, `.env`, `AGENTS.md`, `IDENTITY.md`, `USER.md`, `memory/`, `skills/`, `subagents/`, `profiles/`, `state/`, `tasks/`, `schedule/`, `workspace/`. Nothing instance-specific lives in the code checkout.
- Reference docs and templates live under `docs/reference/`.

## Build, Test, and Development Commands

- Runtime baseline: Go **1.26.1+**.
- Build: `go build ./...`
- Run CLI: `go run ./cmd/kroos` (defaults to the OpenAI API).
- Full test suite: `go test ./...`
- Single package: `go test ./internal/tools/`
- Single test: `go test ./internal/tools/ -run TestGrepToolFindsPattern`
- Verbose: `go test -v ./...`
- Bootstrap a workspace: `kroos init` (writes `AGENTS.md`, `IDENTITY.md`, `USER.md`, `memory/MEMORY.md` into `$KROOS_HOME`, prompts for `$KROOS_HOME/.env` keys).
- Preferred landing bar before pushing `main`: `go build ./...` and `go test ./...` green.
- For narrowly scoped changes, prefer a narrowly scoped test that directly validates the touched behavior. If no meaningful scoped test exists, say so explicitly.
- Do not use scoped tests as permission to ignore plausibly related failures.

## Environment Variables

- Core: `KROOS_PROVIDER` (`chatgpt` default | `omlx`), `KROOS_MODEL` (default: the provider's default model), `KROOS_OMLX_URL`, `KROOS_OMLX_API_KEY`, `KROOS_LLM_TIMEOUT` (default `5m`), `KROOS_SUBAGENT_LLM_TIMEOUT` (default: same as `KROOS_LLM_TIMEOUT`), `KROOS_WEBUI_PORT`, `KROOS_LOG_DIR`, `KROOS_HEARTBEAT_INTERVAL`.
- iMessage (BlueBubbles): `BLUEBUBBLES_ENABLED`, `BLUEBUBBLES_SERVER_URL`, `BLUEBUBBLES_PASSWORD`, `BLUEBUBBLES_WEBHOOK_PORT`, `BLUEBUBBLES_WEBHOOK_PATH`, `BLUEBUBBLES_ALLOWED_USERS`, `BLUEBUBBLES_GROUP_TAG`.
- Discord: `DISCORD_ENABLED`, `DISCORD_BOT_TOKEN`, `DISCORD_ALLOWED_USERS`, `DISCORD_GUILD_TAG`.
- Web search: `KROOS_BRAVE_API_KEY` or `BRAVE_API_KEY`.

## Architecture Notes

- **Agent loop** (`internal/cli/runner.go`): `Runner.agentLoop()` calls the LLM, dispatches any tool calls through the `Registry`, appends results as `"tool"` role messages, and loops until the model responds with text. On empty content after tool calls, retries up to 2 times with a nudge.
- **Tool interface** (`internal/tools/types.go`): `Tool.Definition()` returns metadata + JSON schema; `Tool.Execute(ctx, json.RawMessage)` returns `(ToolResult, error)`. Tool-level errors (bad args, file not found) are returned as `ToolResult{IsError: true}` via `errorResult`/`errorResultf` helpers, not as Go errors.
- **Tool hooks** (`internal/tools/types.go`): `BeforeToolCall` can block execution by returning a reason string; `AfterToolCall` observes results. Hooks register on the `Registry`.
- **Filesystem abstraction** (`internal/tools/fs.go`): Tools that touch files take an `FS` interface. Production uses `OSFS{}`. Directory-walking tools (`grep`, `find`, `bash`) use `os` directly.
- **Skills** (`internal/skills/`): `SKILL.md` files with YAML frontmatter (`name`, `description`, `disable-model-invocation`). Discovered from `$KROOS_HOME/skills/`. Invoke with `/skill:<name> [args]`.
- **System prompt** (`internal/prompt/system.go`): `BuildSystemPrompt(tools, skills, env, ...)` assembles environment info, per-tool guidelines, tool-selection rules, `<available_skills>` block, and injects context files (`IDENTITY.md`, `USER.md`) at the top.
- **Channels** (`internal/channel/`): `Channel` has `Name()`, `Listen(ctx, out chan<- Inbound)`, and `Send(ctx, Outbound)`. The `Dispatcher` fans out to all channels and routes conversations by `(Channel, Thread)`. When refactoring shared channel logic (routing, allowlists, filtering), always consider **all** channels.

## Coding Style & Conventions

- **Minimal external dependencies.** Prefer the standard library. External deps are allowed when they earn their keep — pick focused, small, well-maintained packages over reimplementing non-trivial logic. Avoid anything that pulls a large transitive tree or forces CGo.
- **Tool parameter structs** are named `<ToolName>Params` with JSON tags matching schema field names exactly. Unmarshal with `json.Unmarshal(raw, &params)`.
- **Tool definitions** are module-level vars (`var bashDefinition = ToolDefinition{...}`), not generated at runtime.
- **Directory skipping**: `grep` and `find` skip `.git`, `node_modules`, `__pycache__`, `.cache` via `filepath.SkipDir`.
- **Output truncation**: enforce default limits (`read`: 2000 lines, `grep`: 100 matches, `ls`: 500 entries, `find`: 1000 results) and append `[... limit reached]` when truncated.
- **Skill files** are `SKILL.md` with YAML frontmatter. `name` must be lowercase alphanumeric with hyphens, max 64 chars. `description` is required.
- Keep files concise; extract helpers instead of "V2" copies. Aim for <500 LOC per file as a guideline.
- Add brief comments only for non-obvious logic. Don't narrate what well-named identifiers already say.
- Written English: American spelling in code, comments, docs, UI strings.

## Testing Guidelines

- Framework: standard `testing` package.
- Filesystem tests use `t.TempDir()`. HTTP tests use `httptest.NewServer`. Build tool inputs with `json.Marshal(Params{...})`.
- A shared `writeFile` helper exists in `grep_test.go` and `loader_test.go` for creating temp files.
- Naming: match source names with `*_test.go`.
- Run `go test ./...` before pushing when you touch logic.
- Do not modify snapshot, baseline, or expected-failure files to silence failing checks without explicit approval.

## Commit & Pull Request Guidelines

- Commit messages: concise, action-oriented (e.g., `tools: add find limit marker`).
- Group related changes; avoid bundling unrelated refactors.
- Prefer creating new commits over amending published ones.
- Never skip hooks (`--no-verify`) or bypass signing without explicit approval.

## Security & Configuration Tips

- Never commit real API keys, phone numbers, tokens, or live configuration values. Use obviously fake placeholders in docs, tests, and examples.
- `.env` is loaded automatically but must never be committed.
- Web search credentials live in `KROOS_BRAVE_API_KEY` / `BRAVE_API_KEY`.
- Channel credentials (`BLUEBUBBLES_PASSWORD`, `DISCORD_BOT_TOKEN`) stay in `.env`; do not echo them in logs or replies.

## Collaboration / Safety Notes

- When working on a GitHub issue or PR, print the full URL at the end of the task.
- When answering questions, respond with high-confidence answers only: verify in code; do not guess.
- **Multi-agent safety:** do not create/apply/drop `git stash` entries unless explicitly requested. Do not create/remove/modify `git worktree` checkouts. Do not switch branches unless explicitly requested.
- **Multi-agent safety:** when you see unrecognized files, keep going; focus on your changes and commit only those.
- Bug investigations: read the source of relevant dependencies and all related local code before concluding; aim for a high-confidence root cause.
- Destructive or hard-to-reverse actions (`git reset --hard`, force push, deleting branches, dropping migrations) require explicit user approval every time.

## Core Preservation Policy

- Treat `cmd/kroos/` and `internal/` as the product core. Avoid changing them unless the user explicitly asks for a core change or there is no credible extension-point alternative.
- Prefer solving new behavior in `$KROOS_HOME` first: skills, memory, subagents, docs, scripts, generated assets, small helper apps, and other workspace-local artifacts are fair game.
- Before proposing a core edit, first check whether a skill, prompt/context update, plugin, wrapper script, config/env change, or `$KROOS_HOME` artifact can achieve the goal with less churn.
- If a core edit is truly necessary, say why the extension-point path is insufficient before making the change.
- Uncommitted changes in core files are not yours to tidy up or build on casually. Treat them as high-risk and avoid touching them unless explicitly required.
