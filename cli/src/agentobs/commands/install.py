from __future__ import annotations

import socket
from pathlib import Path
from typing import Optional

import typer
from rich.console import Console

from .. import compose
from ..config import load_yaml_config

console = Console()

REQUIRED_PORTS = {
    4317: "OTLP gRPC",
    4318: "OTLP HTTP",
    8889: "Prometheus exporter",
    9090: "Prometheus UI",
    8123: "ClickHouse HTTP",
    9000: "ClickHouse native",
    3000: "Grafana",
}


def _port_in_use(port: int) -> bool:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        return sock.connect_ex(("localhost", port)) == 0


def install(
    config: Optional[Path] = typer.Option(None, "--config", help="agentobs.yaml path"),
    compose_file: Optional[Path] = typer.Option(None, "--compose-file"),
    detach: bool = typer.Option(True, "--detach/--foreground"),
    non_interactive: bool = typer.Option(False, "--non-interactive", "--yes"),
) -> None:
    """Bring up the collector/prometheus/clickhouse/grafana stack."""
    load_yaml_config(config)  # validated early; port overrides deferred to a future release

    if not compose.docker_available():
        console.print("[red]Docker isn't available (checked `docker info`). Install/start Docker and retry.[/red]")
        raise typer.Exit(1)

    busy = [f"{port} ({desc})" for port, desc in REQUIRED_PORTS.items() if _port_in_use(port)]
    if busy:
        console.print(f"[yellow]Ports already in use: {', '.join(busy)}[/yellow]")
        if not non_interactive:
            if not typer.confirm("Continue anyway?", default=False):
                raise typer.Exit(1)

    resolved_compose_file = compose.resolve_compose_file(compose_file)
    console.print(f"Starting stack via {resolved_compose_file} ...")
    compose.up(resolved_compose_file, detach=detach)

    console.print("[green]Stack is up.[/green]")
    console.print("Grafana:    http://localhost:3000")
    console.print("Prometheus: http://localhost:9090")
    console.print("\nNext: run [bold]agentobs connect[/bold] to wire up an agent's telemetry.")
