# agent-observability

Open-source telemetry platform for AI coding agents — token/cost usage, tool usage, session activity, and more. Supports Claude Code and Gemini CLI (native OTel) and Cursor (via a hook-based OTel shim).

## Quickstart

```bash
pip install -e ./cli
agentobs install    # brings up the collector/prometheus/clickhouse/grafana stack
agentobs connect    # wires up your agent's telemetry (interactive, or --agent/--endpoint flags)
```

Then open Grafana at http://localhost:3000. See [cli/README.md](cli/README.md) for all `agentobs` commands, or [docs/setup.md](docs/setup.md) for the manual (non-CLI) path.

`agentobs connect --agent <claude-code|gemini-cli|cursor>` wires up each agent's telemetry; see [docs/metrics.md](docs/metrics.md), [docs/gemini-cli.md](docs/gemini-cli.md), and [docs/cursor.md](docs/cursor.md) for what each one captures.

## Architecture

Agent → OpenTelemetry Collector → Prometheus (metrics) + ClickHouse (events/traces) → Grafana.

See [docs/architecture.md](docs/architecture.md) for the full design and rationale, including how further agents (Codex, Continue, Aider, etc.) plug in later.

## License

MIT — see [LICENSE](LICENSE).
