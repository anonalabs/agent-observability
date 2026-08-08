# Auto-sync and Per-Project Spaces Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Push each Claude Code session to AnonaMemory automatically when the session ends, with each project routed to its own space and tracked by its own watermark.

**Architecture:** The config becomes a list of project entries, each carrying its own `space_id`, `watermark`, and dedup set. `Sync` is re-scoped from "one credential" to "one project", with a `SyncAll` wrapper. A hidden `agentobs memory hook` command reads Claude Code's Stop-hook payload from stdin, resolves the session's `cwd` to a project entry, and re-execs itself detached so the agent never waits on network I/O.

**Tech Stack:** Go 1.25, `spf13/cobra`, `AlecAivazis/survey/v2`, `gofrs/flock` (already a dependency, used by `cursorhook`). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-08-memory-autosync-per-project-design.md`

## Global Constraints

- Module path is `github.com/anonalabs/agent-observability/cli`. All commands run from `cli/`.
- No new third-party dependencies. `cli/go.mod` and `cli/go.sum` must not change. `github.com/gofrs/flock` is already required and may be used.
- Tests use stdlib `testing` only, table-driven where there are multiple cases. No test framework, no assertion library.
- Tests must not make real network calls and must not read the real `~/.claude` or `~/.config/agentobs`. Use `t.TempDir()` and `t.Setenv`.
- `~/.config/agentobs/memory.json` is written mode `0600`, its parent `0700`, and both are re-asserted on every save.
- Any file this tool writes under a user's home is backed up to `<name>.bak` before being rewritten, and merged rather than replaced.
- The Stop hook must always exit 0 and must never write to stdout or stderr. A broken connector must never break the agent.
- Conversation text must never be written to ClickHouse, Prometheus, or any log line.
- Commit messages must not mention Claude, Anthropic, or AI authorship, and must not carry a `Co-Authored-By` trailer.
- Commit after every task.

## File Structure

**Create:**

| Path | Responsibility |
|---|---|
| `cli/internal/memory/hook.go` | Stop-hook payload parsing, detached re-exec, per-project lock, log file |
| `cli/internal/memory/hook_test.go` | payload parsing, no-match routing, lock contention |
| `cli/internal/agents/claudecode.go` | merge the Stop hook into `~/.claude/settings.json` |
| `cli/internal/agents/claudecode_test.go` | merge preserves unrelated hooks; re-running does not duplicate |

**Modify:**

| Path | Change |
|---|---|
| `cli/internal/memory/credentials.go` | `Project` type, config v2, `migrateV1`, `ProjectFor`; move `AllowsPath`/`Seen`/`MarkSynced` onto `*Project` |
| `cli/internal/memory/credentials_test.go` | migration, routing, per-project watermark isolation |
| `cli/internal/memory/sync.go` | `Sync` → `SyncProject`, add `SyncAll` |
| `cli/internal/memory/sync_test.go` | update to per-project; `SyncAll` continues past one failure |
| `cli/internal/memory/wizard.go` | build a v2 config; offer Stop-hook registration |
| `cli/internal/commands/memory.go` | `hook` subcommand, `sync --project`, `projects` subcommand |
| `README.md` | rewrite the connector section |
| `docs/anonamemory.md` | per-project config, auto-sync, incremental vs `--since` |

---

### Task 1: Config v2 — project entries and migration

**Files:**
- Modify: `cli/internal/memory/credentials.go`
- Test: `cli/internal/memory/credentials_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `type Project struct { Path, SpaceID string; Watermark time.Time; RecentTurns map[string]time.Time }`
  - `Credentials` gains `Version int` and `Projects []Project`; drops top-level `SpaceID`, `SpaceName`, `Watermark`, `RecentTurns`
  - `func (c *Credentials) ProjectFor(cwd string) *Project`
  - `func (p *Project) AllowsPath(path string) bool`
  - `func (p *Project) Seen(turnID string) bool`
  - `func (p *Project) MarkSynced(ids map[string]time.Time)`
  - `const configVersion = 2`

- [ ] **Step 1: Write the failing tests**

Add to `cli/internal/memory/credentials_test.go`:

```go
func TestLoadCredentialsMigratesV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	t.Setenv("AGENTOBS_MEMORY_CONFIG", path)

	v1 := `{
	  "api_key": "anona_live_x",
	  "space_id": "old-space",
	  "space_name": "old-space",
	  "projects": ["/home/dev/repo", "/home/dev/other"],
	  "watermark": "2026-08-06T12:00:00Z",
	  "recent_turns": {"turn-1": "2026-08-06T11:00:00Z"}
	}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := LoadCredentials()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Version != configVersion {
		t.Errorf("version = %d, want %d", got.Version, configVersion)
	}
	if got.APIKey != "anona_live_x" {
		t.Errorf("api_key = %q", got.APIKey)
	}
	if len(got.Projects) != 2 {
		t.Fatalf("got %d projects, want 2: %+v", got.Projects, got.Projects)
	}

	want := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	for _, p := range got.Projects {
		if p.SpaceID != "old-space" {
			t.Errorf("%s space_id = %q, want old-space", p.Path, p.SpaceID)
		}
		if !p.Watermark.Equal(want) {
			t.Errorf("%s watermark = %v, want %v", p.Path, p.Watermark, want)
		}
		if !p.Seen("turn-1") {
			t.Errorf("%s should carry the v1 dedup set", p.Path)
		}
	}
}

