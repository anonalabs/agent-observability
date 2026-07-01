# Setup

## 1. Start the stack

```bash
docker compose up -d
```

This brings up:
- `otel-collector` — OTLP receiver on `4317` (gRPC) / `4318` (HTTP)
- `prometheus` — metrics storage, `9090`
- `clickhouse` — log/event storage, `8123` (HTTP) / `9000` (native)
- `grafana` — dashboards, `3000`

## 2. Point Claude Code at the collector

Claude Code emits telemetry natively via OpenTelemetry. Set these env vars wherever you run Claude Code:

```bash
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_METRICS_EXPORTER=otlp
export OTEL_LOGS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
# Required for Prometheus-style backends -- without this, delta metrics are silently dropped.
export OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=cumulative

# Optional, privacy-sensitive, off by default:
# export OTEL_LOG_USER_PROMPTS=1     # captures full prompt text
# export OTEL_LOG_TOOL_DETAILS=1     # captures bash commands and file paths
```

Or just run `agentobs connect`, which sets all of this interactively (or via flags/YAML — see [cli/README.md](../cli/README.md)).

Then use Claude Code as normal. Metrics and log events stream to the collector in the background.

## 3. View data

- Grafana: http://localhost:3000 (anonymous access enabled, Admin role) — dashboards under "AI Agent Telemetry"
- Prometheus (raw metrics): http://localhost:9090
- ClickHouse (raw events): `docker compose exec clickhouse clickhouse-client --database otel`

## Notes

- Exact metric/log field names should be checked against your installed Claude Code version's telemetry docs — the collector config and dashboards assume the standard `claude_code.*` metric namespace and OTLP log schema; adjust `collector/otel-collector-config.yaml` or the dashboard JSON if your version differs.
- Data is retained per the ClickHouse `ttl` set in `collector/otel-collector-config.yaml` (default 90 days) and Prometheus's default retention.
