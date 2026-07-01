from __future__ import annotations

import httpx
from rich.console import Console
from rich.table import Table

console = Console()

CHECKS = {
    "otel-collector": "http://localhost:4318/v1/logs",  # POST-only endpoint; any response = up
    "prometheus": "http://localhost:9090/-/healthy",
    "clickhouse": "http://localhost:8123/ping",
    "grafana": "http://localhost:3000/api/health",
}


def _is_up(url: str) -> bool:
    try:
        resp = httpx.get(url, timeout=3)
        return resp.status_code < 500
    except httpx.HTTPError:
        return False


def status() -> None:
    """Health-check the collector/prometheus/clickhouse/grafana stack."""
    table = Table()
    table.add_column("Service")
    table.add_column("Status")

    for name, url in CHECKS.items():
        up = _is_up(url)
        table.add_row(name, "[green]up[/green]" if up else "[red]down[/red]")

    console.print(table)
