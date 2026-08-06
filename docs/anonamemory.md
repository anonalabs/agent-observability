# AnonaMemory

Push your agents' prompts and responses to [AnonaMemory](https://docs.anonalabs.com/introduction), so the conversation history the stack already sees becomes a queryable memory layer.

This is off by default and asks before doing anything.

## Setup

`agentobs connect` offers it at the end:

```
Also push prompts and responses to AnonaMemory? (y/N)
```

Answer yes and it walks through:

1. **API key.** Create one at <https://docs.anonalabs.com/quickstart> — dashboard, API keys, New key. Signing up does not create one for you. The key is not echoed as you type.
2. **Space.** Existing spaces are listed; pick one or create a new one by name.
3. **Project directories.** Only sessions whose working directory is inside one of these is ever pushed. This is deny-by-default: an empty list pushes nothing.
4. **First sync**, then the cron line to keep it current.

Config lands at `~/.config/agentobs/memory.json`, mode 0600 (its directory is created/kept at 0700). It holds your API key.

## Keeping it current

Nothing runs in the background. Add the printed cron entry:

```
0 * * * * agentobs memory sync --quiet
```

`--quiet` suppresses only the routine "Pushed N turns" line. Non-zero skip counts and degradation warnings (ClickHouse unreachable, unreadable transcript files, unparseable rows) still print, to stderr, so cron's default "mail me anything the job wrote" behavior still surfaces them -- a quiet cron run is one with no output at all, not one that hides problems.

Or run it by hand whenever you want:

```bash
agentobs memory sync              # push everything since the last sync
agentobs memory sync --dry-run    # count what would go, change nothing
agentobs memory sync --since 7d   # backfill, ignoring the watermark
agentobs memory status            # space, last sync, pending count
agentobs memory disconnect        # delete the local config (server-side data is untouched)
```

## What actually gets pushed

One record per turn: the prompt, the response when there is one, a `context` string with the repo name (plus git branch, plus model, when known), and metadata for turn id, session id, agent, `has_response`, token counts, cost, working directory, git branch, and tool names.

`content` (the prompt+response text) is capped at 20,000 characters, truncated with a trailing `... (truncated)` marker if a turn runs over -- Claude Code prompts routinely carry large pasted files, and an oversized item would otherwise draw a non-retryable 4xx from AnonaMemory's body limit that wedges the connector into retrying the same batch forever (the watermark can't advance past a batch that never succeeds). This is separate from, and more generous than, the telemetry path's own 5,000/10,000-character prompt/tool-output caps (`cursorhook/event_attributes.go`), which apply before a prompt-only turn ever reaches this connector.

Coverage is uneven, because the agents differ in what they expose:

| Agent | Prompt | Response |
|---|---|---|
| Claude Code | yes | yes — read from `~/.claude/projects/*/*.jsonl` |
| Cursor, OpenCode | yes, by default — unless masking was turned on at connect time | no |
| GitHub Copilot coding agent, Codex | telemetry carries prompt text, but delivery here is unconfirmed — see below | no |
| Gemini CLI | **not supported** | not supported |

Only Claude Code writes a session transcript this can read, so it is the only agent that contributes full prompt+response exchanges today. Cursor and OpenCode contribute prompt-only records (tagged `metadata.has_response: false`), read from their OTel spans in ClickHouse rather than a transcript file — specifically, the working directory the project allowlist filters on comes from the `workspace_roots` attribute, which the hook shim only stamps on a span when the incoming hook payload actually contains a `workspace_roots` key (`addCommonAttributes` in `cli/internal/cursorhook/hook.go`). That's confirmed for Cursor, whose own hook protocol supplies it, and for OpenCode, whose `agentobs connect --agent opencode` plugin now sends its plugin-provided project directory as `workspace_roots` on every invocation. GitHub Copilot coding agent and Codex have never been round-tripped against the real tools for this field (`docs/other-agents.md`); until one is, its turns still land in ClickHouse with prompt text (capture is opt-out, see below) but arrive with an empty working directory and are dropped by the project allowlist's deny-by-default (`Credentials.AllowsPath` rejects an empty path) before this connector ever sees them — the same class of dead code the `otel_logs`/Gemini CLI branch was already removed to avoid shipping.

**This capture is opt-out, not opt-in, and it matters for privacy.** `agentobs connect` asks "Mask prompts/file paths/emails? (privacy)" for each of the four hook-shim agents (Cursor, Copilot, Codex, OpenCode), and the answer defaults to **No**, regardless of whether that agent's turns can currently reach AnonaMemory. Answering No (or just hitting enter) means the *full, unmasked* prompt text is written into the span's `gen_ai.prompt.0.content` attribute in ClickHouse from that point on, whether or not `workspace_roots` is ever present on the same span. Only if you explicitly answer **yes** does the hook shim store the literal string `[MASKED]` there instead — and only then does the sync query (`PromptOnlyTurns` in `cli/internal/memory/clickhouse.go`) skip it, because it excludes rows where that attribute is `[MASKED]` or empty. For Cursor and OpenCode, that unmasked text is exactly what this connector picks up and forwards; the only way to stop that is to have answered yes to the mask-prompts question when you connected them (or to keep their sessions out of the project allowlist). For Copilot and Codex, whether it's forwarded depends on the still-unverified `workspace_roots` question above — but the prompt text still reaches ClickHouse either way, so the masking question is worth answering deliberately for all four, not just the two confirmed to sync.

