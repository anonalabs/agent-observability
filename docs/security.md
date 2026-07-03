# Security

## Default mode (no auth)

Out of the box, `agentobs install` brings up:
- Grafana with anonymous Admin access enabled.
- The collector's OTLP ports (4317/4318) accepting any request, no auth.

This is fine for a single laptop where `localhost:3000`/`localhost:4317` aren't reachable by anyone else. It is **not** fine once the stack is reachable beyond localhost: Docker publishes ports on `0.0.0.0` by default, so on a shared network anyone can reach Grafana as Admin, or push fake telemetry to the collector.

## Secure mode (opt-in)

```bash
export AGENTOBS_AUTH_TOKEN=$(openssl rand -hex 32)
export GRAFANA_ADMIN_PASSWORD=$(openssl rand -hex 16)
agentobs install --secure
agentobs connect --agent cursor --auth-token "$AGENTOBS_AUTH_TOKEN"
```

What changes:
- Grafana: anonymous access disabled, requires logging in as `admin` with `GRAFANA_ADMIN_PASSWORD`.
- Collector: OTLP ingest (both gRPC and HTTP) requires `Authorization: Bearer $AGENTOBS_AUTH_TOKEN` on every request; anything else is rejected.

Both env vars are required with no default -- `docker compose` fails to start rather than silently running unsecured if you forget one.

### Wiring an agent up in secure mode

`agentobs connect --auth-token "$AGENTOBS_AUTH_TOKEN"` adds the bearer token to whichever config it writes:
- **Claude Code / generic env-kind agents**: adds `OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer ..."` to the exported env vars.
- **Cursor**: adds the same to `otel_config.json`'s `OTEL_EXPORTER_OTLP_HEADERS`.
- **Gemini CLI / other json-merge agents**: printed as a hint, not written into `settings.json` -- not all such tools read that env var for auth, so this isn't assumed silently.

### Important: existing Grafana data volume

`GF_SECURITY_ADMIN_PASSWORD` only takes effect the **first time** Grafana's data volume is initialized. If you already ran the stack in default (non-secure) mode, its `grafana-data` volume already has an admin account with the default password -- switching to `--secure` later does **not** retroactively change it. Either:

```bash
docker compose exec grafana grafana-cli admin reset-admin-password "$GRAFANA_ADMIN_PASSWORD"
```

or start from a clean volume (`docker compose down -v`, destroys all stored data, dashboards are re-provisioned from files so those come back automatically).

## What secure mode does not cover

- TLS: OTLP and Grafana traffic is still plaintext locally; put a reverse proxy in front for real network exposure (see `docs/architecture.md`'s org-rollout notes on VPN/PrivateLink/ALB).
- ClickHouse and Prometheus have no auth of their own in either mode -- they're only reachable from the collector/Grafana containers' network today if you don't publish their ports; if you do publish them (default `docker-compose.yml` does, for local debugging), treat that the same as the collector's ports.
- Token/password rotation is manual (no built-in rotation mechanism).
