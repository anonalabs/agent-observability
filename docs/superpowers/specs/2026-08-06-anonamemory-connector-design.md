# AnonaMemory connector — design

Date: 2026-08-06
Status: approved, not yet implemented

## Goal

After `agentobs connect` finishes wiring an agent's telemetry, offer to push that agent's prompts and responses to [AnonaMemory](https://docs.anonalabs.com/introduction), so the conversation history captured by the observability stack also becomes a queryable memory layer.

## Findings that shape the design

### Responses are not in the telemetry

Prompts are available from telemetry, gated behind an opt-in per agent:

| Agent | Prompt source | Gate |
|---|---|---|
| Claude Code | `otel_logs`, `user_prompt` event, `prompt` attribute | `OTEL_LOG_USER_PROMPTS=1` (default off) |
| Gemini CLI | `gemini_cli.user_prompt` | `telemetry.logPrompts: true` (default off) |
| Cursor, Copilot, Codex, OpenCode | `otel_traces`, span attribute `gen_ai.prompt.0.content` | truncated at 5000 chars; `[MASKED]` when `mask_prompts` is set |

Assistant response text is captured by **none** of them:

- Gemini CLI's `gemini_cli.api_response` carries token counts only.
- The hook shim has no agent-message event. `stop` yields `status` and `loop_count` (`cursorhook/event_attributes.go`).
- Claude Code emits an `assistant_response` event, but `docs/metrics.md` marks it "(observed, undocumented)" and its attributes are unverified. Claude Code's documented telemetry does not export response text.

What telemetry does have beyond prompts: `tool_input`/`tool_output` (10k truncation), shell commands, file edits, errors, cost, and token counts. Tool activity, not model prose.

### Transcripts do have responses

Claude Code writes `~/.claude/projects/<slug>/<session-id>.jsonl`, one JSON object per line. Verified on disk:

- `type: "assistant"` → `message.content[]` of `{type: "text", text: ...}`, plus `message.model`, `message.usage`, `timestamp`, `sessionId`, `uuid`, `cwd`, `gitBranch`, `isSidechain`
- `type: "user"` → the matching prompt

This gives paired prompt+response with a session id and token usage, and does not require `OTEL_LOG_USER_PROMPTS`.

Coverage is uneven and this bounds v1:

| Agent | Prompt | Response |
|---|---|---|
| Claude Code | yes, transcript (confirmed) | yes, transcript (confirmed) |
| Cursor | yes, telemetry | probable — hook JSON carries `transcript_path`, format unverified |
| Copilot, Codex, OpenCode | yes, telemetry | unknown — `docs/other-agents.md` states these were never round-tripped against the real tools |
| Gemini CLI | yes, telemetry | no — no hooks, no transcript path |

### AnonaMemory API

Base URL `https://api.anonalabs.com`. Auth is `Authorization: Bearer anona_live_...`; the docs explicitly reject `X-API-Key` and a bare `Authorization: <key>`. **There is no OAuth** — the connector offers API key entry only.

| Endpoint | Use |
|---|---|
| `GET /v1/spaces` | list spaces → `{spaces: [{space_id, name, created_at}], total}` |
| `POST /v1/spaces` | create → body `{name, description?}` → server-generated `space_id` (`spc_a1b2c3d4`) |
| `POST /v1/record/batch` | ingest 1–100 items, always async → `{job_id, job_ids, status, accepted}` |

`record/batch` body: `{space_id, items: [{content, context?, timestamp?, metadata?}]}`.
`record` (single) additionally accepts `user_id`, `agent_id`, `session_id`, `tags`.

Errors return `{code, message, request_id}`. Retryable: 429, 500, 503 with exponential backoff plus jitter. Never retry other 4xx. No server-side idempotency key.

## Decisions

| Decision | Choice | Reason |
|---|---|---|
| Response source | `memory sync` reads transcripts **and** ClickHouse | Keeps conversation prose out of ClickHouse — no doubled storage, no 90-day TTL over full transcripts, no new hook wiring |
| Shipper placement | `agentobs memory sync`, a CLI command | ClickHouse is already the union point for all agents; `export` sets the precedent for querying it |
| Cadence | Manual, plus a printed crontab line | Preserves the README's "no separate services to babysit" promise |
| Granularity | One turn (prompt + response) = one record | Best retrieval granularity; a session-sized record returns far more than the matching exchange |
| Privacy scope | Opt-in, project allowlist, reuse `MaskSensitiveData` | Consistent with the repo's privacy-off-by-default invariant |

## Architecture

```
~/.claude/projects/*/*.jsonl ──┐
   (paired prompt+response)     ├─▶ memory.Sync ─▶ POST /v1/record/batch
ClickHouse otel_logs/traces ───┘        │            (100 items per call)
   (cost, tokens, tool names)           │
                                   watermark file
```

Two readers, one writer. Transcripts supply conversation content; ClickHouse enriches each turn with cost and tool context joined on session id.

### New package `cli/internal/memory/`

| File | Responsibility |
|---|---|
| `client.go` | REST client: `ListSpaces`, `CreateSpace`, `RecordBatch`. Bearer auth, retry policy, typed error carrying `code` and `request_id` |
| `transcript.go` | `TranscriptSource` interface; `ClaudeCodeSource` walks `~/.claude/projects/*/*.jsonl` and pairs user→assistant turns |
| `clickhouse.go` | Per-session cost/token/tool lookup over `otel_logs` and `otel_traces` |
| `sync.go` | Orchestration: enumerate → allowlist filter → mask → enrich → chunk → record → advance watermark |
| `credentials.go` | Read/write `~/.config/agentobs/memory.json`, mode 0600 |
| `wizard.go` | The connect-time flow, exposed as `OfferConnect` |

### New command `cli/internal/commands/memory.go`

`agentobs memory` with subcommands:

- `sync` — push everything newer than the watermark. Flags: `--quiet`, `--dry-run`, `--since`.
- `status` — show configured space, last sync time, pending turn count.
- `disconnect` — delete `memory.json`. Does not touch anything server-side.

### Data model

```go
type Turn struct {
    Agent      string    // "claude-code"
    SessionID  string
    TurnID     string    // assistant message uuid — the dedup key
    Timestamp  time.Time
    Prompt     string
    Response   string    // empty for prompt-only agents
    Model      string
    CWD        string
    GitBranch  string
    InputTokens  int
    OutputTokens int
    CostUSD    float64   // from ClickHouse, joined on session id
    Tools      []string
}
```

Mapping to a `record/batch` item:

| Field | Value |
|---|---|
| `content` | `"User: <prompt>\n\nAssistant: <response>"`, or just the prompt when `Response` is empty |
| `context` | repo name, git branch, model |
| `timestamp` | `Turn.Timestamp`, ISO 8601 |
| `metadata` | `turn_id`, `session_id`, `agent_id`, `input_tokens`, `output_tokens`, `cost_usd`, `tools`, `cwd`, `git_branch`, `has_response` |

`record/batch` items accept only `content`, `context`, `timestamp`, and `metadata`, so `session_id` and `agent_id` travel inside `metadata`. `tags` are unavailable on the batch endpoint; batching is worth more than tags at this volume.

### Project allowlist

`memory.json` stores a list of absolute directory paths. A turn is pushed only when `Turn.CWD` is equal to, or nested under, one of them. An empty list means nothing is pushed — the allowlist is never implicitly "all".

### Deduplication

The batch endpoint offers no idempotency key, so dedup is entirely local. `memory.json` stores the last synced timestamp plus the set of `turn_id`s seen within a trailing 24-hour window. Sources are queried from `watermark - 24h`, not the watermark itself, so anything that arrived late relative to a faster source is re-read rather than permanently skipped; a turn is pushed only if its `turn_id` is not already in the seen set. (An explicit `--since` backfill is the one exception: it names its own window and is used as-is, with no lookback added, so a manual backfill never silently fetches more than asked.) The window bounds the file's growth while covering out-of-order transcript writes.

## Connect wizard flow

Runs at the tail of every `connect` path, after `printNextSteps`. Default is No. Skipped entirely under `--non-interactive` unless `--memory` is passed or `memory.enabled` is set in the YAML config.

1. Print the signup link (`https://docs.anonalabs.com/quickstart` — the dashboard's API keys page; signing up does not create a key automatically).
2. Read the key via `survey.Password` so it does not echo.
3. `GET /v1/spaces` → `survey.Select` over space names plus a `Create new space…` entry.
4. If creating: ask for a name → `POST /v1/spaces`.
5. Ask for the project allowlist, defaulting to the current repo root.
6. Write `memory.json` with mode 0600.
7. Run the first sync and report how many turns were pushed.
8. Print the crontab line: `0 * * * * agentobs memory sync --quiet`.

### Refactor in `connect.go`

`connect.go` is the largest file in the CLI (581 lines) and each of its four paths returns directly from `RunE`. The wizard call would otherwise be duplicated four times. Change `RunE` to capture the path's error, then call `memory.OfferConnect(...)` once before returning. Targeted and in scope; no other restructuring.

## Error handling

- **Wizard failure** (bad key, network down, no spaces returned): warn and save nothing. Does **not** fail `connect` — telemetry setup already succeeded and must not be rolled back by an optional add-on.
- **Sync, retryable status** (429, 500, 503): exponential backoff with jitter, capped at 5 attempts.
- **Sync, other 4xx**: abort immediately, printing `code` and `request_id`.
- **Failed batch**: the watermark advances over whatever contiguous, oldest-first prefix the batch call reports as accepted before the error; only the turns after that prefix retry on the next run, so a partial failure doesn't re-push chunks the server already took.
- **ClickHouse unreachable**: sync proceeds without cost/tool enrichment. Degraded, not fatal — transcripts are the primary source.
- **Unreadable or malformed transcript**: skip the file, count it, report the count in the summary rather than aborting the run.

## Testing

Table-driven tests with no network access, following the existing `internal/config/config_test.go` pattern.

| File | Cases |
|---|---|
| `transcript_test.go` | fixture JSONL → expected Turns: multi-block assistant messages, `isSidechain` filtering, `tool_use` blocks, malformed lines |
| `client_test.go` | `httptest` server: exact `Authorization` header, chunking at 100 items, retry on 429 then success, no retry on 400 |
| `sync_test.go` | watermark advance and dedup, allowlist filtering, mask application |
| `credentials_test.go` | 0600 permissions, round-trip |

## Scope

**In v1:**

- Full prompt+response records for Claude Code, from transcripts.
- Prompt-only records for Cursor, Copilot, Codex, OpenCode, and Gemini CLI, sourced from ClickHouse and marked `metadata.has_response: false`. These agents' prompt text is itself gated behind an opt-in that defaults off (`OTEL_LOG_USER_PROMPTS`, `telemetry.logPrompts`, or the hook shim's `mask_prompts`), so an installation that never enabled prompt logging contributes no records for them. `memory status` reports this rather than silently syncing nothing.
- The connect wizard, `memory sync`, `memory status`, `memory disconnect`.

**Out of v1:**

- OAuth — AnonaMemory does not support it.
- Response capture for any agent other than Claude Code. Each additional agent needs its transcript format verified against the real tool first; `TranscriptSource` makes each one a small addition rather than a rework.
- Retrieval. This connector writes only; reading memories back into an agent's context is a separate project.
- Secret scanning beyond `MaskSensitiveData`'s existing email and home-path masking.

## Open risk

Prompt-only records may dilute retrieval quality in a space that also holds full exchanges. `metadata.has_response` makes them identifiable, so they can be filtered or purged if that turns out to be a problem in practice.
