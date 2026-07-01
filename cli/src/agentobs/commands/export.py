from __future__ import annotations

import csv
import json
import time
from pathlib import Path
from typing import Optional

import httpx
import typer
from rich.console import Console

console = Console()

PROMETHEUS_URL = "http://localhost:9090"
CLICKHOUSE_HOST = "localhost"
CLICKHOUSE_PORT = 8123

_DURATION_UNITS = {"s": 1, "m": 60, "h": 3600, "d": 86400}


def _parse_since(since: str) -> int:
    unit = since[-1]
    if unit not in _DURATION_UNITS:
        raise typer.BadParameter(f"unrecognized duration '{since}', expected e.g. 24h, 30m, 1d")
    return int(since[:-1]) * _DURATION_UNITS[unit]


def _fetch_metrics(seconds: int) -> list:
    now = time.time()
    resp = httpx.get(
        f"{PROMETHEUS_URL}/api/v1/query_range",
        params={
            "query": 'sum by (type) (rate(claude_code_token_usage_total[5m]))',
            "start": now - seconds,
            "end": now,
            "step": "60s",
        },
        timeout=30,
    )
    resp.raise_for_status()
    return resp.json().get("data", {}).get("result", [])


def _fetch_logs(seconds: int) -> list:
    query = (
        "SELECT Timestamp, ServiceName, Body FROM otel.otel_logs "
        f"WHERE Timestamp > now() - INTERVAL {seconds} SECOND "
        "ORDER BY Timestamp DESC FORMAT JSONEachRow"
    )
    resp = httpx.post(
        f"http://{CLICKHOUSE_HOST}:{CLICKHOUSE_PORT}/",
        content=query,
        timeout=30,
    )
    resp.raise_for_status()
    lines = [line for line in resp.text.splitlines() if line.strip()]
    return [json.loads(line) for line in lines]


def export(
    since: str = typer.Option("24h", "--since"),
    format: str = typer.Option("json", "--format", help="json or csv"),
    out: Path = typer.Option(Path("./export.json"), "--out"),
) -> None:
    """Pull metrics (Prometheus) and events (ClickHouse) out to a file."""
    if format not in ("json", "csv"):
        raise typer.BadParameter("--format must be 'json' or 'csv'")

    seconds = _parse_since(since)

    try:
        metrics = _fetch_metrics(seconds)
    except httpx.HTTPError as e:
        console.print(f"[yellow]Couldn't reach Prometheus: {e}[/yellow]")
        metrics = []

    try:
        logs = _fetch_logs(seconds)
    except httpx.HTTPError as e:
        console.print(f"[yellow]Couldn't reach ClickHouse: {e}[/yellow]")
        logs = []

    payload = {"metrics": metrics, "logs": logs}

    if format == "json":
        out.write_text(json.dumps(payload, indent=2))
    else:
        with out.open("w", newline="") as f:
            writer = csv.writer(f)
            writer.writerow(["kind", "record"])
            for m in metrics:
                writer.writerow(["metric", json.dumps(m)])
            for l in logs:
                writer.writerow(["log", json.dumps(l)])

    console.print(f"[green]Wrote {len(metrics)} metric series and {len(logs)} log rows to {out}[/green]")
