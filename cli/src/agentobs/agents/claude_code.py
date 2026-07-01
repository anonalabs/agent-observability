"""Claude Code: emits telemetry natively via OpenTelemetry once these env vars are set."""

from __future__ import annotations

import shutil
from pathlib import Path


class ClaudeCodeAgent:
    name = "claude-code"

    def detect(self) -> bool:
        return shutil.which("claude") is not None or (Path.home() / ".claude").exists()

    def env_vars(
        self,
        endpoint: str,
        log_user_prompts: bool = False,
        log_tool_details: bool = False,
    ) -> dict:
        env = {
            "CLAUDE_CODE_ENABLE_TELEMETRY": "1",
            "OTEL_METRICS_EXPORTER": "otlp",
            "OTEL_LOGS_EXPORTER": "otlp",
            "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
            "OTEL_EXPORTER_OTLP_ENDPOINT": endpoint,
            # Prometheus-style backends drop delta metrics silently without this.
            "OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE": "cumulative",
        }
        if log_user_prompts:
            env["OTEL_LOG_USER_PROMPTS"] = "1"
        if log_tool_details:
            env["OTEL_LOG_TOOL_DETAILS"] = "1"
        return env
