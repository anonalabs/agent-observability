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

Coverage is uneven, because the agents differ in what they expose:

| Agent | Prompt | Response |
|---|---|---|
| Claude Code | yes | yes — read from `~/.claude/projects/*/*.jsonl` |
| Cursor, GitHub Copilot coding agent, Codex, OpenCode | yes, by default — unless masking was turned on at connect time | no |
| Gemini CLI | **not supported** | not supported |

Only Claude Code writes a session transcript this can read, so it is the only agent that contributes full prompt+response exchanges today. The four hook-shim agents contribute prompt-only records (tagged `metadata.has_response: false`), read from their OTel spans in ClickHouse rather than a transcript file — specifically, the working directory the project allowlist filters on comes from the `workspace_roots` attribute the hook shim stamps on every span.

**This capture is opt-out, not opt-in, and it matters for privacy.** `agentobs connect` asks "Mask prompts/file paths/emails? (privacy)" for each of these four agents, and the answer defaults to **No**. Answering No (or just hitting enter) means the *full, unmasked* prompt text is written into the span's `gen_ai.prompt.0.content` attribute in ClickHouse from that point on, and this connector will pick it up. Only if you explicitly answer **yes** does the hook shim store the literal string `[MASKED]` there instead — and only then does the sync query (`PromptOnlyTurns` in `cli/internal/memory/clickhouse.go`) skip it, because it excludes rows where that attribute is `[MASKED]` or empty. So on a default `agentobs connect` run for these agents, they *do* contribute prompt-only records; the only way to stop that is to have answered yes to the mask-prompts question when you connected them (or to keep their sessions out of the project allowlist).

**Gemini CLI is not supported for memory sync, even though its telemetry can carry prompt text (`telemetry.logPrompts`).** Gemini CLI's events don't carry a working-directory attribute anywhere, so a prompt from it can never pass the project allowlist — there is no code path left that even tries to read it.

`agentobs memory status` reports why nothing is pending rather than just showing zero: if ClickHouse can't be reached, it says so; otherwise, when the pending count is zero, it reminds you that the four hook-shim agents only show up here if you *didn't* opt into masking for them (or if none of their sessions match the project allowlist).

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
- **Skipped ... unreadable transcript files** — Claude Code `.jsonl` files that couldn't be opened or parsed; that file's turns are missing from this sync, not lost, but investigate if this is nonzero.
- **ClickHouse returned N rows that could not be read** — rows from ClickHouse (prompt-only turns or session cost/token stats) with a timestamp or number that failed to parse; also missing rather than lost.

If ClickHouse itself is unreachable, sync degrades rather than failing outright: Claude Code turns still go out, just without cost/token enrichment, and the hook-shim agents contribute nothing for that run.

## Notes

- AnonaMemory has no OAuth. API key only.
- Writes are async: the API returns a `job_id`, not a memory id.
- Deduplication is local, keyed on turn id, since the batch endpoint has no idempotency key. Deleting `memory.json` and reconnecting can re-push recent turns.
