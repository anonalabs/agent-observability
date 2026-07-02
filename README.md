# agent-observability

**Datadog for AI coding agents.** Open-source, self-hosted telemetry for Claude Code, Gemini CLI, and Cursor — token usage, cost, tool activity, sessions, and (for the first time across tools that were never designed to be compared) one dashboard that ranks them against each other.

Every agent, every session, every dollar spent — in your own Grafana, on your own infra, in about two minutes.

## Why this exists

Claude Code and Gemini CLI ship native OpenTelemetry. Cursor doesn't. Every other project in this space picks one vendor and stops there. This one doesn't: it's a vendor-neutral pipeline (OTel Collector → Prometheus + ClickHouse → Grafana) with a bundled hook shim for agents that need one, so you get one pane of glass regardless of which tool your team actually uses — including the ones that don't play nice with OTel out of the box.

## Features

- **`agentobs`** — a single Go binary. Install the stack, wire up an agent, export data, check health. No Python venvs, no Node, no separate services to babysit.
- **Three agents supported out of the box**: Claude Code and Gemini CLI (native OTel, just config), Cursor (via a from-scratch Go reimplementation of its hook system — no reliance on an external package).
- **Agent Leaderboard dashboard** — the actual innovation here: a real cross-agent comparison view, built on `UNION` queries across ClickHouse's logs and traces tables, normalized on `ServiceName`. Nobody else shows you Claude Code vs. Cursor vs. Gemini CLI side by side, because nobody else treats "which agent" as a first-class dimension.
- **Config merges, never overwrites.** `connect` always backs up (`.bak`) before touching `hooks.json`/`settings.json`/shell rc files, and merges rather than replaces — safe to run alongside other tools that already registered hooks.
- **Privacy-first**: prompt/tool-detail logging is off by default across every agent, toggled explicitly per `connect` run.
- 5 pre-built Grafana dashboards: Agent Leaderboard, Token & Cost Usage, Session & Tool Explorer, Events Detail, Cursor Traces.

## Quickstart

```bash
cd cli && go build -o agentobs ./cmd/agentobs && cd ..
./cli/agentobs install                    # brings up collector + prometheus + clickhouse + grafana
./cli/agentobs connect --agent claude-code # or --agent cursor / --agent gemini-cli
```

(Or `go install ./cli/cmd/agentobs` to put `agentobs` on your `PATH`.)

Then:
- **Claude Code**: source the printed `export` lines (or let `connect` append them to your shell rc), then use `claude` as normal.
- **Gemini CLI**: nothing else to do — `connect` already merged `~/.gemini/settings.json`.
- **Cursor**: restart the IDE to pick up the new hooks.

Open **http://localhost:3000** and watch the dashboards fill in as you work.

## Architecture

```
Claude Code ─┐
Gemini CLI  ─┼─▶ OpenTelemetry Collector ─▶ Prometheus (metrics)
Cursor      ─┘                            ─▶ ClickHouse (logs + traces)
                                                    │
                                                    ▼
                                                 Grafana
```

Full design rationale, including how to add another agent, in [docs/architecture.md](docs/architecture.md).

## Docs

| | |
|---|---|
| [docs/setup.md](docs/setup.md) | Manual (non-CLI) setup path |
| [docs/metrics.md](docs/metrics.md) | Full Claude Code metric/event reference |
| [docs/gemini-cli.md](docs/gemini-cli.md) | Gemini CLI telemetry reference |
| [docs/cursor.md](docs/cursor.md) | Cursor hook shim reference |
| [docs/architecture.md](docs/architecture.md) | System design + cloud export options |
| [cli/README.md](cli/README.md) | `agentobs` CLI internals |

## Contributing

Issues and PRs welcome. The most valuable contributions right now:
- Support for another agent (Codex, Continue, Aider, ...) — see `cli/internal/agents/` for the pattern (native OTel = a config builder; no native OTel = a hook shim like `cli/internal/cursorhook/`).
- More Agent Leaderboard panels as more agents get connected in the wild.
- Cloud exporter wiring (AWS/GCP/Azure) — mapped out but not built, see [docs/architecture.md](docs/architecture.md#cloud-export-options-reference-not-wired-up).

## License

MIT — see [LICENSE](LICENSE).
