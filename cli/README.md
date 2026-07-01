# agentobs

CLI for installing, connecting, and exporting AI coding agent telemetry.

```bash
pip install -e .
agentobs install    # bring up the collector/prometheus/clickhouse/grafana stack
agentobs connect    # wire up an agent's telemetry env vars (Claude Code first)
agentobs export     # pull metrics/logs out to a file
agentobs status     # health check the stack
```

See the repo root [docs/setup.md](../docs/setup.md) for the manual (non-CLI) path.
