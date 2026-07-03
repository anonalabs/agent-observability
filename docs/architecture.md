# Architecture

```
Claude Code (native OTLP exporter)
        |  OTLP/gRPC or HTTP
        v
OpenTelemetry Collector
        |  batched
        v
   +---------+
   |         |
Prometheus  ClickHouse
(metrics)   (logs/events)
   |         |
   +----+----+
        v
     Grafana
```

## Why this split

Claude Code's telemetry is two streams:

- **Metrics** (OTLP metrics): numeric, low-cardinality time series: token counts, cost, session counts, lines of code, active time. These aggregate well and belong in **Prometheus**, which the collector's Prometheus exporter serves and Prometheus scrapes.
- **Logs** (OTLP logs): structured, high-cardinality events: individual prompts, tool calls, API requests/errors. These need to be queried and filtered per-event, not just aggregated, so they land in **ClickHouse** via the collector's ClickHouse exporter.

Grafana reads both: Prometheus for trend/aggregate dashboards, ClickHouse for event-level exploration tables.

## Vendor-neutral design

Nothing in the Collector, Prometheus, or ClickHouse config is Claude-specific: they just consume standard OTLP. Adding another agent (Gemini CLI, Codex, Cursor, etc.) later means either:

1. The agent has native OTLP export: point it at the same collector endpoint (`4317`/`4318`), or
2. It doesn't: write a small shim/exporter that emits OTLP with the same conceptual fields (agent name, model, session id, tokens, cost, tool name, repo, timestamp).

No changes to the collector, storage, or dashboard architecture are needed to onboard a new agent, only new dashboards/panels if you want agent-specific views, and a shared "agent" label/attribute to distinguish sources in shared dashboards.

## Deferred (not in this MVP)

- Auth / multi-tenancy on ingestion
- Hosted/cloud deployment (this repo targets Docker Compose self-hosting)
- Alerting rules
- A gateway/API layer in front of the collector
- Lines-of-code correlation beyond what Claude Code's own metric reports (would require git diff correlation, which Claude Code's native telemetry doesn't do)

## Org rollout roadmap (not in this MVP, but same collector/schema)

For a team bigger than one laptop, the same OTLP-in pipeline extends without redesign:

- **Fleet-wide config**: distribute the `agentobs connect` env vars via Claude Code's managed settings JSON (pushed by MDM) instead of each dev running the CLI locally, so users can't opt out or misconfigure the endpoint.
- **Endpoint security**: once the collector isn't `localhost`, put it behind a private network (VPN/PrivateLink) or an ALB requiring a Bearer token / mTLS. Never expose OTLP ingest on the open internet.
- **Scale-out storage**: swap self-hosted Prometheus/ClickHouse for Amazon Managed Prometheus and Kinesis Firehose → S3 → Athena (add OpenSearch only if full-text prompt search is needed); same collector config, different exporters.
- **Cheapest small-team variant**: for ~2-3 devs, skip Managed Prometheus/Grafana entirely and point the collector at CloudWatch (metrics + Logs Insights), one fewer managed service, same collector.
- **Alerting**: wire Grafana/CloudWatch alarms → SNS/Slack on token, cost, or `api_error` 429-rate thresholds.
- **Privacy controls**: `agentobs connect`'s `--log-user-prompts`/`--log-tool-details` prompts already default these off; at fleet scale, redact at the collector (a processor stage) rather than trusting per-dev config if prompt/tool content shouldn't reach storage at all.

## Cloud export options (reference, not wired up)

If/when telemetry needs to leave a single laptop's Docker Compose stack, the same collector just gets an additional (or replacement) exporter -- no app-level changes, since the agents only ever talk OTLP to the collector.

| Cloud | Metrics | Logs | Traces | Collector exporter |
|---|---|---|---|---|
| AWS | Amazon Managed Prometheus | CloudWatch Logs or Firehose → S3 → Athena | X-Ray or Managed Prometheus/Tempo | `awsemf` / `awsxray` / `awsprometheusremotewrite` (separate exporters per signal) |
| GCP | Cloud Monitoring | Cloud Logging | Cloud Trace | `googlecloud` (one exporter covers all three signals) |
| Azure | Azure Monitor Metrics | Log Analytics | Azure Monitor / App Insights | `azuremonitor` (one exporter covers all three signals) |

GCP/Azure are less config (one exporter each); AWS needs 2-3 separate exporters wired to different services. Local Prometheus/ClickHouse/Grafana doesn't have to be replaced -- collector pipelines support multiple exporters per signal, so local + cloud simultaneously is one config change, not a redesign. Main tradeoff: cloud-native costs per ingested point but gets managed retention/scaling/alerting for free.