func TestLoadCredentialsBacksUpBeforeMigrating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	t.Setenv("AGENTOBS_MEMORY_CONFIG", path)

	v1 := `{"api_key":"k","space_id":"s","projects":["/a"],"watermark":"2026-08-06T12:00:00Z"}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := LoadCredentials(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("expected a backup before migrating a file holding an API key: %v", err)
	}
	if string(backup) != v1 {
		t.Errorf("backup = %q, want the original v1 content", string(backup))
	}
}

func TestLoadCredentialsLeavesV2Alone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	t.Setenv("AGENTOBS_MEMORY_CONFIG", path)

	when := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	if err := SaveCredentials(&Credentials{
		Version: configVersion,
		APIKey:  "k",
		Projects: []Project{
			{Path: "/home/dev/repo", SpaceID: "repo-space", Watermark: when},
		},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := LoadCredentials()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Projects) != 1 || got.Projects[0].SpaceID != "repo-space" {
		t.Fatalf("projects = %+v", got.Projects)
	}
	if !got.Projects[0].Watermark.Equal(when) {
		t.Errorf("watermark = %v, want %v", got.Projects[0].Watermark, when)
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("a v2 config must not be backed up -- nothing was migrated")
	}
}

func TestProjectForLongestPrefixWins(t *testing.T) {
	c := &Credentials{Projects: []Project{
		{Path: "/home/dev/repo", SpaceID: "outer"},
		{Path: "/home/dev/repo/.worktrees/feature", SpaceID: "inner"},
	}}

	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{"exact outer", "/home/dev/repo", "outer"},
		{"nested under outer", "/home/dev/repo/cli", "outer"},
		{"exact inner", "/home/dev/repo/.worktrees/feature", "inner"},
		{"nested under inner picks the longer prefix", "/home/dev/repo/.worktrees/feature/cli", "inner"},
		{"unrelated path", "/home/dev/elsewhere", ""},
		{"sibling sharing a prefix", "/home/dev/repo-two", ""},
		{"empty cwd", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := c.ProjectFor(tt.cwd)
			got := ""
			if p != nil {
				got = p.SpaceID
			}
			if got != tt.want {
				t.Errorf("ProjectFor(%q) = %q, want %q", tt.cwd, got, tt.want)
			}
		})
	}
}

func TestProjectWatermarksAreIndependent(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	c := &Credentials{Projects: []Project{
		{Path: "/a", SpaceID: "a"},
		{Path: "/b", SpaceID: "b"},
	}}

	c.Projects[0].MarkSynced(map[string]time.Time{"t1": now})

	if !c.Projects[0].Watermark.Equal(now) {
		t.Errorf("project a watermark = %v, want %v", c.Projects[0].Watermark, now)
	}
	if !c.Projects[1].Watermark.IsZero() {
		t.Errorf("project b watermark = %v, want it untouched", c.Projects[1].Watermark)
	}
	if c.Projects[1].Seen("t1") {
		t.Error("project b must not see project a's synced turns")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cli && go test ./internal/memory/ -run 'Migrat|ProjectFor|ProjectWatermarks|LeavesV2|BacksUp' -v`
Expected: FAIL — `undefined: Project`, `undefined: configVersion`.

- [ ] **Step 3: Write the implementation**

In `cli/internal/memory/credentials.go`, replace the `Credentials` struct and the `AllowsPath`/`Seen`/`MarkSynced` methods with:

```go
// configVersion is the current on-disk schema. A file without it (or with
// a lower number) is migrated on load.
const configVersion = 2

// Project is one directory tree and the space its turns are recorded into.
// Each carries its own watermark and dedup set: a shared watermark would let
// one project's sync skip another project's turns, since advancing it past a
// timestamp hides everything older from every source.
type Project struct {
	// Path is an absolute directory. A turn is pushed only if its working
	// directory is this path or nested under it.
	Path        string               `json:"path"`
	SpaceID     string               `json:"space_id"`
	Watermark   time.Time            `json:"watermark"`
	RecentTurns map[string]time.Time `json:"recent_turns,omitempty"`
}

// Credentials is the on-disk state at ~/.config/agentobs/memory.json.
// It holds an API key, so it is always written 0600.
type Credentials struct {
	Version int    `json:"version"`
	APIKey  string `json:"api_key"`
	// BaseURL overrides DefaultBaseURL, for staging or the legacy host.
	BaseURL string `json:"base_url,omitempty"`
	// Projects is deny-by-default: a turn whose working directory matches
	// no entry is never pushed. An empty list pushes nothing.
	Projects []Project `json:"projects"`
}

// ProjectFor returns the entry whose Path is the longest prefix of cwd, or
// nil when none matches. Longest-prefix matters for nested projects: a
// worktree inside a parent repo routes to its own space when it has an entry.
func (c *Credentials) ProjectFor(cwd string) *Project {
	if cwd == "" {
		return nil
	}
	var best *Project
	for i := range c.Projects {
		if !c.Projects[i].AllowsPath(cwd) {
			continue
		}
		if best == nil || len(c.Projects[i].Path) > len(best.Path) {
			best = &c.Projects[i]
		}
	}
	return best
}

// AllowsPath reports whether p sits inside this project. It compares cleaned
// paths component-wise so /repo-two doesn't match /repo.
func (p *Project) AllowsPath(path string) bool {
	if path == "" || p.Path == "" {
		return false
	}
	target := filepath.Clean(path)
	root := filepath.Clean(p.Path)
	if target == root {
		return true
	}
	return strings.HasPrefix(target, root+string(filepath.Separator))
}

func (p *Project) Seen(turnID string) bool {
	_, ok := p.RecentTurns[turnID]
	return ok
}

// MarkSynced records the given turn ids, advances this project's watermark to
// the newest of them (never backwards), and prunes ids older than the dedup
// window.
func (p *Project) MarkSynced(ids map[string]time.Time) {
	if p.RecentTurns == nil {
		p.RecentTurns = map[string]time.Time{}
	}
	for id, ts := range ids {
		p.RecentTurns[id] = ts
		if ts.After(p.Watermark) {
			p.Watermark = ts
		}
	}

	cutoff := p.Watermark.Add(-dedupWindow)
	for id, ts := range p.RecentTurns {
		if ts.Before(cutoff) {
			delete(p.RecentTurns, id)
		}
	}
}
```

Add the v1 shape and migration:

