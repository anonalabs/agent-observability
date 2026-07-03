# Alerting

## One command

```bash
agentobs config-alerts
```

Prompts first for which agents to configure (Claude Code, Cursor, or both), then only the thresholds relevant to what you picked -- no editing YAML by hand, no hardcoded thresholds:

```bash
agentobs config-alerts --non-interactive \
  --agents claude-code,cursor \
  --webhook-url "https://hooks.slack.com/services/..." \
  --contact-type slack \
  --cost-threshold 10 \
  --rate-limit-threshold 2 \
  --claude-tool-failure-threshold 5 \
  --cursor-tool-failure-threshold 5
```

| Flag | Meaning | Default |
|---|---|---|
| `--agents` | comma-separated agents to configure: `claude-code`, `cursor`, or both | both |
| `--webhook-url` | Slack incoming webhook, Discord webhook, PagerDuty, or any URL accepting a POST -- shared by every agent's alerts | (required) |
| `--contact-type` | `slack` (also works for Discord's Slack-compatible webhooks) or `webhook` (generic JSON) | `slack` |
| `--cost-threshold` | Claude Code: USD/hour that triggers the cost-spike alert | `5` |
| `--rate-limit-threshold` | Claude Code: 429 errors in 5 minutes that trigger the rate-limit alert | `0` (any hit) |
| `--claude-tool-failure-threshold` | Claude Code: failed tool calls in 15 minutes that trigger its tool-failure alert | `3` |
| `--cursor-tool-failure-threshold` | Cursor: failed tool calls in 15 minutes that trigger its own, separately-thresholded tool-failure alert | `3` |
| `--restart` / `--no-restart` | restart Grafana automatically to apply (default: restart) | `true` |
| `--critical-webhook-url` | separate webhook for critical-severity alerts (currently just the rate-limit rule) -- e.g. a paging channel instead of a general one | (same as `--webhook-url`) |

This generates `grafana/provisioning/alerting/rules.yaml` and `contactpoints.yaml` from scratch every run (both are plain Go structs marshaled to YAML, not string templates with baked-in numbers) and restarts Grafana. `contactpoints.yaml` is gitignored since it holds your webhook URL -- re-run `config-alerts` any time to change thresholds, agents, or rotate the webhook, nothing to hand-edit. Re-running with a narrower `--agents` list removes the rules for whichever agent you dropped (the rule file also carries an explicit `deleteRules` block for exactly this, since Grafana's file provisioner otherwise leaves stale rules behind).

The webhook is shared across every agent: one `config-alerts` run configures the notification channel for the whole stack, not per-agent -- only the thresholds and which rules exist are agent-specific.

## What gets alerted on

| Rule | Checks | Severity | Scope |
|---|---|---|---|
| Claude Code cost spike | `increase(claude_code_cost_usage_USD_total[1h])` against your cost threshold | warning | Claude Code only (only agent with cost data) |
| Claude Code hitting API rate limits (429s) | count of `api_error` events with `status_code=429` in the last 5 minutes | critical | Claude Code only |
| Claude Code tool call failures | count of `tool_result` events with `success=false` in the last 15 minutes, own threshold | warning | Claude Code only |
| Cursor tool call failures | count of `otel_traces` spans with `StatusCode='STATUS_CODE_ERROR'` and `gen_ai.operation.name='tool'` in the last 15 minutes, own threshold | warning | Cursor only |

Claude Code and Cursor tool-failure rules are separate so each can have its own threshold instead of one shared cross-agent count -- Cursor's raw failure rate can be tuned independently of Claude Code's.

Notifications are grouped by `alertname`, held for 30s to batch near-simultaneous firings, and re-sent at most every 4 hours while still firing -- so a sustained problem doesn't re-page you every evaluation cycle. `noDataState` is `OK` on every rule: no data (e.g. idle machine, no activity in the evaluation window) is not an error condition and won't fire an alert -- only an actual breach of the threshold does.

Slack/Discord notifications use a custom `title`/`text` template (not Grafana's default, which dumps every label and raw query values like `A=4, C=1`) -- each notification is just the rule name, status, the human-readable summary (with the real count and threshold filled in), and a link back to Grafana.

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