**Gemini CLI is not supported for memory sync, even though its telemetry can carry prompt text (`telemetry.logPrompts`).** Gemini CLI's events don't carry a working-directory attribute anywhere, so a prompt from it can never pass the project allowlist — there is no code path left that even tries to read it.

`agentobs memory status` reports why nothing is pending rather than just showing zero: if ClickHouse can't be reached, it says so; otherwise, when the pending count is zero, it reminds you that Cursor and OpenCode only show up here if you *didn't* opt into masking for them (or if none of their sessions match the project allowlist), and that Copilot and Codex aren't confirmed to show up here at all yet.

## Privacy

- Off unless you turn it on, per machine.
- The project allowlist is deny-by-default. Turns from directories you did not list never leave.
- Prompt, response, working directory, and git branch text are masked before sending via `Turn.Masked()` — home-directory usernames and email addresses are redacted, reusing the hook shim's `--mask-prompts` patterns. Masking is deliberately not perfect: it skips strings shaped like a version pin or module ref (`express@4.18.2`, `github.com/spf13/cobra@v1.10.2`) and known git-ref words used after an `@` (`actions/checkout@main`, `user@HEAD`) so technical content in prompts and responses isn't corrupted — a real hostname that happens to be one of those ref words (e.g. `admin@main`) is left unmasked too. Treat this as pragmatic scrubbing, not guaranteed redaction.
- Conversation text is never written to ClickHouse or Prometheus *by this connector*. Claude Code's prompt and response text is read straight from its transcript at sync time and goes to AnonaMemory only. The hook-shim agents (Cursor, Copilot, Codex, OpenCode) are different: their prompt text lands in ClickHouse's `otel_traces` table as ordinary span data by default, regardless of whether AnonaMemory is connected at all — that happens unless you opted into masking for that agent at connect time (see above). This connector doesn't cause that capture; it just reads the row back out and forwards it if it's there.
- The connect-time "Mask prompts/file paths/emails?" question (hook-shim agents only) and this connector's own masking (`Turn.Masked()`, above) are two different things. The first controls whether prompt text reaches ClickHouse at all; the second re-scrubs whatever text this connector gathers — from ClickHouse or from a Claude Code transcript — right before it's pushed to AnonaMemory. Opting into the first makes the second moot for that agent's prompts (there's no text left to push); the second still applies to Claude Code, which the first setting doesn't touch at all.
- `agentobs memory disconnect` removes the local config. Deleting memories already recorded is done in the AnonaMemory dashboard.

## Sync output

A non-quiet `sync` reports several counts, each meaning something different:

- **Pushed** — turns actually sent (or that would be sent, with `--dry-run`).
- **Skipped ... already-synced turns** — deduped locally against the last 24h of pushed turn ids, since the batch endpoint has no idempotency key.
- **Skipped ... turns outside the project allowlist** — filtered by the deny-by-default project list, not an error.
- **Skipped ... unreadable transcript files** — Claude Code `.jsonl` files that couldn't be opened or parsed this run. Every run re-reads every matching file in full (the watermark only filters turns after a file parses, not which files get opened), so a transient failure -- a lock, a permissions blip -- recovers on its own next run. That recovery is bounded by the same 24-hour lookback the dedup bullet above relies on: sources are queried from `watermark - 24h`, so if the file is still unreadable by the time a turn inside it falls more than 24h behind the watermark, that turn is genuinely missed, not just delayed. Investigate promptly if this is nonzero rather than assuming it will resolve itself.
- **ClickHouse returned N rows that could not be read** — rows from ClickHouse (prompt-only turns or session cost/token stats) with a timestamp or number that failed to parse. Unlike the transcript case, a malformed value in an already-inserted row doesn't fix itself on retry, so this is effectively permanent for that row; the count is a signal to check for a ClickHouse-side data problem, not something to wait out.

If ClickHouse itself is unreachable, sync degrades rather than failing outright: Claude Code turns still go out, just without cost/token enrichment, and Cursor/OpenCode (Copilot/Codex, once confirmed) contribute nothing for that run.

## Notes

- AnonaMemory has no OAuth. API key only.
- Writes are async: the API returns a `job_id`, not a memory id.
- Deduplication is local, keyed on turn id, since the batch endpoint has no idempotency key. Deleting `memory.json` and reconnecting can re-push recent turns.
