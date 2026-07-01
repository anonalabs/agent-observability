"""Gemini CLI: native OTel like Claude Code, but configured via .gemini/settings.json
(with OTEL_EXPORTER_OTLP_ENDPOINT as the one env var override) rather than a full
env var surface. Settings are merged, never replaced -- other keys in that file
(model choice, sandbox, mcpServers, etc.) must survive."""

from __future__ import annotations

import shutil
from pathlib import Path


class GeminiCliAgent:
    name = "gemini-cli"

    def detect(self) -> bool:
        return shutil.which("gemini") is not None or (Path.home() / ".gemini").exists()

    def settings_path(self) -> Path:
        return Path.home() / ".gemini" / "settings.json"

    def env_vars(self, endpoint: str) -> dict:
        # The one thing Gemini CLI reads from the environment; everything else
        # (enabled, target, logPrompts) lives in settings.json.
        return {"OTEL_EXPORTER_OTLP_ENDPOINT": endpoint}

    def merge_settings(self, existing: dict | None, log_prompts: bool = False) -> dict:
        merged = dict(existing) if existing else {}
        telemetry = dict(merged.get("telemetry", {}))
        telemetry["enabled"] = True
        telemetry["target"] = "local"
        telemetry["logPrompts"] = log_prompts
        merged["telemetry"] = telemetry
        return merged