```go
// credentialsV1 is the original flat schema: one space, one watermark, and
// projects as bare path strings.
type credentialsV1 struct {
	APIKey      string               `json:"api_key"`
	SpaceID     string               `json:"space_id"`
	SpaceName   string               `json:"space_name"`
	BaseURL     string               `json:"base_url"`
	Projects    []string             `json:"projects"`
	Watermark   time.Time            `json:"watermark"`
	RecentTurns map[string]time.Time `json:"recent_turns"`
}

// migrateV1 turns the flat schema into per-project entries, giving every old
// path the old space, watermark, and dedup set. Carrying the watermark
// forward matters: dropping it would re-push the user's entire history, and
// raising it would silently skip turns.
func migrateV1(data []byte) (*Credentials, error) {
	var old credentialsV1
	if err := json.Unmarshal(data, &old); err != nil {
		return nil, err
	}

	creds := &Credentials{
		Version: configVersion,
		APIKey:  old.APIKey,
		BaseURL: old.BaseURL,
	}
	for _, path := range old.Projects {
		recent := make(map[string]time.Time, len(old.RecentTurns))
		for id, ts := range old.RecentTurns {
			recent[id] = ts
		}
		creds.Projects = append(creds.Projects, Project{
			Path:        path,
			SpaceID:     old.SpaceID,
			Watermark:   old.Watermark,
			RecentTurns: recent,
		})
	}
	return creds, nil
}
```

Replace the body of `LoadCredentials` with:

```go
func LoadCredentials() (*Credentials, error) {
	path, err := CredentialsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, err
	}

	if probe.Version < configVersion {
		// Back up before rewriting: this file holds an API key, and a
		// botched migration would cost the user their credential.
		if err := os.WriteFile(path+".bak", data, 0o600); err != nil {
			return nil, err
		}
		creds, err := migrateV1(data)
		if err != nil {
			return nil, err
		}
		if err := SaveCredentials(creds); err != nil {
			return nil, err
		}
		return creds, nil
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	for i := range creds.Projects {
		if creds.Projects[i].RecentTurns == nil {
			creds.Projects[i].RecentTurns = map[string]time.Time{}
		}
	}
	return &creds, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cli && go build ./... && go test ./internal/memory/ -run 'Migrat|ProjectFor|ProjectWatermarks|LeavesV2|BacksUp' -v`
Expected: PASS. Other tests in the package will not compile yet — Tasks 2 and 4 update their call sites. That is expected at this step.

- [ ] **Step 5: Commit**

```bash
git add cli/internal/memory/credentials.go cli/internal/memory/credentials_test.go
git commit -m "Give each project its own space and watermark

A single watermark gated every project at once, so syncing one could
advance past another's unsynced turns and hide them permanently. v1
configs migrate on load, backed up first since the file holds an API key."
```

---

### Task 2: Sync per project

**Files:**
- Modify: `cli/internal/memory/sync.go`
- Test: `cli/internal/memory/sync_test.go`

**Interfaces:**
- Consumes: `Project`, `ProjectFor` from Task 1.
- Produces:
  - `func SyncProject(p *Project, rec Recorder, sources []TranscriptSource, enricher Enricher, opts Options) (Result, error)`
  - `func SyncAll(creds *Credentials, rec Recorder, sources []TranscriptSource, enricher Enricher, opts Options) (map[string]Result, error)` — keyed by project path; the error is non-nil only if every project failed

- [ ] **Step 1: Write the failing tests**

Replace the `Sync` call sites in `cli/internal/memory/sync_test.go` — every `Sync(creds, ...)` becomes `SyncProject(&creds.Projects[0], ...)`, and each existing test's `creds` becomes:

```go
creds := &Credentials{Version: configVersion, Projects: []Project{
	{Path: "/home/dev/repo", SpaceID: "spc_a1"},
}}
```

Then add:

```go
func TestSyncAllContinuesPastOneProjectFailure(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	creds := &Credentials{Version: configVersion, Projects: []Project{
		{Path: "/home/dev/good", SpaceID: "good"},
		{Path: "/home/dev/bad", SpaceID: "bad"},
	}}

	rec := &spaceAwareRecorder{failSpace: "bad"}
	src := fakeSource{turns: []Turn{
		turnAt("g1", "/home/dev/good", now),
		turnAt("b1", "/home/dev/bad", now),
	}}

	results, err := SyncAll(creds, rec, []TranscriptSource{src}, fakeEnricher{}, Options{})
	if err != nil {
		t.Fatalf("one project failing must not fail the whole run: %v", err)
	}

	if results["/home/dev/good"].Pushed != 1 {
		t.Errorf("good pushed = %d, want 1", results["/home/dev/good"].Pushed)
	}
	if creds.Projects[0].Watermark.IsZero() {
		t.Error("the succeeding project's watermark should have advanced")
	}
	if !creds.Projects[1].Watermark.IsZero() {
		t.Error("the failing project's watermark must not advance")
	}
}

// spaceAwareRecorder fails only for a named space, so one project can error
// while another succeeds in the same SyncAll run.
type spaceAwareRecorder struct {
	failSpace string
	bySpace   map[string][]RecordItem
}

func (r *spaceAwareRecorder) RecordBatch(spaceID string, items []RecordItem) (int, error) {
	if spaceID == r.failSpace {
		return 0, errors.New("boom")
	}
	if r.bySpace == nil {
		r.bySpace = map[string][]RecordItem{}
	}
	r.bySpace[spaceID] = append(r.bySpace[spaceID], items...)
	return len(items), nil
}

func TestSyncProjectOnlyTakesItsOwnTurns(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	p := &Project{Path: "/home/dev/repo", SpaceID: "spc_a1"}
	rec := &fakeRecorder{}
	src := fakeSource{turns: []Turn{
		turnAt("mine", "/home/dev/repo/cli", now),
		turnAt("theirs", "/home/dev/other", now),
	}}

	result, err := SyncProject(p, rec, []TranscriptSource{src}, fakeEnricher{}, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Pushed != 1 || result.Filtered != 1 {
		t.Errorf("pushed = %d, filtered = %d, want 1/1", result.Pushed, result.Filtered)
	}
	if rec.items[0].Metadata["turn_id"] != "mine" {
		t.Errorf("pushed turn_id = %v, want mine", rec.items[0].Metadata["turn_id"])
	}
}
```

Add `"errors"` to the test file's imports if it is not already there.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cli && go test ./internal/memory/ -run 'SyncAll|SyncProjectOnly' -v`
Expected: FAIL — `undefined: SyncProject`, `undefined: SyncAll`.

