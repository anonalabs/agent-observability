# Alerting

Three alert rules are provisioned by default (`grafana/provisioning/alerting/rules.yaml`), all currently Claude-Code-specific since that's the agent with cost/rate-limit/tool-result data in the shape these rules expect:

| Rule | Condition | Window |
|---|---|---|
| Claude Code cost spike | `increase(claude_code_cost_usage_USD_total[1h]) > 5` | rolling 1h, checked every 1m |
| Claude Code hitting API rate limits (429s) | any `api_error` event with `status_code=429` | rolling 5m |
| Claude Code tool call failures | more than 3 `tool_result` events with `success=false` | rolling 15m |

Edit the thresholds directly in `rules.yaml` (the `params` list under each rule's `C` condition) and restart Grafana (`docker compose restart grafana`) to apply changes.

## Enabling delivery (Slack, Discord, etc.)

Rules are provisioned but have nowhere to send notifications until a contact point exists:

```bash
cd grafana/provisioning/alerting
cp contactpoints.yaml.example contactpoints.yaml
```

Edit `contactpoints.yaml` and replace the placeholder `url` with a real webhook:
- **Slack**: create an [incoming webhook](https://api.slack.com/messaging/webhooks), use that URL.
- **Discord**: channel settings -> Integrations -> Webhooks, use the webhook URL (append `/slack` to the URL for Slack-compatible payload formatting, which Grafana's generic webhook sender is compatible with).
- **PagerDuty / anything else**: any URL that accepts a POST with a JSON body works with the generic `webhook` receiver type.

Then:

```bash
docker compose restart grafana
```

`contactpoints.yaml` is gitignored since it holds a real webhook URL -- don't commit it.

## Verifying rules loaded correctly

```bash
docker compose logs grafana | grep -i "rule_uid=agentobs"
```

Should show evaluation attempts with no `"Failed to build rule evaluator"` errors. If you see `data source not found`, the datasource UIDs in `grafana/provisioning/datasources/datasources.yml` (pinned to `Prometheus` / `ClickHouse`) don't match what's actually provisioned -- restart Grafana once to let it re-apply, or check `curl localhost:3000/api/datasources` for the actual UIDs in use.
