# Alerting

## One command

```bash
agentobs config-alerts
```

Prompts (or takes flags, for non-interactive/CI use) for everything -- no editing YAML by hand, no hardcoded thresholds:

```bash
agentobs config-alerts --non-interactive \
  --webhook-url "https://hooks.slack.com/services/..." \
  --contact-type slack \
  --cost-threshold 10 \
  --rate-limit-threshold 2 \
  --tool-failure-threshold 5
```

| Flag | Meaning | Default |
|---|---|---|
| `--webhook-url` | Slack incoming webhook, Discord webhook, PagerDuty, or any URL accepting a POST | (required) |
| `--contact-type` | `slack` (also works for Discord's Slack-compatible webhooks) or `webhook` (generic JSON) | `slack` |
| `--cost-threshold` | USD/hour that triggers the Claude Code cost-spike alert | `5` |
| `--rate-limit-threshold` | 429 errors in 5 minutes that trigger the rate-limit alert | `0` (any hit) |
| `--tool-failure-threshold` | failed tool calls in 15 minutes that trigger the tool-failure alert | `3` |
| `--restart` / `--no-restart` | restart Grafana automatically to apply (default: restart) | `true` |

This generates `grafana/provisioning/alerting/rules.yaml` and `contactpoints.yaml` from scratch every run (both are plain Go structs marshaled to YAML, not string templates with baked-in numbers) and restarts Grafana. `contactpoints.yaml` is gitignored since it holds your webhook URL -- re-run `config-alerts` any time to change thresholds or rotate the webhook, nothing to hand-edit.

## What gets alerted on

All three rules are currently Claude-Code-specific, since that's the agent with cost/rate-limit/tool-result data in the shape these checks expect:

| Rule | Checks |
|---|---|
| Claude Code cost spike | `increase(claude_code_cost_usage_USD_total[1h])` against your cost threshold |
| Claude Code hitting API rate limits (429s) | count of `api_error` events with `status_code=429` in the last 5 minutes |
| Claude Code tool call failures | count of `tool_result` events with `success=false` in the last 15 minutes |

## Verifying it worked

```bash
curl -s http://localhost:3000/api/v1/provisioning/contact-points
curl -s http://localhost:3000/api/v1/provisioning/alert-rules
```

To send a real test notification through your webhook right now:

```bash
curl -s -X POST "http://localhost:3000/api/alertmanager/grafana/config/api/v1/receivers/test" \
  -H "Content-Type: application/json" \
  -d '{
    "receivers": [{
      "name": "agentobs-webhook",
      "grafana_managed_receiver_configs": [{
        "uid": "agentobs-webhook-1",
        "name": "agentobs-webhook",
        "type": "slack",
        "settings": {"url": "YOUR_WEBHOOK_URL"}
      }]
    }]
  }'
```

A `"status":"ok"` in the response means it was actually delivered -- check your Slack/Discord channel.

## Troubleshooting

If you see `"Failed to build rule evaluator" ... data source not found` in `docker compose logs grafana`, the datasource UIDs in `grafana/provisioning/datasources/datasources.yml` (pinned to `Prometheus` / `ClickHouse`) don't match what's actually provisioned -- restart Grafana once to let it re-apply.

If Grafana won't start after editing `contactpoints.yaml` by hand, check its permissions aren't too restrictive: Grafana runs as a different user inside its container than whatever wrote the file on the host, so anything tighter than `0644` (e.g. `0600`) causes a permission-denied crash on startup. `agentobs config-alerts` always writes `0644` for exactly this reason -- the file's real protection is being gitignored, not filesystem permissions.