- [ ] **Step 3: Write the implementation**

In `cli/internal/memory/sync.go`, rename `Sync` to `SyncProject` and change its signature and the four places it touched `creds`:

```go
func SyncProject(p *Project, rec Recorder, sources []TranscriptSource, enricher Enricher, opts Options) (Result, error) {
```

Inside, replace:
- `since := creds.Watermark` with `since := p.Watermark`
- `if !creds.AllowsPath(turn.CWD)` with `if !p.AllowsPath(turn.CWD)`
- `if creds.Seen(turn.TurnID)` with `if p.Seen(turn.TurnID)`
- both `creds.MarkSynced(...)` calls with `p.MarkSynced(...)`
- `rec.RecordBatch(creds.SpaceID, items)` with `rec.RecordBatch(p.SpaceID, items)`

Everything else in the function — the lookback comment and logic, the sort, the accepted-prefix handling — stays exactly as written.

Then append:

```go
// SyncAll syncs every configured project independently. One project's
// failure is recorded against that project and leaves its watermark
// untouched; the others still run. The returned error is non-nil only when
// every project failed, so a caller can distinguish "nothing worked" from
// "one space is having a bad day".
func SyncAll(creds *Credentials, rec Recorder, sources []TranscriptSource, enricher Enricher, opts Options) (map[string]Result, error) {
	results := make(map[string]Result, len(creds.Projects))
	failures := 0
	var lastErr error

	for i := range creds.Projects {
		p := &creds.Projects[i]
		result, err := SyncProject(p, rec, sources, enricher, opts)
		if err != nil {
			failures++
			lastErr = err
			result.SyncErr = err
		}
		results[p.Path] = result
	}

	if failures > 0 && failures == len(creds.Projects) {
		return results, lastErr
	}
	return results, nil
}
```

Add `SyncErr` to `Result`:

```go
	// SyncErr is set by SyncAll when this project's own sync failed, so a
	// caller iterating results can report which spaces are broken without
	// the whole run being an error.
	SyncErr error
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cli && go build ./... && go test ./internal/memory/ -v && go vet ./internal/memory/`
Expected: all PASS, vet clean. `internal/commands` will not compile until Task 4; that is expected.

- [ ] **Step 5: Commit**

```bash
git add cli/internal/memory/sync.go cli/internal/memory/sync_test.go
git commit -m "Sync each project independently

SyncProject scopes a run to one project's space, allowlist and watermark;
SyncAll iterates them so one space failing doesn't stop the rest."
```

---

### Task 3: Stop-hook handling

**Files:**
- Create: `cli/internal/memory/hook.go`
- Test: `cli/internal/memory/hook_test.go`

**Interfaces:**
- Consumes: `Credentials`, `ProjectFor`, `SyncProject` from Tasks 1-2.
- Produces:
  - `type HookPayload struct { SessionID, TranscriptPath, CWD string }`
  - `func ParseHookPayload(r io.Reader) (HookPayload, error)`
  - `func HookLogPath() (string, error)`
  - `func LogHook(format string, args ...interface{})`
  - `func ProjectLock(p *Project) (*flock.Flock, error)`

Claude Code's Stop hook delivers JSON on stdin with `session_id`, `transcript_path`, and `cwd`.

- [ ] **Step 1: Write the failing tests**

Create `cli/internal/memory/hook_test.go`:

