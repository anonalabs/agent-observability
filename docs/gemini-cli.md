# Gemini CLI

Like Claude Code, Gemini CLI has native OTel export (metrics + logs over OTLP/gRPC) -- no shim needed. Unlike Claude Code, most config lives in `.gemini/settings.json` rather than shell env vars; only the endpoint can be overridden via `OTEL_EXPORTER_OTLP_ENDPOINT`.

## Setup

```bash
agentobs connect --agent gemini-cli
```

This merges (never replaces) `~/.gemini/settings.json`:

```json
{
  "telemetry": {
    "enabled": true,
    "target": "local",
    "logPrompts": false
  }
}
```

`target: "local"` sends OTLP to `http://localhost:4317` by default, which matches this stack's collector -- no extra endpoint config needed unless you moved the collector elsewhere, in which case `agentobs connect` also prints the `OTEL_EXPORTER_OTLP_ENDPOINT` export to use.

## Metrics (Prometheus)

| Gemini CLI metric | Attributes |
|---|---|
| `gemini_cli.session.count` | n/a |
| `gemini_cli.tool.call.count` | `function_name`, `success`, `decision` |
| `gemini_cli.tool.call.latency` | `function_name`, `decision` |
| `gemini_cli.api.request.count` | `model`, `status_code`, `error_type` |
| `gemini_cli.api.request.latency` | `model` |
| `gemini_cli.token.usage` | `model`, `type` (input/output/thought/cache/tool) |
| `gemini_cli.file.operation.count` | `operation` (create/read/update), `lines`, `mimetype`, `extension` |

Prometheus renames dots to underscores as usual (verify actual names with `curl localhost:9090/api/v1/label/__name__/values` once some activity has happened).

## Events (ClickHouse, `otel_logs.LogAttributes['event.name']`)

| Event | Key attributes |
|---|---|
| `gemini_cli.config` | model, sandbox_enabled, approval_mode, mcp_servers, ... (once per startup) |
| `gemini_cli.user_prompt` | `prompt_length`, `prompt` (only if `logPrompts: true`) |
| `gemini_cli.tool_call` | `function_name`, `function_args`, `duration_ms`, `success`, `decision`, `error` |
| `gemini_cli.api_request` | `model`, `request_text` |
| `gemini_cli.api_response` | `model`, `status_code`, `duration_ms`, `input_token_count`, `output_token_count`, `cached_content_token_count` |
| `gemini_cli.api_error` | `model`, `error`, `error_type`, `status_code`, `duration_ms` |

All events/metrics carry `sessionId` as a common attribute, same role as Claude Code's `session.id`.

## Dashboards

Same Token & Cost Usage / Events Detail dashboards work if you add `gemini_cli_*` panels alongside the `claude_code_*` ones -- not pre-built here since panel queries would need to distinguish agent by metric prefix. A per-agent dashboard variable is the natural next step once a second agent is actually in daily use.
