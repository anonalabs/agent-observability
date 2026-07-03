# GitHub Copilot coding agent, Codex, OpenCode, and Antigravity

These four share the same hook-processing pipeline as Cursor (`internal/cursorhook`, see [docs/cursor.md](cursor.md) for how spans/context-linking/export work) -- only the config file each one reads and its own hook event vocabulary differ. Every event, regardless of source, ends up normalized onto the same span model and lands in the same `otel_traces` table, so the Agent Leaderboard and Session Timeline dashboards already work across all of them without any per-agent dashboard changes.

## GitHub Copilot coding agent

```bash
agentobs connect --agent copilot
```

Repo-scoped only -- Copilot has no global/user-level hook config, so this always writes `.github/hooks/otel-hooks.json` (merged, never replaced) plus `.github/hooks/otel_config.json` and `.github/hooks/otel_hook.sh` in the current directory. Nothing else to do afterward.

## Codex

```bash
agentobs connect --agent codex
```

Writes `~/.codex/hooks.json` (merged) and its own wrapper/config under `~/.codex/hooks/`, and additionally enables `codex_hooks = true` under `[features]` in `~/.codex/config.toml` -- Codex requires that flag before it'll actually run any hooks, writing `hooks.json` alone isn't enough. This step only adds that one key; it never touches anything else already in `config.toml`.

## OpenCode

```bash
agentobs connect --agent opencode
```

OpenCode has no `hooks.json` -- its plugin file *is* the registration. This writes `~/.config/opencode/plugins/agentobs-otel-hook.ts` (a small plugin that pipes each OpenCode event to `agentobs cursor-hook` on stdin) plus its own config JSON alongside it. Global by default; for project scope, move both files into `.opencode/plugins/` in the repo instead. Restart OpenCode afterward to load the plugin.

## Antigravity (or any other hook-compatible runner)

Antigravity has no dedicated `agentobs` integration -- there's nothing to merge into, no config file `connect` can write. If its hook/workflow system can run an arbitrary command and pipe JSON to it on stdin, point it at the same binary directly:

```bash
export OTEL_SERVICE_NAME=antigravity-agent
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
# pipe the runner's own hook JSON to:
agentobs cursor-hook
```

`agentobs cursor-hook` reads config from `OTEL_*` env vars if no `--config` file is given (same precedence as every other agent). Whatever hook event name field the runner sends (`hook_event_name`/`hook_event_type`/hook JSON's own `event` key) is normalized the same way Codex's and OpenCode's are -- unrecognized event names still produce a span, just with a generic `chain` kind and `unknown` operation instead of being dropped.

## Verified vs. unverified

Only Cursor has been round-tripped against the real IDE in this project's testing (see docs/cursor.md's history). Copilot/Codex/OpenCode's hook-processing has been verified with synthetic JSON matching each tool's documented payload shape (spans land correctly in ClickHouse with the right `ServiceName`, event mapping, and error status), but not against the real tools themselves -- if one of their hook runners turns out to expect a different stdout response shape than the empty ack these three currently get, or a field name this doesn't already handle, that's a small, isolated fix once someone can test against the real tool.
