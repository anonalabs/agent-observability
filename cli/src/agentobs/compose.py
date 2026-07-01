"""Wraps `docker compose` against this repo's docker-compose.yml."""

from __future__ import annotations

import os
import subprocess
from pathlib import Path
from typing import Optional

SERVICES = ["otel-collector", "prometheus", "clickhouse", "grafana"]


def default_compose_file() -> Path:
    # cli/src/agentobs/compose.py -> repo root is 3 parents up from this file's dir.
    repo_root = Path(__file__).resolve().parents[3]
    candidate = repo_root / "docker-compose.yml"
    if candidate.exists():
        return candidate
    raise FileNotFoundError(
        "could not locate docker-compose.yml next to the agentobs package; "
        "pass --compose-file explicitly"
    )


def resolve_compose_file(explicit: Optional[Path]) -> Path:
    if explicit is not None:
        return explicit
    env_path = os.environ.get("AGENTOBS_COMPOSE_FILE")
    if env_path:
        return Path(env_path)
    return default_compose_file()


def run(compose_file: Path, *args: str, check: bool = True) -> subprocess.CompletedProcess:
    cmd = ["docker", "compose", "-f", str(compose_file), *args]
    return subprocess.run(cmd, check=check)


def up(compose_file: Path, detach: bool = True) -> subprocess.CompletedProcess:
    args = ["up"]
    if detach:
        args.append("-d")
    return run(compose_file, *args)


def ps(compose_file: Path) -> subprocess.CompletedProcess:
    return run(compose_file, "ps", check=False)


def docker_available() -> bool:
    try:
        subprocess.run(
            ["docker", "info"],
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        return True
    except (subprocess.CalledProcessError, FileNotFoundError):
        return False
