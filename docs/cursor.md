# Cursor

Cursor has no native OTel export. `agentobs` bundles its own hook shim (`internal/cursorhook`, a from-scratch Go reimplementation) that Cursor's hooks system shells out to on every agent event, converting each into an OTel span sent to the collector. No separate binary or install step -- it's the same `agentobs` binary, invoked as `agentobs cursor-hook` (a hidden subcommand; Cursor's wrapper script calls it directly, you shouldn't need to run it by hand).

Every span is linked to its parent across process invocations (each hook event runs in a fresh process) via a small file-based context store in the system temp dir, so a full session shows up as one correlated trace in Grafana rather than disconnected single spans -- this holds for both `grpc` and `http/protobuf` protocols.

## Setup

```bash
agentobs connect --agent cursor
```

This merges (never replaces) `~/.cursor/hooks.json` and writes `~/.cursor/hooks/otel_config.json` + `~/.cursor/hooks/otel_hook.sh`. If you already have other hooks registered (e.g. a different tool's `sessionStart` hook), they're preserved -- Cursor supports multiple commands per event.

Restart Cursor IDE afterward to pick up the new hooks.

## What it captures (spans, not metrics/logs)

Unlike Claude Code and Gemini CLI, Cursor's data lands in **traces** (`otel_traces` table in ClickHouse, added to the collector's pipeline for this reason).

| Hook event | Span meaning |
|---|---|
| `sessionStart` / `sessionEnd` | session lifecycle |
| `preToolUse` / `postToolUse` / `postToolUseFailure` | tool calls |
| `beforeShellExecution` / `afterShellExecution` | shell commands (as a tool span) |
| `beforeMCPExecution` / `afterMCPExecution` | MCP calls (as a tool span) |
| `beforeReadFile` / `afterFileEdit` | file operations |
| `beforeSubmitPrompt` | prompt submission |
| `stop` | agent run completion |
| `subagentStart` / `subagentStop` | subagent activity |

Key span attributes (GenAI + LangSmith conventions): `gen_ai.tool.name`, `gen_ai.request.model`, `langsmith.trace.session_id`, `langsmith.metadata.duration_ms`, `langsmith.metadata.shell_command`, `langsmith.metadata.file_path`.

## Privacy

`CURSOR_OTEL_MASK_PROMPTS` (set via the `agentobs connect` prompt) masks prompt text, tool I/O, file path usernames, and emails -- same privacy-by-default posture as Claude Code's `OTEL_LOG_USER_PROMPTS`.

## Dashboard

See **Cursor Traces** in Grafana: span volume by name, tool usage breakdown, average span duration, and span-level errors -- all queried from ClickHouse's `otel_traces` table.
