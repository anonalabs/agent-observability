# agentobs

Go CLI for installing, connecting, and exporting AI coding agent telemetry.

```bash
go build -o agentobs ./cmd/agentobs
# or: go install ./cmd/agentobs

./agentobs install    # bring up the collector/prometheus/clickhouse/grafana stack
./agentobs connect    # wire up an agent's telemetry (Claude Code, Gemini CLI, Cursor, Copilot, Codex, OpenCode)
./agentobs export     # pull metrics/logs out to a file
./agentobs status     # health check the stack
./agentobs config-alerts   # set alert thresholds + Slack/Discord webhook, no YAML editing
```

## Layout

- `cmd/agentobs/`: entry point, wires up cobra subcommands.
- `internal/config/`: YAML/flag/prompt precedence resolution, shared by every command.
- `internal/compose/`: `docker compose` wrapper.
- `internal/agents/`: declarative agent specs (`builtin.yaml` + `registry.go`) for env-var and JSON-merge style agents (Claude Code, Gemini CLI, and anything added via `~/.config/agentobs/agents.yaml`), plus the hook-based agents that need real code instead of just config: `cursor.go`, `copilot.go`, `codex.go` (all three implement the shared `HookAgent` interface in `hookagent.go`, driven generically by `connect.go`), and `opencode.go` (its own path -- OpenCode has no hooks.json to merge into, its plugin file is the registration).
- `internal/commands/`: the actual `install`/`connect`/`export`/`status`/`agents`/`config-alerts`/`cursor-hook` subcommands.
- `internal/cursorhook/`: none of Cursor/Copilot/Codex/OpenCode have native OTel export, so this is a from-scratch hook-processing pipeline all four share: parses hook JSON on stdin (normalizing each tool's own event-name vocabulary -- camelCase for Cursor/Copilot, PascalCase for Codex/OpenCode -- onto one canonical set), builds an OTel span with GenAI/LangSmith-convention attributes, links it to its parent span across process invocations (each hook event is a separate process) via a small file-based context store, and exports it via gRPC or HTTP/protobuf. Despite the package name (Cursor was first), it's agent-agnostic -- the span's `service.name` (and therefore its name prefix, e.g. `copilot.postToolUse`) comes from whichever agent's own config file is passed in. See [docs/cursor.md](../docs/cursor.md) and [docs/other-agents.md](../docs/other-agents.md) for the full attribute reference.

## Testing

```bash
go test ./...
go vet ./...
```

See the repo root [docs/setup.md](../docs/setup.md) for the manual (non-CLI) path, and [docs/architecture.md](../docs/architecture.md) for the overall system design.
