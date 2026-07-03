# agent-observability

**Datadog for AI coding agents.** Open-source, self-hosted telemetry for Claude Code, Gemini CLI, and Cursor: token usage, cost, tool activity, sessions, and (for the first time across tools that were never designed to be compared) one dashboard that ranks them against each other.

Every agent, every session, every dollar spent, in your own Grafana, on your own infra, in about two minutes.

## Why this exists

Claude Code and Gemini CLI ship native OpenTelemetry. Cursor doesn't. Every other project in this space picks one vendor and stops there. This one doesn't: it's a vendor-neutral pipeline (OTel Collector → Prometheus + ClickHouse → Grafana) with a bundled hook shim for agents that need one, so you get one pane of glass regardless of which tool your team actually uses, including the ones that don't play nice with OTel out of the box.

## Features

- **`agentobs`**: a single Go binary, installable via one `curl | sh` (prebuilt releases) or `go build` from source. Install the stack, wire up an agent, export data, check health. No Python venvs, no Node, no separate services to babysit.
- **Any OTel-emitting tool works via config, not code.** Claude Code and Gemini CLI ship as declarative specs (`cli/internal/agents/builtin.yaml`); add your own tool the same way in `~/.config/agentobs/agents.yaml` -- `agentobs agents list` shows everything registered.
- **Cursor supported despite having no native OTel**: a from-scratch Go reimplementation of its hook system, no reliance on an external package.
- **Agent Leaderboard + Session Timeline dashboards**: the actual innovation, real cross-agent views built on `UNION` queries across ClickHouse's logs and traces tables, normalized on `ServiceName`/session id. Pick one session, see its full timeline regardless of which agent ran it. Nobody else treats "which agent" as a first-class dimension.
- **Config merges, never overwrites.** `connect` always backs up (`.bak`) before touching `hooks.json`/`settings.json`/shell rc files, and merges rather than replaces, safe to run alongside other tools that already registered hooks.
- **Privacy-first**: prompt/tool-detail logging is off by default across every agent, toggled explicitly per `connect` run.
- 6 pre-built Grafana dashboards: Agent Leaderboard, Session Timeline, Token & Cost Usage, Session & Tool Explorer, Events Detail, Cursor Traces.

## Quickstart

```bash
curl -fsSL https://raw.githubusercontent.com/anonalabs/agent-observability/main/install.sh | sh

agentobs install                    # brings up collector + prometheus + clickhouse + grafana
agentobs connect --agent claude-code # or --agent cursor / --agent gemini-cli
```

No Go toolchain needed -- that installs a prebuilt binary. Building from source instead:

```bash
cd cli && go build -o agentobs ./cmd/agentobs && cd ..
./cli/agentobs install
```

Then:
- **Claude Code**: source the printed `export` lines (or let `connect` append them to your shell rc), then use `claude` as normal.
- **Gemini CLI**: nothing else to do, `connect` already merged `~/.gemini/settings.json`.
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
| [docs/security.md](docs/security.md) | Opt-in auth hardening (`agentobs install --secure`) |
| [cli/README.md](cli/README.md) | `agentobs` CLI internals |

## Contributing

Issues and PRs welcome. The most valuable contributions right now:
- Support for another agent: if it already speaks OTel, it's a `~/.config/agentobs/agents.yaml` entry, zero code (`agentobs agents list` shows what's registered). If it doesn't, it needs a hook shim like `cli/internal/cursorhook/`.
- More Agent Leaderboard panels as more agents get connected in the wild.
- Cloud exporter wiring (AWS/GCP/Azure): mapped out but not built, see [docs/architecture.md](docs/architecture.md#cloud-export-options-reference-not-wired-up).

## License

MIT, see [LICENSE](LICENSE).
