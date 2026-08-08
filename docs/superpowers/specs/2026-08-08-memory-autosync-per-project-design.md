# Auto-sync and per-project spaces — design

Date: 2026-08-08
Status: approved, not yet implemented
Builds on: `docs/superpowers/specs/2026-08-06-anonamemory-connector-design.md`

## Goal

Make the AnonaMemory connector run itself: push each Claude Code session to AnonaMemory when the session ends, and route each project's turns to its own space, configured in one file.

Scope is the write side only. Reading memories back — recall, an MCP server, retrieval into an agent's context — is explicitly out of scope.

## Why

Three limits in the shipped connector prompted this:

1. **Nothing is automatic.** `agentobs memory sync` only runs when typed. The wizard prints a cron line it never installs.
2. **One space for everything.** A single `space_id` and one flat allowlist mean every project lands in the same space. Working around it today means a separate config file per project, selected through `AGENTOBS_MEMORY_CONFIG` on every invocation.
3. **One global watermark.** Syncing any project advances a watermark that gates *every* project. In practice this silently skipped about 2,240 turns: a narrow `--since 20m` test run pushed the watermark to the present, putting all older history below it.

A timer-based auto-sync would have made a fourth problem worse. A sync landing mid-exchange pushes that exchange again on completion under a new `turn_id`, so hourly runs during long sessions leave partial earlier copies. Triggering on session end avoids this entirely — the exchange is complete before anything is sent.

## Decisions

| Decision | Choice | Reason |
|---|---|---|
| Auto-sync trigger | Claude Code `Stop` hook | Exchanges are always complete when pushed; no partial duplicates, which no timer interval can guarantee |
| Space naming | Explicit folder → space map | Predictable, no derived-name collisions between projects sharing a basename |
| Config shape | One file, a list of project entries | Replaces per-file `AGENTOBS_MEMORY_CONFIG` juggling; one command covers every project |
| Watermark scope | Per project | A global watermark lets one project's sync skip another's turns |
| Other agents | Manual sync only | Only Claude Code exposes a session-end hook this can attach to |

## Config v2

```json
{
  "version": 2,
  "api_key": "anona_live_...",
  "base_url": "",
  "projects": [
    {
      "path": "/home/srujan/Documents/Projects/agent-observability",
      "space_id": "agent-observability",
      "watermark": "2026-08-08T17:21:15Z",
      "recent_turns": {}
    }
  ]
}
```

`api_key` and `base_url` stay global — one account, one endpoint. Everything else moves per project.

**Routing.** A turn belongs to the entry whose `path` is the longest prefix of the turn's `cwd`, matched on whole path components. No match means the turn is not pushed, preserving deny-by-default. Longest-prefix matters for nested projects: a worktree under a parent repo must route to its own entry when one exists.

**Migration.** A v1 config — flat `space_id`, `space_name`, `projects` as strings, single `watermark` and `recent_turns` — is upgraded on load. Every old path becomes an entry pointing at the old space, carrying the old watermark and dedup set, so no turn is re-pushed and none is skipped. The file is backed up to `memory.json.bak` before the upgraded form is written, because it holds an API key and a botched rewrite would cost the user their credential. Migration happens on load and is persisted on the next save.

## Components

### `cli/internal/memory/credentials.go` — config v2 and migration

- `Project` struct: `Path`, `SpaceID`, `Watermark`, `RecentTurns`
- `Credentials` gains `Version` and `Projects []Project`; loses top-level `SpaceID`, `SpaceName`, `Watermark`, `RecentTurns`
- `(c *Credentials) ProjectFor(cwd string) *Project` — longest-prefix match, nil when none
- `Seen` / `MarkSynced` / `AllowsPath` move onto `*Project`
- `migrateV1` runs inside `LoadCredentials`

### `cli/internal/memory/sync.go` — per-project sync

`Sync` currently takes one space and one allowlist. It becomes:

- `SyncProject(creds *Credentials, p *Project, rec Recorder, sources []TranscriptSource, enricher Enricher, opts Options) (Result, error)` — the existing logic, scoped to one entry
- `SyncAll(...) (map[string]Result, error)` — iterates entries, calling `SyncProject` for each

One project's failure must not abort the others: its error is recorded against that project and its watermark is left unmoved, while the remaining projects proceed.

### `cli/internal/memory/hook.go` — Stop-hook handling

Claude Code delivers a JSON payload on stdin carrying `session_id`, `transcript_path`, and `cwd`.

- `ParseHookPayload(r io.Reader) (HookPayload, error)`
- `RunHook(payload HookPayload) error` — resolve `cwd` to a project entry, sync only that entry