```go
package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseHookPayload(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantCWD string
		wantErr bool
	}{
		{
			"well formed",
			`{"session_id":"s1","transcript_path":"/home/dev/.claude/projects/x/s1.jsonl","cwd":"/home/dev/repo"}`,
			"/home/dev/repo",
			false,
		},
		{
			"extra fields are ignored",
			`{"session_id":"s1","cwd":"/home/dev/repo","hook_event_name":"Stop","unknown":123}`,
			"/home/dev/repo",
			false,
		},
		{"malformed json", `{not json`, "", true},
		{"empty input", ``, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseHookPayload(strings.NewReader(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.CWD != tt.wantCWD {
				t.Errorf("cwd = %q, want %q", got.CWD, tt.wantCWD)
			}
		})
	}
}

func TestLogHookWritesToConfigDirAndCaps(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTOBS_MEMORY_CONFIG", filepath.Join(dir, "memory.json"))

	LogHook("first entry %d", 1)
	LogHook("second entry")

	path, err := HookLogPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(data), "first entry 1") || !strings.Contains(string(data), "second entry") {
		t.Errorf("log = %q, want both entries", string(data))
	}
}

func TestProjectLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTOBS_MEMORY_CONFIG", filepath.Join(dir, "memory.json"))

	p := &Project{Path: "/home/dev/repo", SpaceID: "repo"}

	first, err := ProjectLock(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first == nil {
		t.Fatal("expected to acquire the lock")
	}
	defer first.Unlock()

	second, err := ProjectLock(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if second != nil {
		t.Error("a second lock on the same project must not be granted")
		second.Unlock()
	}
}

func TestProjectLockDistinctPerProject(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTOBS_MEMORY_CONFIG", filepath.Join(dir, "memory.json"))

	a, err := ProjectLock(&Project{Path: "/home/dev/a", SpaceID: "a"})
	if err != nil || a == nil {
		t.Fatalf("expected lock on a: %v", err)
	}
	defer a.Unlock()

	b, err := ProjectLock(&Project{Path: "/home/dev/b", SpaceID: "b"})
	if err != nil || b == nil {
		t.Fatalf("a different project must be lockable independently: %v", err)
	}
	b.Unlock()
}

func TestHookLogIsTruncatedWhenOversized(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTOBS_MEMORY_CONFIG", filepath.Join(dir, "memory.json"))

	path, err := HookLogPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(path, make([]byte, maxHookLogBytes+1024), 0o600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	LogHook("after truncation %v", time.Now().Year())

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Size() > maxHookLogBytes {
		t.Errorf("log size = %d, want <= %d", info.Size(), maxHookLogBytes)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "after truncation") {
		t.Error("the newest entry must survive truncation")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cli && go test ./internal/memory/ -run 'HookPayload|LogHook|ProjectLock|HookLogIs' -v`
Expected: FAIL — `undefined: ParseHookPayload`.

- [ ] **Step 3: Write the implementation**

Create `cli/internal/memory/hook.go`:

```go
package memory

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// maxHookLogBytes caps the hook's log. The hook runs unattended on every
// session end, so an uncapped log would grow without anyone noticing.
const maxHookLogBytes = 1 << 20 // 1 MiB

// HookPayload is the subset of Claude Code's Stop-hook stdin this needs.
type HookPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
}

func ParseHookPayload(r io.Reader) (HookPayload, error) {
	var p HookPayload
	data, err := io.ReadAll(r)
	if err != nil {
		return p, err
	}
	if len(data) == 0 {
		return p, fmt.Errorf("empty hook payload")
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("parsing hook payload: %w", err)
	}
	return p, nil
}

// configDir is where the hook keeps its log and lock files -- alongside the
// config, so AGENTOBS_MEMORY_CONFIG relocates all of it together in tests.
func configDir() (string, error) {
	path, err := CredentialsPath()
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}

func HookLogPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sync.log"), nil
}

// LogHook appends a timestamped line to the hook log. It never writes to
// stdout or stderr: Claude Code reads a Stop hook's output, and a broken
// connector must stay invisible to the agent. Every failure here is
// swallowed for the same reason.
func LogHook(format string, args ...interface{}) {
	path, err := HookLogPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}

	if info, err := os.Stat(path); err == nil && info.Size() > maxHookLogBytes {
		truncateLogFront(path)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// truncateLogFront keeps the newest half of an oversized log, so recent
// entries survive while the file stops growing.
func truncateLogFront(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	keep := data[len(data)/2:]
	if i := indexByte(keep, '\n'); i >= 0 {
		keep = keep[i+1:]
	}
	_ = os.WriteFile(path, keep, 0o600)
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// ProjectLock takes an exclusive, non-blocking lock for one project.
// It returns (nil, nil) when another process already holds it -- two
// concurrent syncs of the same project would double-push, since the batch
// endpoint has no idempotency key. Waiting is wrong here: the next session
// end will sync anyway, so the right move is to skip this run.
func ProjectLock(p *Project) (*flock.Flock, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	name := fmt.Sprintf("sync-%x.lock", sha256Hex(p.Path))
	lock := flock.New(filepath.Join(dir, name))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return lock, nil
}
```

Add `sha256Hex` next to it (the project path is user-supplied and must not be used raw as a filename):

```go
// sha256Hex hashes a project path for use in a lock filename -- paths
// contain separators and arbitrary characters that a filename cannot.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
```

and add `"crypto/sha256"` and `"encoding/hex"` to the imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cli && go test ./internal/memory/ -v && go vet ./internal/memory/`
Expected: all PASS, vet clean.

- [ ] **Step 5: Commit**

```bash
git add cli/internal/memory/hook.go cli/internal/memory/hook_test.go
git commit -m "Add Stop-hook payload parsing, per-project lock and capped log

The hook runs unattended on every session end, so it logs to a capped
file rather than stdout and takes a non-blocking per-project lock -- two
concurrent syncs would double-push against an endpoint with no
idempotency key."
```

---

### Task 4: `memory hook`, `sync --project`, and `memory projects`

**Files:**
- Modify: `cli/internal/commands/memory.go`
- Modify: `cli/internal/memory/wizard.go`

**Interfaces:**
- Consumes: everything from Tasks 1-3.
- Produces: no new exported Go API; three CLI surfaces.

No unit tests — this is command wiring over tested units, matching `install`, `status`, `export`, and `agents`, none of which have tests. Verification is by running the binary.

- [ ] **Step 1: Update the existing command wiring**

In `cli/internal/commands/memory.go`, every `memory.Sync(creds, ...)` call becomes a `SyncAll` call, and the summary loop iterates results. Replace `memorySyncCmd`'s sync block with:

```go
			results, syncErr := memory.SyncAll(
				creds,
				newClient(creds),
				[]memory.TranscriptSource{source},
				clickhouse,
				opts,
			)

			if !dryRun {
				if err := memory.SaveCredentials(creds); err != nil {
					return err
				}
			}

			if !quiet {
				for path, result := range results {
					reportResult(cmd, path, result, dryRun)
				}
			}
			return syncErr
```

Add the shared reporter, preserving the existing routing rules — routine counts to stdout under `!quiet`, degradations to stderr always:

```go
// reportResult prints one project's outcome. Routine bookkeeping goes to
// stdout and is suppressed by --quiet; degradations go to stderr regardless,
// because the cron line the wizard prints uses --quiet and a silently
// degraded sync is worse than a noisy one.
func reportResult(cmd *cobra.Command, path string, result memory.Result, dryRun bool) {
	verb := "Pushed"
	if dryRun {
		verb = "Would push"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s: %s %d turns.\n", path, verb, result.Pushed)
	if result.Deduped > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "%s: skipped %d already-synced turns.\n", path, result.Deduped)
	}
	if result.Filtered > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "%s: skipped %d turns outside this project.\n", path, result.Filtered)
	}
	if result.SkippedFiles > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: skipped %d unreadable transcript files.\n", path, result.SkippedFiles)
	}
	if result.SkippedRows > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: ClickHouse returned %d rows that could not be read.\n", path, result.SkippedRows)
	}
	if result.SyncErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: sync failed: %v\n", path, result.SyncErr)
	}
}
```

- [ ] **Step 2: Add `--project` to `sync`**

Add the flag and, when set, sync only that entry:

```go
	cmd.Flags().StringVar(&project, "project", "", "sync only this configured project path")
```

and before the `SyncAll` call:

```go
			if project != "" {
				p := creds.ProjectFor(project)
				if p == nil {
					return fmt.Errorf("no configured project matches %q -- run `agentobs memory projects` to see them", project)
				}
				result, err := memory.SyncProject(p, newClient(creds), []memory.TranscriptSource{source}, clickhouse, opts)
				if !dryRun {
					if saveErr := memory.SaveCredentials(creds); saveErr != nil {
						return saveErr
					}
				}
				if !quiet {
					reportResult(cmd, p.Path, result, dryRun)
				}
				return err
			}
