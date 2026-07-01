"""Pluggability seam: each supported coding agent implements this protocol."""

from __future__ import annotations

from typing import Protocol


class Agent(Protocol):
    name: str

    def detect(self) -> bool:
        """Return True if this agent looks installed on the current machine."""
        ...

    def env_vars(self, endpoint: str) -> dict:
        """Return the env vars needed to point this agent's telemetry at `endpoint`."""
        ...