### `cli/internal/commands/memory.go` — new subcommands

- `agentobs memory hook` (hidden) — the Stop-hook entry point
- `agentobs memory sync --project <path>` — sync one entry
- `agentobs memory projects` — list entries with space, last sync, and pending count

### `cli/internal/agents/claudecode.go` — Stop-hook registration

Claude Code is currently an env-vars-only spec in `builtin.yaml` with no hook wiring. Registration merges into `~/.claude/settings.json`:

```json
{ "hooks": { "Stop": [ { "hooks": [ { "type": "command", "command": "agentobs memory hook" } ] } ] } }
```

The file is backed up first and merged, never replaced — matching how `connect` already treats `hooks.json` and `settings.json` for every other agent. The entry is added only when an identical command is not already present, so re-running `connect` cannot duplicate it.

### `cli/internal/commands/connect.go`

The wizard offers Stop-hook registration after the existing AnonaMemory setup, defaulting to yes when the user has just connected AnonaMemory. Declining leaves manual sync working.

## The detached sync

A sync is network I/O against AnonaMemory — seconds, occasionally tens of seconds under backoff. Claude Code waits for a Stop hook to exit, so syncing inline would delay the end of every session.

`agentobs memory hook` therefore re-execs itself as a detached child (`agentobs memory hook --detached`, stdin content passed through a temp file), then returns immediately. The parent exits in milliseconds; the child performs the sync after Claude Code has moved on.

**Guards this needs:**

- A per-project lock file prevents two concurrent syncs of the same project from double-pushing. A held lock means the second invocation exits without syncing rather than waiting.
- The child never writes to stdout or stderr. Everything goes to `~/.config/agentobs/sync.log`, which is size-capped and truncated from the front when it exceeds 1 MB.
- The hook always exits 0, whatever happens. A broken connector must never break the agent — the same principle that makes `OfferConnect` swallow its errors during `connect`.
- The child inherits no terminal and is reparented to init, so it survives the parent's exit.

## Error handling

- **Malformed or empty hook stdin:** log and exit 0. Never block the agent.
- **`cwd` matches no project:** log and exit 0. Not an error — deny-by-default working as intended.
- **API failure inside the detached child:** logged; the project's watermark does not advance; the next session end retries.
- **Lock held:** log "already syncing", exit 0.
- **Config missing:** log and exit 0 — the hook may outlive an `agentobs memory disconnect`.

## Testing

Table-driven, stdlib `testing` only, no network.

| File | Cases |
|---|---|
| `credentials_test.go` | v1→v2 migration preserving watermark and dedup set; backup written before rewrite; `ProjectFor` longest-prefix including nested projects, sibling with shared prefix, and no-match; per-project watermark isolation |
| `sync_test.go` | `SyncAll` continues past one project's failure; the failed project's watermark is unmoved while others advance |
| `hook_test.go` | payload parsing for well-formed, malformed, and empty stdin; `cwd` with no matching project is a no-op; lock contention exits without syncing |
| `claudecode_test.go` | merge into an existing `settings.json` with unrelated hooks preserved; re-running does not duplicate the entry |

The detached re-exec is verified by running the built binary, not by unit test — the parent's exit latency is the property that matters and it is not observable in-process.

## README

Rewrite the connector section to describe what shipped:

- `sync` is incremental; `--since` is the full backfill. The current text does not distinguish them.
- The folder allowlist, per-project spaces, and how to scope to one directory.
- Stop-hook auto-sync, and that nothing else is automatic.
- Correct the claim that "prompt/tool-detail logging is off by default across every agent" — false for Cursor, Copilot, Codex, and OpenCode, whose `mask_prompts` defaults to false so prompt text is captured unless masking is opted into.
- Fix the quickstart: it instructs `curl | sh` then `agentobs install`, which cannot work because no compose file ships with the binary.

## Out of scope

- Recall, retrieval, MCP — the read side, deliberately excluded.
- Auto-sync for agents other than Claude Code; none expose a usable session-end hook to this tool today.
- Deriving space names from folder names; the explicit map was chosen instead.
- `memory prune` for the partial duplicates left by mid-session syncs. The Stop hook largely prevents them being created; cleaning existing ones is separate work.

## Known consequence carried forward

A session that ends while an exchange is still being written can still produce a partial record, because `turn_id` keys on the last text-bearing assistant message. The Stop hook makes this rare rather than routine — it fires after the exchange completes — but it is not eliminated, and the existing documentation of this behaviour in `docs/anonamemory.md` remains accurate.