```

- [ ] **Step 2b: Update `memoryStatusCmd` for per-project config**

`memoryStatusCmd` also calls the old `Sync` (with `DryRun: true`) to compute a pending count, and prints a single `space` line. Replace its body's config summary and pending computation with a per-project view:

```go
			fmt.Fprintf(cmd.OutOrStdout(), "%-40s %-20s %-24s %s\n", "PROJECT", "SPACE", "LAST SYNC", "PENDING")

			source, err := memory.NewClaudeCodeSource()
			if err != nil {
				return err
			}
			results, _ := memory.SyncAll(
				creds,
				newClient(creds),
				[]memory.TranscriptSource{source},
				memory.NewClickHouse(),
				memory.Options{DryRun: true},
			)

			degraded := false
			for _, p := range creds.Projects {
				last := "never"
				if !p.Watermark.IsZero() {
					last = p.Watermark.Format(time.RFC3339)
				}
				r := results[p.Path]
				if r.EnrichErr != nil || r.PromptOnlyErr != nil {
					degraded = true
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-40s %-20s %-24s %d\n", p.Path, p.SpaceID, last, r.Pushed)
			}

			if degraded {
				fmt.Fprintln(cmd.ErrOrStderr())
				fmt.Fprintln(cmd.ErrOrStderr(), "ClickHouse is unreachable: pending counts above are from transcripts only, and prompt-only agents (Cursor, Copilot, Codex, OpenCode) contribute nothing.")
			}
			return nil
```

A dry-run `SyncAll` mutates nothing — `SyncProject` returns before `MarkSynced` when `opts.DryRun` is set — so `status` stays read-only.

- [ ] **Step 3: Add the `hook` and `projects` subcommands**

```go
// memoryHookCmd is invoked by Claude Code's Stop hook. It must always exit 0
// and never write to stdout or stderr: Claude Code reads a hook's output, and
// an optional connector must never be able to break the agent.
func memoryHookCmd() *cobra.Command {
	var detached bool

	cmd := &cobra.Command{
		Use:    "hook",
		Short:  "Internal: invoked by Claude Code's Stop hook to sync the finished session",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := memory.ParseHookPayload(cmd.InOrStdin())
			if err != nil {
				memory.LogHook("bad payload: %v", err)
				return nil
			}

			creds, err := memory.LoadCredentials()
			if err != nil || creds == nil {
				memory.LogHook("no usable config (%v) -- nothing to sync", err)
				return nil
			}

			p := creds.ProjectFor(payload.CWD)
			if p == nil {
				memory.LogHook("cwd %s matches no configured project", payload.CWD)
				return nil
			}

			if !detached {
				// Claude Code waits for the hook to exit. A sync is network
				// I/O against AnonaMemory, so doing it inline would delay the
				// end of every session. Re-exec detached and return at once.
				if err := respawnDetached(payload); err != nil {
					memory.LogHook("could not detach: %v", err)
				}
				return nil
			}

			lock, err := memory.ProjectLock(p)
			if err != nil {
				memory.LogHook("lock error for %s: %v", p.Path, err)
				return nil
			}
			if lock == nil {
				memory.LogHook("%s already syncing -- skipping this run", p.Path)
				return nil
			}
			defer lock.Unlock()

			source, err := memory.NewClaudeCodeSource()
			if err != nil {
				memory.LogHook("transcript source: %v", err)
				return nil
			}

			result, syncErr := memory.SyncProject(p, newClient(creds), []memory.TranscriptSource{source}, memory.NewClickHouse(), memory.Options{})
			if saveErr := memory.SaveCredentials(creds); saveErr != nil {
				memory.LogHook("saving config: %v", saveErr)
			}
			if syncErr != nil {
				memory.LogHook("%s sync failed after %d turns: %v", p.Path, result.Pushed, syncErr)
				return nil
			}
			memory.LogHook("%s pushed %d turns (session %s)", p.Path, result.Pushed, payload.SessionID)
			return nil
		},
	}

	cmd.Flags().BoolVar(&detached, "detached", false, "internal: the re-exec'd child that performs the sync")
	return cmd
}

// respawnDetached re-runs this command with --detached, handing the payload
// on the child's stdin, then returns without waiting.
func respawnDetached(payload memory.HookPayload) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	child := exec.Command(exe, "memory", "hook", "--detached")
	child.Stdin = bytes.NewReader(encoded)
	child.Stdout = nil
	child.Stderr = nil
	child.SysProcAttr = detachedSysProcAttr()
	if err := child.Start(); err != nil {
		return err
	}
	// Not waited on deliberately: the child outlives this process so Claude
	// Code is never blocked on the sync.
	return child.Process.Release()
}

func memoryProjectsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "projects",
		Short: "List configured projects, their spaces, and when each last synced",
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := requireCredentials()
			if err != nil {
				return err
			}
			if len(creds.Projects) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No projects configured. Run `agentobs connect` to add one.")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%-50s %-24s %s\n", "PROJECT", "SPACE", "LAST SYNC")
			for _, p := range creds.Projects {
				last := "never"
				if !p.Watermark.IsZero() {
					last = p.Watermark.Format(time.RFC3339)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-50s %-24s %s\n", p.Path, p.SpaceID, last)
			}
			return nil
		},
	}
}
```

Register both in `MemoryCmd()`:

```go
	memoryCmd.AddCommand(memoryHookCmd())
	memoryCmd.AddCommand(memoryProjectsCmd())
```

Add `bytes`, `encoding/json`, `os/exec`, and `time` to the file's imports.

- [ ] **Step 4: Add the platform-specific detach attribute**

Create `cli/internal/commands/detach_unix.go`:

```go
//go:build !windows

package commands

import "syscall"

// detachedSysProcAttr puts the child in its own session so it is reparented
// to init and survives the hook process exiting.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
```

Create `cli/internal/commands/detach_windows.go`:

```go
//go:build windows

