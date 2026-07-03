# Full metric & event reference (Claude Code)

Claude Code emits all of this automatically once telemetry is on (`agentobs connect`, or the raw env vars in [setup.md](setup.md)), nothing below requires extra configuration beyond enabling both exporters. Source: Claude Code's own OpenTelemetry docs.

## Metrics (Prometheus)

Names below are as they land in Prometheus after the collector's exporter (dots → underscores, `_total` suffix, unit sometimes embedded, verify actual names anytime with `curl localhost:9090/api/v1/label/__name__/values`).

| Claude Code metric | Prometheus name (verified) | Key attributes |
|---|---|---|
| `claude_code.session.count` | `claude_code_session_count_total` | `session.id`, `organization.id`, `user.account_uuid` |
| `claude_code.token.usage` | `claude_code_token_usage_tokens_total` | `type` (input/output/cacheRead/cacheCreation), `model`, `session_id` |
| `claude_code.cost.usage` | `claude_code_cost_usage_USD_total` | `model`, `session_id` |
| `claude_code.lines_of_code.count` | `claude_code_lines_of_code_count_total` | `type` (added/removed) |
| `claude_code.commit.count` | `claude_code_commit_count_total` | n/a |
| `claude_code.pull_request.count` | `claude_code_pull_request_count_total` | n/a |
| `claude_code.code_edit_tool.decision` | `claude_code_code_edit_tool_decision_total` | `tool` (Edit/MultiEdit/Write/NotebookEdit), `decision` (accept/reject) |
| (undocumented, observed) | `claude_code_active_time_seconds_total` | n/a |

All covered on the **Token & Cost Usage** Grafana dashboard.

## Events (ClickHouse, via `otel_logs.LogAttributes`)

Filter with `LogAttributes['event.name'] = '<name>'`. Observed in this stack (some undocumented, added by newer Claude Code versions):

| Event | Key attributes | Notes |
|---|---|---|
| `user_prompt` | `prompt_length`, `prompt` (redacted unless `OTEL_LOG_USER_PROMPTS=1`) | one per submitted prompt |
| `tool_result` | `name`, `success`, `duration_ms`, `error` | one per tool execution |
| `tool_decision` | `tool_name`, `decision`, `source` (config/user_permanent/user_temporary/user_abort/user_reject) | permission prompts |
| `api_request` | `model`, `cost_usd`, `duration_ms`, `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_creation_tokens` | one per API call |
| `api_error` | `model`, `error`, `status_code`, `attempt` | includes 429 rate-limit hits |
| `assistant_response` | (observed, undocumented) | |
| `hook_execution_start` / `hook_execution_complete` | (observed, undocumented) | hooks config |
| `plugin_loaded` / `hook_registered` | (observed, undocumented) | plugin system |

All standard attributes (`session.id`, `user.account_id`/`user.id`, `organization.id`, `prompt.id`) are present on every event and let you join across event types for a single prompt/session. Covered on the **Events Detail (Tool & API)** and **Session & Tool Explorer** dashboards.

## Cardinality / interval knobs (optional, not required to see data)

These only affect volume/freshness, not what's captured. Set them directly as env vars alongside what `agentobs connect` gives you if needed:

| Env var | Default | Effect |
|---|---|---|
| `OTEL_METRIC_EXPORT_INTERVAL` | 60000 (ms) | lower for faster dashboard updates during debugging |
| `OTEL_LOGS_EXPORT_INTERVAL` | 5000 (ms) | same, for events |
| `OTEL_METRICS_INCLUDE_SESSION_ID` | true | drop to reduce metric cardinality |
| `OTEL_METRICS_INCLUDE_VERSION` | false | set true to segment by Claude Code version |
| `OTEL_METRICS_INCLUDE_ACCOUNT_UUID` | true | drop for anonymized metrics |
