# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Self-hosted OpenTelemetry pipeline for AI coding agents (Claude Code, Gemini CLI, Cursor, GitHub Copilot coding agent, Codex, OpenCode): OTel Collector → Prometheus (metrics) + ClickHouse (logs + traces) → Grafana. Two moving parts: the Docker Compose stack (`docker-compose*.yml`, `collector/`, `grafana/`) and the `agentobs` Go CLI (`cli/`) that installs it and wires agents up to it.

## Commands

```bash
cd cli && go build -o agentobs ./cmd/agentobs   # build (binary is gitignored)
cd cli && go test ./...                          # tests (only internal/config has any today)
cd cli && go test -run TestFlagWinsOverYAML ./internal/config   # single test
cd cli && go vet ./...
```

No linter config, no Makefile, no CI beyond `.github/workflows/release.yml` (goreleaser on `v*` tags).

Running the stack (from repo root, since `compose.DefaultComposeFile` searches upward from cwd for `docker-compose.yml`):

```bash
./cli/agentobs install            # + --secure | --cloud aws|gcp|azure (mutually exclusive)
./cli/agentobs connect            # --agent claude-code|gemini-cli|cursor|copilot|codex|opencode
./cli/agentobs status             # HTTP health check of all 4 services
./cli/agentobs config-alerts      # regenerates grafana/provisioning/alerting/*.yaml, restarts Grafana
docker compose logs -f otel-collector
```

`AGENTOBS_COMPOSE_FILE` overrides compose-file discovery.

## Architecture

### Signal routing (why the split)

Metrics are low-cardinality time series (tokens, cost, session counts) → Prometheus. Logs are high-cardinality per-event records (prompts, tool calls, API errors) → ClickHouse `otel_logs`. Traces (from the hook shim, for agents with no native OTel) → ClickHouse `otel_traces`. Nothing in `collector/otel-collector-config.yaml` is agent-specific — it just consumes OTLP, which is what makes new agents a config change.

The cross-agent dashboards (`agent-leaderboard.json`, `session-timeline.json`) are the point of the project: they `UNION` across `otel_logs` and `otel_traces`, normalizing on `ServiceName` + session id, so one session renders the same regardless of which agent produced it. Any change to service naming or session-id attributes breaks those queries.

### Three ways an agent gets connected

1. **Declarative spec** (`cli/internal/agents/builtin.yaml`, loaded by `registry.go`, plus user-supplied `~/.config/agentobs/agents.yaml`). Two kinds: `env` (print/write shell `export` lines — Claude Code) and `json-merge` (set dotted paths in a settings JSON — Gemini CLI). **An agent with native OTel export should be added here, not in Go.** Values may be `{{.Endpoint}}` or `{{.<prompt_name>}}`; `resolveValue` substitutes the whole value type-preserved (bool stays bool), it is not a general template engine.
2. **`HookAgent` interface** (`hookagent.go`; implemented by `cursor.go`, `copilot.go`, `codex.go`). For agents with no native OTel and a `hooks.json`-shaped config that needs array-merge logic the flat json-merge setter can't express. `connectHookAgent` in `commands/connect.go` drives all three generically — writing an otel config JSON, a wrapper shell script that `exec`s `agentobs cursor-hook`, and a merged hooks.json.
3. **OpenCode** (`opencode.go` + `connectOpenCode`), its own path: no hooks.json exists to merge into, the dropped-in plugin file *is* the registration.

Codex additionally needs `codex_hooks = true` written into `~/.codex/config.toml` (`EnableHooksFeature`) — hooks.json alone does nothing there.

### The hook shim (`cli/internal/cursorhook/`)

Named for Cursor (first supported) but agent-agnostic. Invoked once per hook event as a **fresh process**, reads the event JSON on stdin, emits one OTLP span, prints the response the runner expects on stdout.

- `normalizeEvent`/`canonicalEvent` (`hook.go`) map each tool's own event vocabulary — Cursor/Copilot camelCase, Codex/OpenCode PascalCase — onto one canonical set. Unrecognized names pass through and still produce a `chain`/`unknown` span rather than being dropped.
- The agent identity comes from `Config.ServiceName` alone (`spanNamePrefix` trims `-agent`), which is why one binary serves every hook agent — each wrapper's config.json sets its own service name.
- Because each event is a separate process, parent/child linkage goes through a file-based store in `$TMPDIR/agentobs_cursor_context` (`context.go`, flock-guarded, externally-supplied IDs hashed before use as filenames). Session root trace IDs are derived deterministically from `conversation_id` via SHA-256.
- OTLP is encoded against the protobuf wire format directly, not the otel-go SDK, deliberately: externally-computed trace/span IDs can't be injected cleanly through the SDK.
- `generateResponse` returns `{"permission":"allow"}` only for Cursor's pre-approval events. Do **not** extend that to other agents' permission-shaped events without testing against the real runner — a wrong ack shape can silently block them.
- Attributes follow GenAI (`gen_ai.*`) + LangSmith (`langsmith.*`) conventions; the dashboards query those names.

### Config precedence

`config.Resolve` (`cli/internal/config/config.go`) is the single point of truth: **flag > YAML (`--config`) > interactive prompt > documented default**, erroring under `--non-interactive` when there's no default. Every command uses it; don't read flags directly for user-facing settings. Flags are only treated as "set" via `cmd.Flags().Changed(name)` (see `flagOrNil`), so a `false` bool flag is distinguishable from an unset one.

## Invariants worth preserving

- **Never overwrite user config.** Every write path calls `backupIfExists` (`.bak`) and merges into existing content. Shell rc writes go between `# BEGIN/END agentobs connect (<agent>)` markers and replace the prior block rather than appending (`writeShellRcBlock`).
- **Spec values are untrusted** (they can come from a downloaded `agents.yaml`). Env var names are validated by `isShellSafeIdentifier` and values single-quoted via `shellQuote` — single quotes specifically, because double quotes still allow `$(...)`/backtick substitution in a shell rc file.
- **Privacy defaults off.** `log_user_prompts` / `log_tool_details` / `mask_prompts` all default false-safe; prompt and tool content is never captured unless explicitly enabled per `connect` run.
- **`config-alerts` generates YAML from Go structs**, never string templates — thresholds and webhooks are never baked into the binary. It regenerates `rules.yaml` from scratch each run and emits an explicit `deleteRules` block, because Grafana's file provisioner otherwise leaves stale rules behind. `contactpoints.yaml` is gitignored (holds the webhook URL); `contactpoints.yaml.example` is the tracked template.
- **Grafana datasource UIDs are literally `Prometheus` and `ClickHouse`** (`grafana/provisioning/datasources/datasources.yml`); dashboards and generated alert rules hardcode those.
- `--secure` and `--cloud` can't be combined — both replace the collector config wholesale.

## Adding an agent

If it speaks OTel: add an entry to `builtin.yaml`. If it doesn't: implement `HookAgent`, register it in `hookAgents()` in `commands/connect.go`, add its event-name mappings to `canonicalEvent` in `cursorhook/hook.go`, and add a next-step line to `hookAgentNextStep`. Docs live in `docs/cursor.md` (attribute reference) and `docs/other-agents.md`.

## Commit style

Commit every change. Never reference Claude/AI authorship in commit messages.