package commands

import "syscall"

// detachedSysProcAttr has no session concept to use on Windows; the child is
// simply started without inherited handles.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
```

- [ ] **Step 5: Update the wizard to build a v2 config**

In `cli/internal/memory/wizard.go`, replace the `Credentials` construction with:

```go
	creds := &Credentials{
		Version: configVersion,
		APIKey:  apiKey,
	}
	for _, path := range projects {
		creds.Projects = append(creds.Projects, Project{
			Path:        path,
			SpaceID:     spaceID,
			RecentTurns: map[string]time.Time{},
		})
	}
```

and replace the first-sync call with `SyncAll(creds, client, []TranscriptSource{source}, NewClickHouse(), Options{})`, summing `Pushed` across the returned results for the message it prints.

- [ ] **Step 6: Verify by building and running**

Run:
```bash
cd cli && go build -o agentobs ./cmd/agentobs && go test ./... && go vet ./... && gofmt -l .
./agentobs memory --help
./agentobs memory projects
echo '{"session_id":"s1","cwd":"/nonexistent"}' | ./agentobs memory hook; echo "exit=$?"
```
Expected: build and tests clean; `memory --help` lists `hook` as hidden (absent from the list) plus `projects`, `sync`, `status`, `disconnect`; the hook invocation prints nothing and exits 0.

- [ ] **Step 7: Commit**

```bash
git add cli/internal/commands/memory.go cli/internal/commands/detach_unix.go cli/internal/commands/detach_windows.go cli/internal/memory/wizard.go
git commit -m "Add memory hook, sync --project and memory projects

The hook re-execs itself detached so Claude Code never waits on network
I/O at session end, and exits 0 on every path so an optional connector
cannot break the agent."
```

---

### Task 5: Register the Stop hook in Claude Code's settings

**Files:**
- Create: `cli/internal/agents/claudecode.go`
- Test: `cli/internal/agents/claudecode_test.go`
- Modify: `cli/internal/memory/wizard.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `func ClaudeSettingsPath() (string, error)`
  - `func StopHookCommand() string`
  - `func MergeStopHook(existing map[string]interface{}) map[string]interface{}`
  - `func RegisterStopHook() (string, error)` — writes the merged settings, returns the path

Claude Code's `~/.claude/settings.json` holds hooks as `{"hooks":{"<Event>":[{"matcher":"...","hooks":[{"type":"command","command":"..."}]}]}}`. `Stop` entries carry no matcher. Real installs have unrelated hooks in this file that must survive.

- [ ] **Step 1: Write the failing tests**

Create `cli/internal/agents/claudecode_test.go`:

