# agentobs

Go CLI for installing, connecting, and exporting AI coding agent telemetry.

```bash
go build -o agentobs ./cmd/agentobs
# or: go install ./cmd/agentobs

./agentobs install    # bring up the collector/prometheus/clickhouse/grafana stack
./agentobs connect    # wire up an agent's telemetry (Claude Code, Gemini CLI, or Cursor)
./agentobs export     # pull metrics/logs out to a file
./agentobs status     # health check the stack
./agentobs config-alerts   # set alert thresholds + Slack/Discord webhook, no YAML editing
```

## Layout

- `cmd/agentobs/`: entry point, wires up cobra subcommands.
- `internal/config/`: YAML/flag/prompt precedence resolution, shared by every command.
- `internal/compose/`: `docker compose` wrapper.
- `internal/agents/`: declarative agent specs (`builtin.yaml` + `registry.go`) for env-var and JSON-merge style agents (Claude Code, Gemini CLI, and anything added via `~/.config/agentobs/agents.yaml`), plus Cursor's dedicated hook-based path (`cursor.go`).
- `internal/commands/`: the actual `install`/`connect`/`export`/`status`/`agents`/`config-alerts`/`cursor-hook` subcommands.
- `internal/cursorhook/`: Cursor has no native OTel export, so this is a from-scratch reimplementation of the hook shim Cursor's `hooks.json` execs on every agent event: parses the hook JSON on stdin, builds an OTel span with GenAI/LangSmith-convention attributes, links it to its parent span across process invocations (each hook event is a separate process) via a small file-based context store, and exports it via gRPC or HTTP/protobuf. See [docs/cursor.md](../docs/cursor.md) for the full attribute reference.

## Testing

```bash
go test ./...
go vet ./...
```

See the repo root [docs/setup.md](../docs/setup.md) for the manual (non-CLI) path, and [docs/architecture.md](../docs/architecture.md) for the overall system design.
