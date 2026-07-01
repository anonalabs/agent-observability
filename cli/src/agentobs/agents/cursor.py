"""Cursor has no native OTel export -- it needs the cursor-otel-hook shim
(vendored at integrations/cursor-otel-hook) wired into ~/.cursor/hooks.json.
Unlike Claude Code, this agent is configured via files, not shell env vars.
"""

from __future__ import annotations

import json
from pathlib import Path

HOOK_EVENTS = [
    "sessionStart",
    "sessionEnd",
    "preToolUse",
    "postToolUse",
    "postToolUseFailure",
    "beforeShellExecution",
    "afterShellExecution",
    "beforeMCPExecution",
    "afterMCPExecution",
    "beforeReadFile",
    "afterFileEdit",
    "beforeSubmitPrompt",
    "preCompact",
    "stop",
    "subagentStart",
    "subagentStop",
]


class CursorAgent:
    name = "cursor"

    def detect(self) -> bool:
        return (Path.home() / ".cursor").exists()

    def hooks_dir(self) -> Path:
        return Path.home() / ".cursor" / "hooks"

    def wrapper_script_path(self) -> Path:
        return self.hooks_dir() / "otel_hook.sh"

    def config_path(self) -> Path:
        return self.hooks_dir() / "otel_config.json"

    def hooks_json_path(self) -> Path:
        return Path.home() / ".cursor" / "hooks.json"

    def vendored_package_path(self) -> Path:
        # cli/src/agentobs/agents/cursor.py -> repo root is 4 parents up.
        return Path(__file__).resolve().parents[4] / "integrations" / "cursor-otel-hook"

    def otel_config(self, endpoint: str, mask_prompts: bool = False) -> dict:
        return {
            "OTEL_EXPORTER_OTLP_ENDPOINT": endpoint,
            "OTEL_SERVICE_NAME": "cursor-agent",
            "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
            "OTEL_EXPORTER_OTLP_INSECURE": "true",
            "OTEL_EXPORTER_OTLP_HEADERS": None,
            "CURSOR_OTEL_MASK_PROMPTS": "true" if mask_prompts else "false",
            "OTEL_EXPORTER_OTLP_TIMEOUT": "30",
        }

    def hooks_json(self, existing: dict | None = None) -> dict:
        """Merge our hook entry into each event's list -- never replace whatever
        else (other tools, e.g. hindsight) already registered for that event."""
        wrapper = str(self.wrapper_script_path())
        entry = {"command": wrapper, "timeout": 5}

        merged = {"version": 1, "hooks": {}}
        if existing:
            merged["version"] = existing.get("version", 1)
            merged["hooks"] = {k: list(v) for k, v in existing.get("hooks", {}).items()}

        for event in HOOK_EVENTS:
            entries = merged["hooks"].setdefault(event, [])
            if not any(e.get("command") == wrapper for e in entries):
                entries.append(entry)

        return merged

    def wrapper_script(self) -> str:
        return (
            "#!/bin/bash\n"
            f'exec cursor-otel-hook --config "{self.config_path()}" "$@"\n'
        )