```go
package agents

import (
	"encoding/json"
	"testing"
)

func TestMergeStopHookPreservesUnrelatedHooks(t *testing.T) {
	existing := map[string]interface{}{}
	raw := `{
	  "model": "opus",
	  "hooks": {
	    "PreToolUse": [{"matcher":"Bash","hooks":[{"type":"command","command":"other-tool hook"}]}]
	  }
	}`
	if err := json.Unmarshal([]byte(raw), &existing); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	merged := MergeStopHook(existing)

	if merged["model"] != "opus" {
		t.Errorf("unrelated top-level keys must survive: %+v", merged)
	}
	hooks, ok := merged["hooks"].(map[string]interface{})
	if !ok {
		t.Fatalf("hooks = %T, want a map", merged["hooks"])
	}
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Error("an unrelated hook event must not be dropped")
	}
	stop, ok := hooks["Stop"].([]interface{})
	if !ok || len(stop) != 1 {
		t.Fatalf("Stop = %#v, want one entry", hooks["Stop"])
	}
}

func TestMergeStopHookIsIdempotent(t *testing.T) {
	merged := MergeStopHook(map[string]interface{}{})
	again := MergeStopHook(merged)

	hooks := again["hooks"].(map[string]interface{})
	stop := hooks["Stop"].([]interface{})
	if len(stop) != 1 {
		t.Errorf("Stop has %d entries after a second merge, want 1", len(stop))
	}
}

func TestMergeStopHookKeepsForeignStopEntries(t *testing.T) {
	existing := map[string]interface{}{}
	raw := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"someone-elses-tool"}]}]}}`
	if err := json.Unmarshal([]byte(raw), &existing); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	merged := MergeStopHook(existing)

	hooks := merged["hooks"].(map[string]interface{})
	stop := hooks["Stop"].([]interface{})
	if len(stop) != 2 {
		t.Fatalf("Stop has %d entries, want the foreign one plus ours", len(stop))
	}
	found := false
	for _, entry := range stop {
		m := entry.(map[string]interface{})
		inner := m["hooks"].([]interface{})
		for _, h := range inner {
			if h.(map[string]interface{})["command"] == "someone-elses-tool" {
				found = true
			}
		}
	}
	if !found {
		t.Error("another tool's Stop hook must not be replaced")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cli && go test ./internal/agents/ -run MergeStopHook -v`
Expected: FAIL — `undefined: MergeStopHook`.

- [ ] **Step 3: Write the implementation**

Create `cli/internal/agents/claudecode.go`:

```go
package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// StopHookCommand is what Claude Code runs when a session ends. It is
// matched exactly when deciding whether our entry is already registered, so
// re-running connect cannot add it twice.
func StopHookCommand() string { return "agentobs memory hook" }

func ClaudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// MergeStopHook adds our Stop hook to an existing settings map without
// disturbing anything else. Real installs carry unrelated hooks and settings
// in this file, so it merges rather than replaces -- the same discipline
// connect already applies to every other agent's config.
func MergeStopHook(existing map[string]interface{}) map[string]interface{} {
	merged := map[string]interface{}{}
	for k, v := range existing {
		merged[k] = v
	}

	hooks, _ := merged["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = map[string]interface{}{}
	} else {
		copied := map[string]interface{}{}
		for k, v := range hooks {
			copied[k] = v
		}
		hooks = copied
	}

	stop, _ := hooks["Stop"].([]interface{})
	for _, entry := range stop {
		m, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		inner, ok := m["hooks"].([]interface{})
		if !ok {
			continue
		}
		for _, h := range inner {
			hm, ok := h.(map[string]interface{})
			if ok && hm["command"] == StopHookCommand() {
				// Already registered; leave the file untouched.
				merged["hooks"] = hooks
				return merged
			}
		}
	}

	stop = append(stop, map[string]interface{}{
		"hooks": []interface{}{
			map[string]interface{}{"type": "command", "command": StopHookCommand()},
		},
	})
	hooks["Stop"] = stop
	merged["hooks"] = hooks
	return merged
}

// RegisterStopHook merges the hook into ~/.claude/settings.json, backing the
// file up first.
func RegisterStopHook() (string, error) {
	path, err := ClaudeSettingsPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}

	existing := map[string]interface{}{}
	if data, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".bak", data, 0o644); err != nil {
			return "", err
		}
		if err := json.Unmarshal(data, &existing); err != nil {
			return "", err
		}
	}

	out, err := json.MarshalIndent(MergeStopHook(existing), "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
```

- [ ] **Step 4: Offer registration in the wizard**

In `cli/internal/memory/wizard.go`, after the first sync succeeds, add:

```go
	autoSync := false
	if err := survey.AskOne(&survey.Confirm{
		Message: "Sync automatically when a Claude Code session ends?",
		Default: true,
	}, &autoSync); err == nil && autoSync {
		path, err := agents.RegisterStopHook()
		if err != nil {
			fmt.Printf("Could not register the Stop hook: %v\n", err)
			fmt.Println("Sync still works manually with `agentobs memory sync`.")
		} else {
			fmt.Printf("Registered a Stop hook in %s. New sessions sync when they end.\n", path)
		}
	}
```

Add the `agents` import. Keep the printed cron line as the alternative for users who decline.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd cli && go build -o agentobs ./cmd/agentobs && go test ./... && go vet ./... && gofmt -l .`
Expected: all PASS, vet and fmt clean.

- [ ] **Step 6: Commit**

```bash
git add cli/internal/agents/claudecode.go cli/internal/agents/claudecode_test.go cli/internal/memory/wizard.go
git commit -m "Register a Claude Code Stop hook to sync on session end

Merges into ~/.claude/settings.json, backing it up first and preserving
any hooks another tool registered, and is idempotent so re-running
connect cannot add a duplicate."
```

---

### Task 6: Documentation

**Files:**
- Modify: `README.md`, `docs/anonamemory.md`, `CLAUDE.md`

- [ ] **Step 1: Rewrite the README's connector bullet and docs row**

Replace the AnonaMemory feature bullet with:

```markdown
- **Optional AnonaMemory push**: opt in at the end of `agentobs connect` and each project's prompts and responses become a queryable memory layer in its own AnonaMemory space, masked before they leave the machine. Syncs automatically when a Claude Code session ends. See [docs/anonamemory.md](docs/anonamemory.md).
```

- [ ] **Step 2: Fix the false privacy claim**

`README.md`'s privacy bullet currently reads "prompt/tool-detail logging is off by default across every agent". That is false for the hook-shim agents: `connect.go` defaults `mask_prompts` to false, and `cursorhook/event_attributes.go` stores the full prompt when it is false. Replace with:

```markdown
- **Privacy**: Claude Code and Gemini CLI capture prompt text only if you opt in during `connect`. For Cursor, Copilot, Codex, and OpenCode the hook shim captures prompt text **by default** — answer yes to the mask-prompts question during `connect` to store `[MASKED]` instead.
```

- [ ] **Step 3: Fix the quickstart**

The quickstart tells users to `curl | sh` and then run `agentobs install`, which cannot work: the installer places only the binary, and `install` needs this repo's `docker-compose.yml`. Add after the install line:

```markdown
`agentobs install` needs this repo's `docker-compose.yml`, which the binary does not bundle — clone the repo and run it from there, or point at the file with `AGENTOBS_COMPOSE_FILE=/path/to/docker-compose.yml`.
```

- [ ] **Step 4: Update `docs/anonamemory.md`**

Add a section covering, with accurate wording checked against the code:

- `sync` is incremental (watermark plus a 24h lookback); `--since` is the full backfill and ignores the watermark
- one config, several projects, each with its own space and watermark; show the v2 JSON
- `agentobs memory projects`, `sync --project <path>`
- the Stop hook: what it registers, that it re-execs detached so session end is not delayed, that it logs to `~/.config/agentobs/sync.log` and never to the terminal
- v1 configs migrate automatically and are backed up to `memory.json.bak` first

- [ ] **Step 5: Update `CLAUDE.md`**

Extend the AnonaMemory subsection to say the config is per-project (each entry with its own space and watermark), that `SyncAll` iterates entries so one space failing does not stop the others, and that `agentobs memory hook` is the Stop-hook entry point which always exits 0 and logs to a file rather than the terminal.

- [ ] **Step 6: Verify the docs match the binary**

Run:
```bash
cd cli && go build -o agentobs ./cmd/agentobs
./agentobs memory --help && ./agentobs memory sync --help && ./agentobs memory projects --help
```
Expected: every flag and subcommand named in the docs appears in the real output. Fix the docs where they disagree — the code wins.

- [ ] **Step 7: Commit**

```bash
git add README.md docs/anonamemory.md CLAUDE.md
git commit -m "Document per-project spaces and Stop-hook auto-sync

Also corrects the claim that prompt logging is off by default for every
agent -- it is on by default for the four hook-shim agents -- and the
quickstart, which told users to run install without the compose file the
binary does not ship."
```

---

## Verification

```bash
cd cli && go build -o agentobs ./cmd/agentobs && go test ./... && go vet ./... && gofmt -l .
```

Manual, on a machine with a real config:

```bash
./agentobs memory projects
./agentobs memory sync --dry-run
echo '{"session_id":"s1","cwd":"'"$PWD"'"}' | ./agentobs memory hook; echo "exit=$?"
cat ~/.config/agentobs/sync.log
```

The hook must return immediately, print nothing, exit 0, and leave a line in the log. Confirm `~/.claude/settings.json` still contains any hooks that were there before registration.
