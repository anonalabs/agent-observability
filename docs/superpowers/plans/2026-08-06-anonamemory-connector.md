# AnonaMemory Connector Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Push AI coding agent prompt+response turns to AnonaMemory via a new `agentobs memory sync` command, offered at the end of `agentobs connect`.

**Architecture:** A new `cli/internal/memory` package with two readers and one writer. Claude Code session transcripts (`~/.claude/projects/*/*.jsonl`) supply paired prompt+response text; ClickHouse supplies prompt-only turns for the other agents plus per-session cost/token enrichment. Turns are filtered by a project allowlist, masked, deduplicated against a local watermark, then written in chunks of 100 to `POST /v1/record/batch`.

**Tech Stack:** Go 1.25, `spf13/cobra` (CLI), `AlecAivazis/survey/v2` (prompts), stdlib `net/http` + `encoding/json`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-06-anonamemory-connector-design.md`

## Global Constraints

- Module path is `github.com/anonalabs/agent-observability/cli`. All imports of new code use `github.com/anonalabs/agent-observability/cli/internal/memory`.
- All commands run from the `cli/` directory: `go build -o agentobs ./cmd/agentobs`, `go test ./...`, `go vet ./...`.
- No new third-party dependencies. `cli/go.mod` must not change.
- Tests use stdlib `testing` only, table-driven where there are multiple cases, matching `cli/internal/config/config_test.go`. No test framework, no assertion library.
- Tests must not make real network calls. Use `net/http/httptest`.
- AnonaMemory base URL is `https://api.anonalabs.com`. Auth header is exactly `Authorization: Bearer <key>`. `X-API-Key` and a bare `Authorization: <key>` are rejected by the API.
- Retryable HTTP statuses are exactly 429, 500, 503. Never retry any other 4xx.
- `~/.config/agentobs/memory.json` is written with mode `0600`; its parent directory with `0700`.
- Conversation text must never be written to ClickHouse, Prometheus, or any log line.
- Commit messages must not mention Claude, Anthropic, or AI authorship, and must not carry a `Co-Authored-By` trailer.
- Commit after every task.

## File Structure

**Create:**

| Path | Responsibility |
|---|---|
| `cli/internal/memory/client.go` | AnonaMemory REST client: spaces, batch record, retry policy, typed error |
| `cli/internal/memory/client_test.go` | httptest coverage of the client |
| `cli/internal/memory/credentials.go` | `memory.json` load/save, allowlist matching, watermark and dedup bookkeeping |
| `cli/internal/memory/credentials_test.go` | permissions, round-trip, allowlist, dedup |
| `cli/internal/memory/turn.go` | The `Turn` type and its conversion to a `RecordItem`; text masking |
| `cli/internal/memory/turn_test.go` | record-item mapping and masking |
| `cli/internal/memory/transcript.go` | `TranscriptSource` interface and `ClaudeCodeSource` |
| `cli/internal/memory/transcript_test.go` | fixture JSONL parsing |
| `cli/internal/memory/clickhouse.go` | prompt-only turns and per-session cost enrichment |
| `cli/internal/memory/clickhouse_test.go` | httptest coverage of both queries |
| `cli/internal/memory/sync.go` | orchestration: gather, filter, mask, enrich, chunk, record, advance |
| `cli/internal/memory/sync_test.go` | end-to-end sync against fakes |
| `cli/internal/memory/wizard.go` | the connect-time interactive flow |
| `cli/internal/commands/memory.go` | `agentobs memory {sync,status,disconnect}` |
| `docs/anonamemory.md` | user-facing setup guide |

**Modify:**

| Path | Change |
|---|---|
| `cli/internal/commands/connect.go` | call `memory.OfferConnect` once after the path dispatch |
| `cli/cmd/agentobs/main.go` | register `commands.MemoryCmd()` |
| `README.md` | feature bullet, docs table row |
| `CLAUDE.md` | note the new package and command |

---

### Task 1: AnonaMemory REST client

**Files:**
- Create: `cli/internal/memory/client.go`
- Test: `cli/internal/memory/client_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `const DefaultBaseURL = "https://api.anonalabs.com"`
  - `type Space struct { SpaceID, Name, CreatedAt string }`
  - `type RecordItem struct { Content, Context, Timestamp string; Metadata map[string]interface{} }`
  - `type APIError struct { Status int; Code, Message, RequestID string }` implementing `error`
  - `type Client struct { BaseURL, APIKey string; HTTP *http.Client; Sleep func(time.Duration) }`
  - `func NewClient(apiKey string) *Client`
  - `func (c *Client) ListSpaces() ([]Space, error)`
  - `func (c *Client) CreateSpace(name, description string) (Space, error)`
  - `func (c *Client) RecordBatch(spaceID string, items []RecordItem) (int, error)`

- [ ] **Step 1: Write the failing tests**

Create `cli/internal/memory/client_test.go`:

```go
package memory

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testClient(srv *httptest.Server) *Client {
	c := NewClient("anona_live_testkey")
	c.BaseURL = srv.URL
	c.Sleep = func(time.Duration) {}
	return c
}

func TestListSpacesSendsBearerHeader(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"spaces":[{"space_id":"spc_a1","name":"work","created_at":"2026-08-01T00:00:00Z"}],"total":1}`)
	}))
	defer srv.Close()

	spaces, err := testClient(srv).ListSpaces()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer anona_live_testkey" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer anona_live_testkey")
	}
	if gotPath != "/v1/spaces" {
		t.Errorf("path = %q, want /v1/spaces", gotPath)
	}
	if len(spaces) != 1 || spaces[0].SpaceID != "spc_a1" || spaces[0].Name != "work" {
		t.Fatalf("spaces = %+v", spaces)
	}
}

func TestCreateSpacePostsName(t *testing.T) {
	var body map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"space_id":"spc_new","name":"agentobs","created_at":"2026-08-06T00:00:00Z"}`)
	}))
	defer srv.Close()

	space, err := testClient(srv).CreateSpace("agentobs", "coding agent memory")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body["name"] != "agentobs" {
		t.Errorf("name = %v, want agentobs", body["name"])
	}
	if body["description"] != "coding agent memory" {
		t.Errorf("description = %v", body["description"])
	}
	if space.SpaceID != "spc_new" {
		t.Errorf("space_id = %q, want spc_new", space.SpaceID)
	}
}

func TestRecordBatchChunksAtHundred(t *testing.T) {
	var batchSizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			SpaceID string       `json:"space_id"`
			Items   []RecordItem `json:"items"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.SpaceID != "spc_a1" {
			t.Errorf("space_id = %q, want spc_a1", body.SpaceID)
		}
		batchSizes = append(batchSizes, len(body.Items))
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"job_id":"job_1","status":"processing","accepted":`+itoa(len(body.Items))+`}`)
	}))
	defer srv.Close()

	items := make([]RecordItem, 250)
	for i := range items {
		items[i] = RecordItem{Content: "turn"}
	}

	accepted, err := testClient(srv).RecordBatch("spc_a1", items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if accepted != 250 {
		t.Errorf("accepted = %d, want 250", accepted)
	}
	want := []int{100, 100, 50}
	if len(batchSizes) != len(want) {
		t.Fatalf("batches = %v, want %v", batchSizes, want)
	}
	for i := range want {
		if batchSizes[i] != want[i] {
			t.Fatalf("batches = %v, want %v", batchSizes, want)
		}
	}
}

func TestRecordBatchRetriesOn429ThenSucceeds(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"code":"rate_limited","message":"slow down","request_id":"req_1"}`)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"job_id":"job_1","status":"processing","accepted":1}`)
	}))
	defer srv.Close()

	accepted, err := testClient(srv).RecordBatch("spc_a1", []RecordItem{{Content: "turn"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if accepted != 1 {
		t.Errorf("accepted = %d, want 1", accepted)
	}
}

func TestRecordBatchDoesNotRetryOn400(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"code":"bad_request","message":"content required","request_id":"req_7"}`)
	}))
	defer srv.Close()

	_, err := testClient(srv).RecordBatch("spc_a1", []RecordItem{{Content: ""}})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (must not retry 400)", calls)
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Code != "bad_request" || apiErr.RequestID != "req_7" || apiErr.Status != 400 {
		t.Errorf("apiErr = %+v", apiErr)
	}
}

func TestRecordBatchEmptyIsNoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request expected for an empty batch")
	}))
	defer srv.Close()

	accepted, err := testClient(srv).RecordBatch("spc_a1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if accepted != 0 {
		t.Errorf("accepted = %d, want 0", accepted)
	}
}
```

The tests reference a small helper `itoa`; define it in `client.go` (Step 3) as `func itoa(n int) string { return strconv.Itoa(n) }`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cli && go test ./internal/memory/ -v`
Expected: FAIL — the package does not exist yet (`no Go files in .../internal/memory`).

- [ ] **Step 3: Write the implementation**

Create `cli/internal/memory/client.go`:

```go
// Package memory pushes AI coding agent conversation turns to AnonaMemory.
package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// DefaultBaseURL is AnonaMemory's data-plane host. The docs also list
// https://memory.anonalabs.com as a legacy alternative.
const DefaultBaseURL = "https://api.anonalabs.com"

// maxBatchItems is the API's hard limit on /v1/record/batch.
const maxBatchItems = 100

// maxAttempts caps retries of a single request, including the first try.
const maxAttempts = 5

func itoa(n int) string { return strconv.Itoa(n) }

type Space struct {
	SpaceID   string `json:"space_id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// RecordItem is one element of a /v1/record/batch payload. The batch
// endpoint accepts only these four fields -- session_id, agent_id, and tags
// exist on the single-record endpoint only, so those travel in Metadata.
type RecordItem struct {
	Content   string                 `json:"content"`
	Context   string                 `json:"context,omitempty"`
	Timestamp string                 `json:"timestamp,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// APIError is AnonaMemory's documented error envelope. RequestID is what
// their support asks for, so it must survive to the user's terminal.
type APIError struct {
	Status    int    `json:"-"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("anonamemory: HTTP %d %s: %s (request_id %s)", e.Status, e.Code, e.Message, e.RequestID)
}

// retryable reports whether a status should be retried. The API docs are
// explicit that no other 4xx is worth retrying -- it fails identically.
func retryable(status int) bool {
	return status == http.StatusTooManyRequests ||
		status == http.StatusInternalServerError ||
		status == http.StatusServiceUnavailable
}

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
	// Sleep is injectable so tests don't actually wait out the backoff.
	Sleep func(time.Duration)
}

func NewClient(apiKey string) *Client {
	return &Client{
		BaseURL: DefaultBaseURL,
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		Sleep:   time.Sleep,
	}
}

// do issues one request with exponential backoff plus jitter on retryable
// statuses, decoding either the success body or the error envelope into out.
func (c *Client) do(method, path string, payload interface{}, out interface{}) error {
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			jitter := time.Duration(rand.Int63n(int64(time.Second)))
			c.Sleep(backoff + jitter)
		}

		var body io.Reader
		if payload != nil {
			encoded, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			body = bytes.NewReader(encoded)
		}

		req, err := http.NewRequest(method, c.BaseURL+path, body)
		if err != nil {
			return err
		}
		// The scheme is required: X-API-Key and a bare Authorization value
		// are both rejected by the API.
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}

		if resp.StatusCode >= 400 {
			apiErr := &APIError{Status: resp.StatusCode}
			// A non-JSON body (proxy error page, gateway timeout) leaves the
			// envelope fields empty rather than masking the status code.
			_ = json.Unmarshal(respBody, apiErr)
			if !retryable(resp.StatusCode) {
				return apiErr
			}
			lastErr = apiErr
			continue
		}

		if out != nil {
			return json.Unmarshal(respBody, out)
		}
		return nil
	}

	return lastErr
}

func (c *Client) ListSpaces() ([]Space, error) {
	var out struct {
		Spaces []Space `json:"spaces"`
		Total  int     `json:"total"`
	}
	if err := c.do(http.MethodGet, "/v1/spaces", nil, &out); err != nil {
		return nil, err
	}
	return out.Spaces, nil
}

func (c *Client) CreateSpace(name, description string) (Space, error) {
	payload := map[string]string{"name": name}
	if description != "" {
		payload["description"] = description
	}
	var space Space
	if err := c.do(http.MethodPost, "/v1/spaces", payload, &space); err != nil {
		return Space{}, err
	}
	return space, nil
}

// RecordBatch writes items in chunks of maxBatchItems and returns how many
// the API accepted in total. Writes are async server-side: the response
// carries a job_id, not a memory_id.
func (c *Client) RecordBatch(spaceID string, items []RecordItem) (int, error) {
	accepted := 0

	for start := 0; start < len(items); start += maxBatchItems {
		end := start + maxBatchItems
		if end > len(items) {
			end = len(items)
		}

		payload := map[string]interface{}{
			"space_id": spaceID,
			"items":    items[start:end],
		}
		var out struct {
			JobID    string `json:"job_id"`
			Status   string `json:"status"`
			Accepted int    `json:"accepted"`
		}
		if err := c.do(http.MethodPost, "/v1/record/batch", payload, &out); err != nil {
			// Return what landed so the caller can advance its watermark
			// only over the chunks that actually succeeded.
			return accepted, err
		}
		accepted += out.Accepted
	}

	return accepted, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cli && go test ./internal/memory/ -v && go vet ./internal/memory/`
Expected: all six tests PASS, vet clean.

- [ ] **Step 5: Commit**

```bash
git add cli/internal/memory/client.go cli/internal/memory/client_test.go
git commit -m "Add AnonaMemory REST client

Spaces list/create plus batched record writes, with the documented retry
policy: 429/500/503 back off exponentially, every other 4xx fails fast."
```

---

### Task 2: Credentials, allowlist, and watermark

**Files:**
- Create: `cli/internal/memory/credentials.go`
- Test: `cli/internal/memory/credentials_test.go`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces:
  - `type Credentials struct { APIKey, SpaceID, SpaceName, BaseURL string; Projects []string; Watermark time.Time; RecentTurns map[string]time.Time }`
  - `func CredentialsPath() (string, error)`
  - `func LoadCredentials() (*Credentials, error)` — returns `(nil, nil)` when the file does not exist
  - `func SaveCredentials(c *Credentials) error`
  - `func DeleteCredentials() error`
  - `func (c *Credentials) AllowsPath(p string) bool`
  - `func (c *Credentials) Seen(turnID string) bool`
  - `func (c *Credentials) MarkSynced(ids map[string]time.Time)`

Note: `AGENTOBS_MEMORY_CONFIG` overrides the config path. Tests set it via `t.Setenv` so they never touch a real home directory.

- [ ] **Step 1: Write the failing tests**

Create `cli/internal/memory/credentials_test.go`:

```go
package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveCredentialsUsesOwnerOnlyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "memory.json")
	t.Setenv("AGENTOBS_MEMORY_CONFIG", path)

	err := SaveCredentials(&Credentials{APIKey: "anona_live_x", SpaceID: "spc_a1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 600", got)
	}

	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("dir mode = %o, want 700", got)
	}
}

func TestLoadCredentialsRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	t.Setenv("AGENTOBS_MEMORY_CONFIG", path)

	when := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	want := &Credentials{
		APIKey:      "anona_live_x",
		SpaceID:     "spc_a1",
		SpaceName:   "work",
		Projects:    []string{"/home/dev/repo"},
		Watermark:   when,
		RecentTurns: map[string]time.Time{"turn-1": when},
	}
	if err := SaveCredentials(want); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := LoadCredentials()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected credentials, got nil")
	}
	if got.APIKey != want.APIKey || got.SpaceID != want.SpaceID || got.SpaceName != want.SpaceName {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if len(got.Projects) != 1 || got.Projects[0] != "/home/dev/repo" {
		t.Errorf("projects = %v", got.Projects)
	}
	if !got.Watermark.Equal(when) {
		t.Errorf("watermark = %v, want %v", got.Watermark, when)
	}
	if !got.Seen("turn-1") {
		t.Error("turn-1 should be seen")
	}
}

func TestLoadCredentialsMissingFileReturnsNil(t *testing.T) {
	t.Setenv("AGENTOBS_MEMORY_CONFIG", filepath.Join(t.TempDir(), "absent.json"))

	got, err := LoadCredentials()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestAllowsPath(t *testing.T) {
	tests := []struct {
		name     string
		projects []string
		path     string
		want     bool
	}{
		{"exact match", []string{"/home/dev/repo"}, "/home/dev/repo", true},
		{"nested under project", []string{"/home/dev/repo"}, "/home/dev/repo/cli/internal", true},
		{"outside project", []string{"/home/dev/repo"}, "/home/dev/other", false},
		{"sibling with shared prefix", []string{"/home/dev/repo"}, "/home/dev/repo-two", false},
		{"empty allowlist denies everything", nil, "/home/dev/repo", false},
		{"empty path denied", []string{"/home/dev/repo"}, "", false},
		{"second project matches", []string{"/a", "/home/dev/repo"}, "/home/dev/repo/x", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Credentials{Projects: tt.projects}
			if got := c.AllowsPath(tt.path); got != tt.want {
				t.Errorf("AllowsPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestMarkSyncedAdvancesWatermarkAndPrunes(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-48 * time.Hour)
	recent := now.Add(-1 * time.Hour)

	c := &Credentials{
		Watermark:   stale,
		RecentTurns: map[string]time.Time{"old-turn": stale},
	}
	c.MarkSynced(map[string]time.Time{"new-turn": recent, "newest-turn": now})

	if !c.Watermark.Equal(now) {
		t.Errorf("watermark = %v, want %v", c.Watermark, now)
	}
	if c.Seen("old-turn") {
		t.Error("old-turn is older than the 24h window and should have been pruned")
	}
	if !c.Seen("new-turn") || !c.Seen("newest-turn") {
		t.Error("newly synced turns should be seen")
	}
}

func TestMarkSyncedNeverMovesWatermarkBackwards(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	c := &Credentials{Watermark: now}

	c.MarkSynced(map[string]time.Time{"late-arriving": now.Add(-2 * time.Hour)})

	if !c.Watermark.Equal(now) {
		t.Errorf("watermark = %v, want it unchanged at %v", c.Watermark, now)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cli && go test ./internal/memory/ -run 'Credentials|AllowsPath|MarkSynced' -v`
Expected: FAIL — `undefined: Credentials`, `undefined: SaveCredentials`, and so on.

- [ ] **Step 3: Write the implementation**

Create `cli/internal/memory/credentials.go`:

```go
package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// dedupWindow bounds how long a synced turn id is remembered. The batch
// endpoint has no idempotency key, so dedup is entirely local; the window
// keeps the file small while still covering out-of-order transcript writes.
const dedupWindow = 24 * time.Hour

// Credentials is the on-disk state at ~/.config/agentobs/memory.json.
// It holds an API key, so it is always written 0600.
type Credentials struct {
	APIKey    string `json:"api_key"`
	SpaceID   string `json:"space_id"`
	SpaceName string `json:"space_name"`
	// BaseURL overrides DefaultBaseURL, for staging or the legacy host.
	BaseURL string `json:"base_url,omitempty"`
	// Projects are absolute directory paths. A turn is pushed only if its
	// working directory is one of these or nested under one. An empty list
	// means nothing is pushed -- it is never implicitly "all".
	Projects    []string             `json:"projects"`
	Watermark   time.Time            `json:"watermark"`
	RecentTurns map[string]time.Time `json:"recent_turns,omitempty"`
}

// CredentialsPath honours AGENTOBS_MEMORY_CONFIG so tests (and anyone
// running more than one space) never touch the real config.
func CredentialsPath() (string, error) {
	if override := os.Getenv("AGENTOBS_MEMORY_CONFIG"); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "agentobs", "memory.json"), nil
}

// LoadCredentials returns (nil, nil) when no config exists -- "not
// connected" is an ordinary state, not an error.
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
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	if creds.RecentTurns == nil {
		creds.RecentTurns = map[string]time.Time{}
	}
	return &creds, nil
}

func SaveCredentials(c *Credentials) error {
	path, err := CredentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func DeleteCredentials() error {
	path, err := CredentialsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// AllowsPath reports whether p sits inside the project allowlist. It
// compares cleaned paths component-wise so /repo-two doesn't match /repo.
func (c *Credentials) AllowsPath(p string) bool {
	if p == "" {
		return false
	}
	target := filepath.Clean(p)
	for _, project := range c.Projects {
		root := filepath.Clean(project)
		if target == root {
			return true
		}
		if strings.HasPrefix(target, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (c *Credentials) Seen(turnID string) bool {
	_, ok := c.RecentTurns[turnID]
	return ok
}

// MarkSynced records the given turn ids, advances the watermark to the
// newest of them (never backwards, so a late-arriving turn can't rewind
// progress), and prunes ids older than the dedup window.
func (c *Credentials) MarkSynced(ids map[string]time.Time) {
	if c.RecentTurns == nil {
		c.RecentTurns = map[string]time.Time{}
	}
	for id, ts := range ids {
		c.RecentTurns[id] = ts
		if ts.After(c.Watermark) {
			c.Watermark = ts
		}
	}

	cutoff := c.Watermark.Add(-dedupWindow)
	for id, ts := range c.RecentTurns {
		if ts.Before(cutoff) {
			delete(c.RecentTurns, id)
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cli && go test ./internal/memory/ -v && go vet ./internal/memory/`
Expected: all tests PASS (Task 1's included), vet clean.

- [ ] **Step 5: Commit**

```bash
git add cli/internal/memory/credentials.go cli/internal/memory/credentials_test.go
git commit -m "Add memory credentials store with allowlist and watermark

Config lives at ~/.config/agentobs/memory.json, mode 0600. The project
allowlist is deny-by-default and matches whole path components, so
/repo-two never counts as inside /repo."
```

---

### Task 3: The Turn type, masking, and record mapping

**Files:**
- Create: `cli/internal/memory/turn.go`
- Test: `cli/internal/memory/turn_test.go`

**Interfaces:**
- Consumes: `RecordItem` from Task 1.
- Produces:
  - `type Turn struct { Agent, SessionID, TurnID string; Timestamp time.Time; Prompt, Response, Model, CWD, GitBranch string; InputTokens, OutputTokens int; CostUSD float64; Tools []string }`
  - `func (t Turn) HasResponse() bool`
  - `func (t Turn) RecordItem() RecordItem`
  - `func MaskText(s string) string`
  - `func (t Turn) Masked() Turn`

- [ ] **Step 1: Write the failing tests**

Create `cli/internal/memory/turn_test.go`:

```go
package memory

import (
	"testing"
	"time"
)

func TestMaskText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			"home path",
			"see /home/srujan/Documents/notes.md",
			"see /home/[USER]/Documents/notes.md",
		},
		{
			"macos path",
			"open /Users/alice/dev",
			"open /Users/[USER]/dev",
		},
		{
			"email in prose",
			"ping alice@example.com about it",
			"ping a***@example.com about it",
		},
		{
			"two emails",
			"alice@example.com and bob@corp.io",
			"a***@example.com and b***@corp.io",
		},
		{
			"nothing sensitive",
			"refactor the parser",
			"refactor the parser",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MaskText(tt.in); got != tt.want {
				t.Errorf("MaskText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMaskedAppliesToPromptResponseAndCWD(t *testing.T) {
	turn := Turn{
		Prompt:   "fix /home/srujan/app.go",
		Response: "done, mailed alice@example.com",
		CWD:      "/home/srujan/app",
	}

	got := turn.Masked()

	if got.Prompt != "fix /home/[USER]/app.go" {
		t.Errorf("prompt = %q", got.Prompt)
	}
	if got.Response != "done, mailed a***@example.com" {
		t.Errorf("response = %q", got.Response)
	}
	if got.CWD != "/home/[USER]/app" {
		t.Errorf("cwd = %q", got.CWD)
	}
}

func TestRecordItemWithResponse(t *testing.T) {
	when := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	turn := Turn{
		Agent:        "claude-code",
		SessionID:    "sess-1",
		TurnID:       "turn-1",
		Timestamp:    when,
		Prompt:       "add a test",
		Response:     "added",
		Model:        "claude-opus-5",
		CWD:          "/home/dev/repo",
		GitBranch:    "main",
		InputTokens:  120,
		OutputTokens: 45,
		CostUSD:      0.02,
		Tools:        []string{"Read", "Edit"},
	}

	item := turn.RecordItem()

	wantContent := "User: add a test\n\nAssistant: added"
	if item.Content != wantContent {
		t.Errorf("content = %q, want %q", item.Content, wantContent)
	}
	if item.Timestamp != "2026-08-06T12:00:00Z" {
		t.Errorf("timestamp = %q, want RFC3339 UTC", item.Timestamp)
	}
	if item.Context != "repo (main), claude-opus-5" {
		t.Errorf("context = %q", item.Context)
	}
	if item.Metadata["turn_id"] != "turn-1" {
		t.Errorf("turn_id = %v", item.Metadata["turn_id"])
	}
	if item.Metadata["session_id"] != "sess-1" {
		t.Errorf("session_id = %v", item.Metadata["session_id"])
	}
	if item.Metadata["agent_id"] != "claude-code" {
		t.Errorf("agent_id = %v", item.Metadata["agent_id"])
	}
	if item.Metadata["has_response"] != true {
		t.Errorf("has_response = %v, want true", item.Metadata["has_response"])
	}
	if item.Metadata["cost_usd"] != 0.02 {
		t.Errorf("cost_usd = %v", item.Metadata["cost_usd"])
	}
}

func TestRecordItemPromptOnly(t *testing.T) {
	turn := Turn{
		Agent:     "cursor",
		SessionID: "sess-2",
		TurnID:    "turn-2",
		Timestamp: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		Prompt:    "refactor this",
		CWD:       "/home/dev/repo",
	}

	item := turn.RecordItem()

	if item.Content != "User: refactor this" {
		t.Errorf("content = %q, want the prompt with no Assistant section", item.Content)
	}
	if item.Metadata["has_response"] != false {
		t.Errorf("has_response = %v, want false", item.Metadata["has_response"])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cli && go test ./internal/memory/ -run 'Mask|RecordItem' -v`
Expected: FAIL — `undefined: Turn`, `undefined: MaskText`.

- [ ] **Step 3: Write the implementation**

Create `cli/internal/memory/turn.go`:

```go
package memory

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/anonalabs/agent-observability/cli/internal/cursorhook"
)

// emailPattern finds addresses inside free text. cursorhook.MaskEmail
// expects a bare address, so prose needs this to locate them first.
var emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// Turn is one prompt/response exchange. Response is empty for agents whose
// telemetry carries prompts but no assistant text.
type Turn struct {
	Agent     string
	SessionID string
	TurnID    string
	Timestamp time.Time

	Prompt    string
	Response  string
	Model     string
	CWD       string
	GitBranch string

	InputTokens  int
	OutputTokens int
	CostUSD      float64
	Tools        []string
}

func (t Turn) HasResponse() bool { return t.Response != "" }

// MaskText redacts home-directory usernames and email addresses in free
// text, reusing cursorhook's path patterns so both paths behave the same.
func MaskText(s string) string {
	masked := cursorhook.MaskPath(s)
	return emailPattern.ReplaceAllStringFunc(masked, cursorhook.MaskEmail)
}

// Masked returns a copy with every user-supplied text field redacted.
func (t Turn) Masked() Turn {
	t.Prompt = MaskText(t.Prompt)
	t.Response = MaskText(t.Response)
	t.CWD = MaskText(t.CWD)
	return t
}

// RecordItem converts a turn into a /v1/record/batch item. The batch
// endpoint takes only content/context/timestamp/metadata, so session_id,
// agent_id, and tool names all travel inside metadata.
func (t Turn) RecordItem() RecordItem {
	content := "User: " + t.Prompt
	if t.HasResponse() {
		content += "\n\nAssistant: " + t.Response
	}

	var contextParts []string
	if t.CWD != "" {
		repo := filepath.Base(t.CWD)
		if t.GitBranch != "" {
			repo = fmt.Sprintf("%s (%s)", repo, t.GitBranch)
		}
		contextParts = append(contextParts, repo)
	}
	if t.Model != "" {
		contextParts = append(contextParts, t.Model)
	}

	metadata := map[string]interface{}{
		"turn_id":       t.TurnID,
		"session_id":    t.SessionID,
		"agent_id":      t.Agent,
		"has_response":  t.HasResponse(),
		"input_tokens":  t.InputTokens,
		"output_tokens": t.OutputTokens,
		"cost_usd":      t.CostUSD,
		"cwd":           t.CWD,
		"git_branch":    t.GitBranch,
	}
	if len(t.Tools) > 0 {
		metadata["tools"] = t.Tools
	}

	return RecordItem{
		Content:   content,
		Context:   strings.Join(contextParts, ", "),
		Timestamp: t.Timestamp.UTC().Format(time.RFC3339),
		Metadata:  metadata,
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cli && go test ./internal/memory/ -v && go vet ./internal/memory/`
Expected: all tests PASS, vet clean.

- [ ] **Step 5: Commit**

```bash
git add cli/internal/memory/turn.go cli/internal/memory/turn_test.go
git commit -m "Add Turn type with masking and record mapping

Batch items carry only content/context/timestamp/metadata, so session and
agent ids travel in metadata. Masking reuses cursorhook's path patterns."
```

---

### Task 4: Claude Code transcript reader

**Files:**
- Create: `cli/internal/memory/transcript.go`
- Test: `cli/internal/memory/transcript_test.go`

**Interfaces:**
- Consumes: `Turn` from Task 3.
- Produces:
  - `type TranscriptSource interface { Name() string; Turns(since time.Time) ([]Turn, int, error) }` — the `int` is the count of files skipped as unreadable or malformed
  - `type ClaudeCodeSource struct { Root string }`
  - `func NewClaudeCodeSource() (ClaudeCodeSource, error)` — defaults `Root` to `~/.claude/projects`

Transcript format (verified on disk): one JSON object per line. Relevant lines are `type:"user"` and `type:"assistant"`. Assistant lines carry `message.content[]` blocks of `{type:"text",text}` or `{type:"tool_use",name}`, plus `message.model` and `message.usage.{input_tokens,output_tokens}`. Every line carries `uuid`, `timestamp`, `sessionId`, `cwd`, `gitBranch`, `isSidechain`.

- [ ] **Step 1: Write the failing tests**

Create `cli/internal/memory/transcript_test.go`:

```go
package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTranscript creates <root>/<project>/<name>.jsonl with the given lines.
func writeTranscript(t *testing.T, root, project, name, content string) {
	t.Helper()
	dir := filepath.Join(root, project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".jsonl"), []byte(content), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClaudeCodeSourcePairsPromptAndResponse(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "sess-1", `
{"type":"user","uuid":"u1","timestamp":"2026-08-06T12:00:00Z","sessionId":"sess-1","cwd":"/home/dev/repo","gitBranch":"main","message":{"role":"user","content":"add a test"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-08-06T12:00:05Z","sessionId":"sess-1","cwd":"/home/dev/repo","gitBranch":"main","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"added"}],"usage":{"input_tokens":120,"output_tokens":45}}}
`)

	turns, skipped, err := ClaudeCodeSource{Root: root}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1: %+v", len(turns), turns)
	}

	got := turns[0]
	if got.Agent != "claude-code" {
		t.Errorf("agent = %q", got.Agent)
	}
	if got.TurnID != "a1" {
		t.Errorf("turn_id = %q, want the assistant uuid a1", got.TurnID)
	}
	if got.Prompt != "add a test" || got.Response != "added" {
		t.Errorf("prompt/response = %q / %q", got.Prompt, got.Response)
	}
	if got.SessionID != "sess-1" || got.CWD != "/home/dev/repo" || got.GitBranch != "main" {
		t.Errorf("metadata fields = %+v", got)
	}
	if got.Model != "claude-opus-5" {
		t.Errorf("model = %q", got.Model)
	}
	if got.InputTokens != 120 || got.OutputTokens != 45 {
		t.Errorf("tokens = %d / %d", got.InputTokens, got.OutputTokens)
	}
	if !got.Timestamp.Equal(time.Date(2026, 8, 6, 12, 0, 5, 0, time.UTC)) {
		t.Errorf("timestamp = %v, want the assistant line's", got.Timestamp)
	}
}

func TestClaudeCodeSourceJoinsMultipleTextBlocksAndCollectsTools(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "sess-2", `
{"type":"user","uuid":"u1","timestamp":"2026-08-06T12:00:00Z","sessionId":"sess-2","cwd":"/home/dev/repo","message":{"role":"user","content":"read then edit"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-08-06T12:00:01Z","sessionId":"sess-2","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read"}]}}
{"type":"assistant","uuid":"a2","timestamp":"2026-08-06T12:00:02Z","sessionId":"sess-2","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit"}]}}
{"type":"assistant","uuid":"a3","timestamp":"2026-08-06T12:00:03Z","sessionId":"sess-2","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}}
`)

	turns, _, err := ClaudeCodeSource{Root: root}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}
	if turns[0].Response != "first\nsecond" {
		t.Errorf("response = %q, want the text blocks joined", turns[0].Response)
	}
	if len(turns[0].Tools) != 2 || turns[0].Tools[0] != "Read" || turns[0].Tools[1] != "Edit" {
		t.Errorf("tools = %v, want [Read Edit]", turns[0].Tools)
	}
}

func TestClaudeCodeSourceSkipsSidechains(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "sess-3", `
{"type":"user","uuid":"u1","isSidechain":true,"timestamp":"2026-08-06T12:00:00Z","sessionId":"sess-3","cwd":"/home/dev/repo","message":{"role":"user","content":"subagent prompt"}}
{"type":"assistant","uuid":"a1","isSidechain":true,"timestamp":"2026-08-06T12:00:01Z","sessionId":"sess-3","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"subagent reply"}]}}
`)

	turns, _, err := ClaudeCodeSource{Root: root}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turns) != 0 {
		t.Fatalf("got %d turns, want 0 -- sidechains are subagent traffic", len(turns))
	}
}

func TestClaudeCodeSourceHonoursSince(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "sess-4", `
{"type":"user","uuid":"u1","timestamp":"2026-08-06T10:00:00Z","sessionId":"sess-4","cwd":"/home/dev/repo","message":{"role":"user","content":"old"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-08-06T10:00:01Z","sessionId":"sess-4","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"old reply"}]}}
{"type":"user","uuid":"u2","timestamp":"2026-08-06T14:00:00Z","sessionId":"sess-4","cwd":"/home/dev/repo","message":{"role":"user","content":"new"}}
{"type":"assistant","uuid":"a2","timestamp":"2026-08-06T14:00:01Z","sessionId":"sess-4","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"new reply"}]}}
`)

	since := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	turns, _, err := ClaudeCodeSource{Root: root}.Turns(since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}
	if turns[0].Prompt != "new" {
		t.Errorf("prompt = %q, want the turn after since", turns[0].Prompt)
	}
}

func TestClaudeCodeSourceCountsMalformedFilesWithoutFailing(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "good", `
{"type":"user","uuid":"u1","timestamp":"2026-08-06T12:00:00Z","sessionId":"s","cwd":"/home/dev/repo","message":{"role":"user","content":"hi"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-08-06T12:00:01Z","sessionId":"s","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}
`)
	writeTranscript(t, root, "-home-dev-repo", "bad", "{not json at all\n")

	turns, skipped, err := ClaudeCodeSource{Root: root}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turns) != 1 {
		t.Errorf("got %d turns, want the one good file's turn", len(turns))
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
}

func TestClaudeCodeSourceMissingRootIsNotAnError(t *testing.T) {
	turns, skipped, err := ClaudeCodeSource{Root: filepath.Join(t.TempDir(), "absent")}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turns) != 0 || skipped != 0 {
		t.Errorf("turns = %d, skipped = %d, want 0/0", len(turns), skipped)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cli && go test ./internal/memory/ -run ClaudeCode -v`
Expected: FAIL — `undefined: ClaudeCodeSource`.

- [ ] **Step 3: Write the implementation**

Create `cli/internal/memory/transcript.go`:

```go
package memory

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TranscriptSource yields conversation turns for one agent. The second
// return value counts files skipped as unreadable or malformed -- reported
// to the user rather than aborting a sync over one bad file.
type TranscriptSource interface {
	Name() string
	Turns(since time.Time) ([]Turn, int, error)
}

// ClaudeCodeSource reads ~/.claude/projects/<slug>/<session-id>.jsonl.
// Each line is one JSON object; user and assistant lines interleave.
type ClaudeCodeSource struct {
	Root string
}

func NewClaudeCodeSource() (ClaudeCodeSource, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return ClaudeCodeSource{}, err
	}
	return ClaudeCodeSource{Root: filepath.Join(home, ".claude", "projects")}, nil
}

func (ClaudeCodeSource) Name() string { return "claude-code" }

// transcriptLine is the subset of each JSONL record this needs.
type transcriptLine struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	IsSidechain bool            `json:"isSidechain"`
	Timestamp   string          `json:"timestamp"`
	SessionID   string          `json:"sessionId"`
	CWD         string          `json:"cwd"`
	GitBranch   string          `json:"gitBranch"`
	Message     json.RawMessage `json:"message"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
}

type transcriptMessage struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// blocks normalizes content, which is a plain string on user lines and an
// array of typed blocks on assistant lines.
func (m transcriptMessage) blocks() []contentBlock {
	var asString string
	if err := json.Unmarshal(m.Content, &asString); err == nil {
		return []contentBlock{{Type: "text", Text: asString}}
	}
	var asBlocks []contentBlock
	if err := json.Unmarshal(m.Content, &asBlocks); err == nil {
		return asBlocks
	}
	return nil
}

func (s ClaudeCodeSource) Turns(since time.Time) ([]Turn, int, error) {
	pattern := filepath.Join(s.Root, "*", "*.jsonl")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, 0, err
	}

	var all []Turn
	skipped := 0

	for _, file := range files {
		turns, err := s.turnsFromFile(file, since)
		if err != nil {
			skipped++
			continue
		}
		all = append(all, turns...)
	}

	return all, skipped, nil
}

// turnsFromFile pairs each user prompt with the next assistant message that
// actually contains text. Tool-only assistant messages in between contribute
// their tool names to the turn rather than ending it.
func (s ClaudeCodeSource) turnsFromFile(path string, since time.Time) ([]Turn, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var turns []Turn
	var pending *Turn
	var tools []string
	sawValidLine := false

	scanner := bufio.NewScanner(f)
	// Transcript lines routinely exceed bufio's 64KB default.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}

		var line transcriptLine
		if err := json.Unmarshal([]byte(text), &line); err != nil {
			continue
		}
		if line.Type != "user" && line.Type != "assistant" {
			continue
		}
		sawValidLine = true
		// Sidechains are subagent traffic, not the user's own conversation.
		if line.IsSidechain {
			continue
		}

		var msg transcriptMessage
		if err := json.Unmarshal(line.Message, &msg); err != nil {
			continue
		}

		timestamp, err := time.Parse(time.RFC3339, line.Timestamp)
		if err != nil {
			continue
		}

		if line.Type == "user" {
			var promptParts []string
			for _, b := range msg.blocks() {
				if b.Type == "text" && b.Text != "" {
					promptParts = append(promptParts, b.Text)
				}
			}
			if len(promptParts) == 0 {
				// Tool results come back as user-role lines; they aren't prompts.
				continue
			}
			pending = &Turn{
				Agent:     "claude-code",
				SessionID: line.SessionID,
				Prompt:    strings.Join(promptParts, "\n"),
				CWD:       line.CWD,
				GitBranch: line.GitBranch,
			}
			tools = nil
			continue
		}

		var responseParts []string
		for _, b := range msg.blocks() {
			switch b.Type {
			case "text":
				if b.Text != "" {
					responseParts = append(responseParts, b.Text)
				}
			case "tool_use":
				if b.Name != "" {
					tools = append(tools, b.Name)
				}
			}
		}
		if len(responseParts) == 0 || pending == nil {
			continue
		}

		turn := *pending
		turn.TurnID = line.UUID
		turn.Timestamp = timestamp
		turn.Response = strings.Join(responseParts, "\n")
		turn.Model = msg.Model
		turn.InputTokens = msg.Usage.InputTokens
		turn.OutputTokens = msg.Usage.OutputTokens
		turn.Tools = tools
		pending = nil
		tools = nil

		if !since.IsZero() && !turn.Timestamp.After(since) {
			continue
		}
		turns = append(turns, turn)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !sawValidLine {
		return nil, errNoValidLines
	}
	return turns, nil
}

// errNoValidLines marks a file that parsed to nothing usable, so the caller
// counts it as skipped instead of silently reporting zero turns.
var errNoValidLines = errors.New("no valid transcript lines")
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cli && go test ./internal/memory/ -v && go vet ./internal/memory/`
Expected: all tests PASS, vet clean.

- [ ] **Step 5: Commit**

```bash
git add cli/internal/memory/transcript.go cli/internal/memory/transcript_test.go
git commit -m "Add Claude Code transcript reader

Pairs each prompt with the next text-bearing assistant message, collecting
tool names from the tool-only messages in between. Sidechains are subagent
traffic and are excluded."
```

---

### Task 5: ClickHouse prompt-only turns and cost enrichment

**Files:**
- Create: `cli/internal/memory/clickhouse.go`
- Test: `cli/internal/memory/clickhouse_test.go`

**Interfaces:**
- Consumes: `Turn` from Task 3.
- Produces:
  - `type SessionStats struct { CostUSD float64; InputTokens, OutputTokens int }`
  - `type ClickHouse struct { BaseURL string; HTTP *http.Client }`
  - `func NewClickHouse() ClickHouse` — defaults to `http://localhost:8123`
  - `func (ch ClickHouse) PromptOnlyTurns(since time.Time) ([]Turn, error)`
  - `func (ch ClickHouse) SessionStats(since time.Time) (map[string]SessionStats, error)`

ClickHouse is queried over HTTP with the query as the POST body and `FORMAT JSONEachRow`, exactly as `commands/export.go` already does. Schema (confirmed from the shipped dashboards): `otel.otel_logs` has `Timestamp, ServiceName, Body, LogAttributes`; `otel.otel_traces` has `Timestamp, ServiceName, SpanAttributes`. Both attribute columns are `Map(String, String)`.

- [ ] **Step 1: Write the failing tests**

Create `cli/internal/memory/clickhouse_test.go`:

```go
package memory

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPromptOnlyTurnsExcludesClaudeCodeAndMaskedPrompts(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotQuery = string(body)
		io.WriteString(w, strings.Join([]string{
			`{"ts":"2026-08-06 12:00:00.000000000","agent":"cursor-agent","prompt":"refactor this","session":"sess-9","model":"claude-opus-5","cwd":"/home/dev/repo"}`,
			`{"ts":"2026-08-06 12:05:00.000000000","agent":"gemini-cli","prompt":"explain","session":"sess-10","model":"","cwd":""}`,
		}, "\n"))
	}))
	defer srv.Close()

	ch := ClickHouse{BaseURL: srv.URL, HTTP: srv.Client()}
	turns, err := ch.PromptOnlyTurns(time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(gotQuery, "claude-code") {
		t.Error("query must exclude claude-code, whose turns come from transcripts")
	}
	if !strings.Contains(gotQuery, "[MASKED]") {
		t.Error("query must exclude prompts the hook shim already masked")
	}

	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2", len(turns))
	}
	if turns[0].Agent != "cursor-agent" || turns[0].Prompt != "refactor this" {
		t.Errorf("turn 0 = %+v", turns[0])
	}
	if turns[0].SessionID != "sess-9" || turns[0].CWD != "/home/dev/repo" {
		t.Errorf("turn 0 metadata = %+v", turns[0])
	}
	if turns[0].HasResponse() {
		t.Error("ClickHouse turns never carry response text")
	}
	if turns[0].TurnID == "" || turns[0].TurnID == turns[1].TurnID {
		t.Errorf("turn ids must be present and distinct: %q, %q", turns[0].TurnID, turns[1].TurnID)
	}
}

func TestPromptOnlyTurnsTurnIDIsStable(t *testing.T) {
	row := `{"ts":"2026-08-06 12:00:00.000000000","agent":"cursor-agent","prompt":"same","session":"sess-9","model":"","cwd":""}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, row)
	}))
	defer srv.Close()

	ch := ClickHouse{BaseURL: srv.URL, HTTP: srv.Client()}
	first, err := ch.PromptOnlyTurns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := ch.PromptOnlyTurns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first[0].TurnID != second[0].TurnID {
		t.Errorf("turn id must be deterministic: %q vs %q", first[0].TurnID, second[0].TurnID)
	}
}

func TestSessionStatsAggregatesBySession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Join([]string{
			`{"session":"sess-1","cost":"0.0400","input_tokens":"300","output_tokens":"120"}`,
			`{"session":"sess-2","cost":"0.0100","input_tokens":"50","output_tokens":"20"}`,
		}, "\n"))
	}))
	defer srv.Close()

	ch := ClickHouse{BaseURL: srv.URL, HTTP: srv.Client()}
	stats, err := ch.SessionStats(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(stats) != 2 {
		t.Fatalf("got %d sessions, want 2", len(stats))
	}
	if stats["sess-1"].CostUSD != 0.04 {
		t.Errorf("sess-1 cost = %v, want 0.04", stats["sess-1"].CostUSD)
	}
	if stats["sess-1"].InputTokens != 300 || stats["sess-1"].OutputTokens != 120 {
		t.Errorf("sess-1 tokens = %+v", stats["sess-1"])
	}
}

func TestClickHouseErrorsSurfaceStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "Code: 60. Unknown table")
	}))
	defer srv.Close()

	ch := ClickHouse{BaseURL: srv.URL, HTTP: srv.Client()}
	if _, err := ch.SessionStats(time.Time{}); err == nil {
		t.Fatal("expected an error, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cli && go test ./internal/memory/ -run 'PromptOnly|SessionStats|ClickHouse' -v`
Expected: FAIL — `undefined: ClickHouse`.

- [ ] **Step 3: Write the implementation**

Create `cli/internal/memory/clickhouse.go`:

```go
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultClickHouseURL matches the HTTP port docker-compose.yml publishes.
const DefaultClickHouseURL = "http://localhost:8123"

// clickHouseTimeLayout is what ClickHouse's JSONEachRow emits for DateTime64.
const clickHouseTimeLayout = "2006-01-02 15:04:05.999999999"

type SessionStats struct {
	CostUSD      float64
	InputTokens  int
	OutputTokens int
}

type ClickHouse struct {
	BaseURL string
	HTTP    *http.Client
}

func NewClickHouse() ClickHouse {
	return ClickHouse{
		BaseURL: DefaultClickHouseURL,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// query posts SQL as the request body, the same shape commands/export.go
// uses, and decodes the JSONEachRow response one object per line.
func (ch ClickHouse) query(sql string) ([]map[string]string, error) {
	resp, err := ch.HTTP.Post(ch.BaseURL+"/", "text/plain", strings.NewReader(sql))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("clickhouse returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var rows []map[string]string
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row map[string]string
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// sinceClause renders a WHERE fragment; a zero time means no lower bound.
func sinceClause(column string, since time.Time) string {
	if since.IsZero() {
		return "1=1"
	}
	return fmt.Sprintf("%s > toDateTime64('%s', 9, 'UTC')", column, since.UTC().Format(clickHouseTimeLayout))
}

// promptTurnID derives a stable id for a turn that has no natural uuid, so
// re-running a sync doesn't re-push the same prompt.
func promptTurnID(agent, session, timestamp, prompt string) string {
	sum := sha256.Sum256([]byte(agent + "|" + session + "|" + timestamp + "|" + prompt))
	return "ch-" + hex.EncodeToString(sum[:8])
}

// PromptOnlyTurns returns prompts for every agent except Claude Code, whose
// turns come from transcripts with real response text. Both otel_logs (the
// native-OTel agents) and otel_traces (the hook-shim agents) are unioned,
// mirroring how the leaderboard dashboard treats the two tables.
func (ch ClickHouse) PromptOnlyTurns(since time.Time) ([]Turn, error) {
	// The UNION ALL is wrapped in a subquery because ClickHouse binds a
	// trailing ORDER BY to the last SELECT only, not to the union.
	sql := fmt.Sprintf(`
SELECT ts, agent, prompt, session, model, cwd FROM (
  SELECT
    toString(Timestamp) AS ts,
    ServiceName AS agent,
    LogAttributes['prompt'] AS prompt,
    LogAttributes['session.id'] AS session,
    LogAttributes['model'] AS model,
    '' AS cwd
  FROM otel.otel_logs
  WHERE LogAttributes['event.name'] IN ('user_prompt', 'gemini_cli.user_prompt')
    AND LogAttributes['prompt'] NOT IN ('', '[MASKED]')
    AND ServiceName != 'claude-code'
    AND %s
  UNION ALL
  SELECT
    toString(Timestamp) AS ts,
    ServiceName AS agent,
    SpanAttributes['gen_ai.prompt.0.content'] AS prompt,
    SpanAttributes['langsmith.trace.session_id'] AS session,
    SpanAttributes['gen_ai.request.model'] AS model,
    SpanAttributes['langsmith.metadata.shell_cwd'] AS cwd
  FROM otel.otel_traces
  WHERE SpanAttributes['gen_ai.prompt.0.content'] NOT IN ('', '[MASKED]')
    AND ServiceName != 'claude-code'
    AND %s
)
ORDER BY ts ASC
FORMAT JSONEachRow`,
		sinceClause("Timestamp", since),
		sinceClause("Timestamp", since),
	)

	rows, err := ch.query(sql)
	if err != nil {
		return nil, err
	}

	var turns []Turn
	for _, row := range rows {
		timestamp, err := time.Parse(clickHouseTimeLayout, row["ts"])
		if err != nil {
			continue
		}
		turns = append(turns, Turn{
			Agent:     row["agent"],
			SessionID: row["session"],
			TurnID:    promptTurnID(row["agent"], row["session"], row["ts"], row["prompt"]),
			Timestamp: timestamp.UTC(),
			Prompt:    row["prompt"],
			Model:     row["model"],
			CWD:       row["cwd"],
		})
	}
	return turns, nil
}

// SessionStats sums cost and tokens per session from Claude Code's
// api_request events, used to enrich turns that already have their text.
func (ch ClickHouse) SessionStats(since time.Time) (map[string]SessionStats, error) {
	sql := fmt.Sprintf(`
SELECT
  LogAttributes['session.id'] AS session,
  toString(sum(toFloat64OrZero(LogAttributes['cost_usd']))) AS cost,
  toString(sum(toInt64OrZero(LogAttributes['input_tokens']))) AS input_tokens,
  toString(sum(toInt64OrZero(LogAttributes['output_tokens']))) AS output_tokens
FROM otel.otel_logs
WHERE LogAttributes['event.name'] = 'api_request'
  AND LogAttributes['session.id'] != ''
  AND %s
GROUP BY session
FORMAT JSONEachRow`, sinceClause("Timestamp", since))

	rows, err := ch.query(sql)
	if err != nil {
		return nil, err
	}

	stats := map[string]SessionStats{}
	for _, row := range rows {
		cost, _ := strconv.ParseFloat(row["cost"], 64)
		input, _ := strconv.Atoi(row["input_tokens"])
		output, _ := strconv.Atoi(row["output_tokens"])
		stats[row["session"]] = SessionStats{
			CostUSD:      cost,
			InputTokens:  input,
			OutputTokens: output,
		}
	}
	return stats, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cli && go test ./internal/memory/ -v && go vet ./internal/memory/`
Expected: all tests PASS, vet clean.

- [ ] **Step 5: Commit**

```bash
git add cli/internal/memory/clickhouse.go cli/internal/memory/clickhouse_test.go
git commit -m "Add ClickHouse reader for prompt-only turns and session cost

Unions otel_logs and otel_traces the way the leaderboard dashboard does,
excluding claude-code since transcripts already give it full exchanges.
Turn ids are content-hashed so re-syncing never duplicates a prompt."
```

---

### Task 6: Sync orchestration

**Files:**
- Create: `cli/internal/memory/sync.go`
- Test: `cli/internal/memory/sync_test.go`

**Interfaces:**
- Consumes: `Credentials`, `Client`, `Turn`, `TranscriptSource`, `ClickHouse` from Tasks 1–5.
- Produces:
  - `type Enricher interface { PromptOnlyTurns(since time.Time) ([]Turn, error); SessionStats(since time.Time) (map[string]SessionStats, error) }` — satisfied by `ClickHouse`
  - `type Recorder interface { RecordBatch(spaceID string, items []RecordItem) (int, error) }` — satisfied by `*Client`
  - `type Options struct { DryRun bool; Since *time.Time }`
  - `type Result struct { Pushed, Deduped, Filtered, SkippedFiles int; EnrichErr error; PromptOnlyErr error }`
  - `func Sync(creds *Credentials, rec Recorder, sources []TranscriptSource, enricher Enricher, opts Options) (Result, error)`

`Sync` mutates `creds` (watermark and recent turns) but does **not** save it — the caller decides when to persist, so a dry run leaves the file untouched.

- [ ] **Step 1: Write the failing tests**

Create `cli/internal/memory/sync_test.go`:

```go
package memory

import (
	"errors"
	"testing"
	"time"
)

// --- fakes ---

type fakeSource struct {
	turns   []Turn
	skipped int
}

func (fakeSource) Name() string { return "fake" }
func (f fakeSource) Turns(since time.Time) ([]Turn, int, error) {
	var out []Turn
	for _, turn := range f.turns {
		if since.IsZero() || turn.Timestamp.After(since) {
			out = append(out, turn)
		}
	}
	return out, f.skipped, nil
}

type fakeRecorder struct {
	items []RecordItem
	err   error
}

func (r *fakeRecorder) RecordBatch(spaceID string, items []RecordItem) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	r.items = append(r.items, items...)
	return len(items), nil
}

type fakeEnricher struct {
	promptOnly []Turn
	stats      map[string]SessionStats
	promptErr  error
	statsErr   error
}

func (e fakeEnricher) PromptOnlyTurns(time.Time) ([]Turn, error) {
	return e.promptOnly, e.promptErr
}
func (e fakeEnricher) SessionStats(time.Time) (map[string]SessionStats, error) {
	return e.stats, e.statsErr
}

func turnAt(id, cwd string, at time.Time) Turn {
	return Turn{
		Agent:     "claude-code",
		SessionID: "sess-1",
		TurnID:    id,
		Timestamp: at,
		Prompt:    "do it",
		Response:  "done",
		CWD:       cwd,
	}
}

// --- tests ---

func TestSyncFiltersByProjectAllowlist(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	creds := &Credentials{SpaceID: "spc_a1", Projects: []string{"/home/dev/repo"}}
	rec := &fakeRecorder{}
	src := fakeSource{turns: []Turn{
		turnAt("in", "/home/dev/repo/cli", now),
		turnAt("out", "/home/dev/elsewhere", now),
	}}

	result, err := Sync(creds, rec, []TranscriptSource{src}, fakeEnricher{}, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Pushed != 1 {
		t.Errorf("pushed = %d, want 1", result.Pushed)
	}
	if result.Filtered != 1 {
		t.Errorf("filtered = %d, want 1", result.Filtered)
	}
	if len(rec.items) != 1 {
		t.Fatalf("recorded %d items, want 1", len(rec.items))
	}
}

func TestSyncSkipsAlreadySeenTurns(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	creds := &Credentials{
		SpaceID:     "spc_a1",
		Projects:    []string{"/home/dev/repo"},
		RecentTurns: map[string]time.Time{"seen": now},
	}
	rec := &fakeRecorder{}
	src := fakeSource{turns: []Turn{
		turnAt("seen", "/home/dev/repo", now),
		turnAt("fresh", "/home/dev/repo", now.Add(time.Minute)),
	}}

	result, err := Sync(creds, rec, []TranscriptSource{src}, fakeEnricher{}, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Pushed != 1 || result.Deduped != 1 {
		t.Errorf("pushed = %d, deduped = %d, want 1/1", result.Pushed, result.Deduped)
	}
	if rec.items[0].Metadata["turn_id"] != "fresh" {
		t.Errorf("pushed turn_id = %v, want fresh", rec.items[0].Metadata["turn_id"])
	}
}

func TestSyncMasksContent(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	creds := &Credentials{SpaceID: "spc_a1", Projects: []string{"/home/dev/repo"}}
	rec := &fakeRecorder{}
	turn := turnAt("t1", "/home/dev/repo", now)
	turn.Prompt = "look at /home/dev/repo/main.go"

	if _, err := Sync(creds, rec, []TranscriptSource{fakeSource{turns: []Turn{turn}}}, fakeEnricher{}, Options{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := rec.items[0].Content; got != "User: look at /home/[USER]/repo/main.go\n\nAssistant: done" {
		t.Errorf("content = %q, want the home path masked", got)
	}
}

func TestSyncEnrichesFromSessionStats(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	creds := &Credentials{SpaceID: "spc_a1", Projects: []string{"/home/dev/repo"}}
	rec := &fakeRecorder{}
	enricher := fakeEnricher{stats: map[string]SessionStats{
		"sess-1": {CostUSD: 0.25, InputTokens: 900, OutputTokens: 300},
	}}

	src := fakeSource{turns: []Turn{turnAt("t1", "/home/dev/repo", now)}}
	if _, err := Sync(creds, rec, []TranscriptSource{src}, enricher, Options{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.items[0].Metadata["cost_usd"] != 0.25 {
		t.Errorf("cost_usd = %v, want 0.25", rec.items[0].Metadata["cost_usd"])
	}
}

func TestSyncSurvivesEnricherFailure(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	creds := &Credentials{SpaceID: "spc_a1", Projects: []string{"/home/dev/repo"}}
	rec := &fakeRecorder{}
	enricher := fakeEnricher{
		statsErr:  errors.New("clickhouse down"),
		promptErr: errors.New("clickhouse down"),
	}

	src := fakeSource{turns: []Turn{turnAt("t1", "/home/dev/repo", now)}}
	result, err := Sync(creds, rec, []TranscriptSource{src}, enricher, Options{})
	if err != nil {
		t.Fatalf("sync must degrade, not fail: %v", err)
	}
	if result.Pushed != 1 {
		t.Errorf("pushed = %d, want 1", result.Pushed)
	}
	if result.EnrichErr == nil || result.PromptOnlyErr == nil {
		t.Error("the enrichment failures should be reported on the result")
	}
}

func TestSyncDryRunRecordsNothingAndLeavesWatermark(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	creds := &Credentials{SpaceID: "spc_a1", Projects: []string{"/home/dev/repo"}}
	rec := &fakeRecorder{}
	src := fakeSource{turns: []Turn{turnAt("t1", "/home/dev/repo", now)}}

	result, err := Sync(creds, rec, []TranscriptSource{src}, fakeEnricher{}, Options{DryRun: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Pushed != 1 {
		t.Errorf("pushed = %d, want 1 (the count it would push)", result.Pushed)
	}
	if len(rec.items) != 0 {
		t.Errorf("recorded %d items, want 0 on a dry run", len(rec.items))
	}
	if !creds.Watermark.IsZero() {
		t.Errorf("watermark = %v, want it untouched on a dry run", creds.Watermark)
	}
}

func TestSyncDoesNotAdvanceWatermarkWhenRecordFails(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	creds := &Credentials{SpaceID: "spc_a1", Projects: []string{"/home/dev/repo"}}
	rec := &fakeRecorder{err: errors.New("429 forever")}
	src := fakeSource{turns: []Turn{turnAt("t1", "/home/dev/repo", now)}}

	if _, err := Sync(creds, rec, []TranscriptSource{src}, fakeEnricher{}, Options{}); err == nil {
		t.Fatal("expected the record failure to surface")
	}
	if !creds.Watermark.IsZero() {
		t.Errorf("watermark = %v, want it unchanged after a failed write", creds.Watermark)
	}
	if creds.Seen("t1") {
		t.Error("a turn that failed to record must not be marked as seen")
	}
}

func TestSyncCountsSkippedFiles(t *testing.T) {
	creds := &Credentials{SpaceID: "spc_a1", Projects: []string{"/home/dev/repo"}}
	rec := &fakeRecorder{}

	result, err := Sync(creds, rec, []TranscriptSource{fakeSource{skipped: 3}}, fakeEnricher{}, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SkippedFiles != 3 {
		t.Errorf("skipped files = %d, want 3", result.SkippedFiles)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cli && go test ./internal/memory/ -run Sync -v`
Expected: FAIL — `undefined: Sync`, `undefined: Options`.

- [ ] **Step 3: Write the implementation**

Create `cli/internal/memory/sync.go`:

```go
package memory

import (
	"sort"
	"time"
)

// Enricher supplies the ClickHouse-derived parts of a sync. It is an
// interface so tests can run without a ClickHouse, and so a failure to
// reach it degrades the sync instead of ending it.
type Enricher interface {
	PromptOnlyTurns(since time.Time) ([]Turn, error)
	SessionStats(since time.Time) (map[string]SessionStats, error)
}

// Recorder is the write side, satisfied by *Client.
type Recorder interface {
	RecordBatch(spaceID string, items []RecordItem) (int, error)
}

type Options struct {
	DryRun bool
	// Since overrides the stored watermark, for a manual backfill.
	Since *time.Time
}

type Result struct {
	Pushed       int
	Deduped      int
	Filtered     int
	SkippedFiles int
	// EnrichErr and PromptOnlyErr record a degraded run: the turns that
	// were available still went out, without ClickHouse's contribution.
	EnrichErr     error
	PromptOnlyErr error
}

// Sync gathers turns from every source, filters them by allowlist and
// watermark, masks them, enriches them, and writes them. It mutates creds'
// watermark on success but does not persist -- the caller decides that, so
// a dry run leaves the config file alone.
func Sync(creds *Credentials, rec Recorder, sources []TranscriptSource, enricher Enricher, opts Options) (Result, error) {
	var result Result

	since := creds.Watermark
	if opts.Since != nil {
		since = *opts.Since
	}

	var candidates []Turn

	for _, source := range sources {
		turns, skipped, err := source.Turns(since)
		if err != nil {
			return result, err
		}
		result.SkippedFiles += skipped
		candidates = append(candidates, turns...)
	}

	if enricher != nil {
		promptOnly, err := enricher.PromptOnlyTurns(since)
		if err != nil {
			result.PromptOnlyErr = err
		} else {
			candidates = append(candidates, promptOnly...)
		}
	}

	var stats map[string]SessionStats
	if enricher != nil {
		var err error
		stats, err = enricher.SessionStats(since)
		if err != nil {
			result.EnrichErr = err
		}
	}

	var selected []Turn
	for _, turn := range candidates {
		if !creds.AllowsPath(turn.CWD) {
			result.Filtered++
			continue
		}
		if creds.Seen(turn.TurnID) {
			result.Deduped++
			continue
		}
		selected = append(selected, turn)
	}

	// Oldest first, so a partial failure leaves the watermark at a
	// contiguous point rather than skipping over unsent turns.
	sort.Slice(selected, func(i, j int) bool {
		return selected[i].Timestamp.Before(selected[j].Timestamp)
	})

	items := make([]RecordItem, 0, len(selected))
	synced := make(map[string]time.Time, len(selected))
	for _, turn := range selected {
		masked := turn.Masked()
		if stat, ok := stats[masked.SessionID]; ok && masked.CostUSD == 0 {
			masked.CostUSD = stat.CostUSD
			if masked.InputTokens == 0 {
				masked.InputTokens = stat.InputTokens
			}
			if masked.OutputTokens == 0 {
				masked.OutputTokens = stat.OutputTokens
			}
		}
		items = append(items, masked.RecordItem())
		synced[turn.TurnID] = turn.Timestamp
	}

	result.Pushed = len(items)

	if opts.DryRun || len(items) == 0 {
		return result, nil
	}

	accepted, err := rec.RecordBatch(creds.SpaceID, items)
	if err != nil {
		// Leave the watermark alone so the next run retries these turns.
		result.Pushed = accepted
		return result, err
	}

	creds.MarkSynced(synced)
	return result, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cli && go test ./internal/memory/ -v && go vet ./internal/memory/`
Expected: all tests PASS, vet clean.

- [ ] **Step 5: Commit**

```bash
git add cli/internal/memory/sync.go cli/internal/memory/sync_test.go
git commit -m "Add memory sync orchestration

Gathers turns from transcripts and ClickHouse, applies the allowlist and
watermark, masks, enriches, and writes. A ClickHouse outage degrades the
run rather than ending it; a failed write leaves the watermark untouched."
```

---

### Task 7: `agentobs memory` command

**Files:**
- Create: `cli/internal/commands/memory.go`
- Modify: `cli/cmd/agentobs/main.go`

**Interfaces:**
- Consumes: everything from Tasks 1–6.
- Produces: `func MemoryCmd() *cobra.Command` in package `commands`.

There is no test for this task. It is command wiring over already-tested units, matching how `install`, `status`, `export`, and `agents` are structured — none of those have tests either. Verification is by running the binary.

- [ ] **Step 1: Write the command**

Create `cli/internal/commands/memory.go`:

```go
package commands

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/anonalabs/agent-observability/cli/internal/memory"
)

// requireCredentials loads the memory config, turning "not connected" into
// an actionable error rather than a nil dereference.
func requireCredentials() (*memory.Credentials, error) {
	creds, err := memory.LoadCredentials()
	if err != nil {
		return nil, err
	}
	if creds == nil {
		return nil, fmt.Errorf("not connected to AnonaMemory -- run `agentobs connect` and answer yes, or see docs/anonamemory.md")
	}
	return creds, nil
}

func newClient(creds *memory.Credentials) *memory.Client {
	client := memory.NewClient(creds.APIKey)
	if creds.BaseURL != "" {
		client.BaseURL = creds.BaseURL
	}
	return client
}

func MemoryCmd() *cobra.Command {
	memoryCmd := &cobra.Command{
		Use:   "memory",
		Short: "Push agent prompts and responses to AnonaMemory",
	}

	memoryCmd.AddCommand(memorySyncCmd())
	memoryCmd.AddCommand(memoryStatusCmd())
	memoryCmd.AddCommand(memoryDisconnectCmd())
	return memoryCmd
}

func memorySyncCmd() *cobra.Command {
	var quiet, dryRun bool
	var since string

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Push every turn newer than the last sync to AnonaMemory",
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := requireCredentials()
			if err != nil {
				return err
			}

			opts := memory.Options{DryRun: dryRun}
			if since != "" {
				seconds, err := parseSince(since)
				if err != nil {
					return err
				}
				at := time.Now().Add(-time.Duration(seconds) * time.Second)
				opts.Since = &at
			}

			source, err := memory.NewClaudeCodeSource()
			if err != nil {
				return err
			}
			clickhouse := memory.NewClickHouse()

			result, syncErr := memory.Sync(
				creds,
				newClient(creds),
				[]memory.TranscriptSource{source},
				clickhouse,
				opts,
			)

			// Persist whatever progress was made before reporting failure,
			// so a partial run isn't repeated from the start.
			if !dryRun {
				if err := memory.SaveCredentials(creds); err != nil {
					return err
				}
			}
			if syncErr != nil {
				return syncErr
			}

			if !quiet {
				verb := "Pushed"
				if dryRun {
					verb = "Would push"
				}
				fmt.Printf("%s %d turns to space %s.\n", verb, result.Pushed, creds.SpaceName)
				if result.Deduped > 0 {
					fmt.Printf("Skipped %d already-synced turns.\n", result.Deduped)
				}
				if result.Filtered > 0 {
					fmt.Printf("Skipped %d turns outside the project allowlist.\n", result.Filtered)
				}
				if result.SkippedFiles > 0 {
					fmt.Printf("Skipped %d unreadable transcript files.\n", result.SkippedFiles)
				}
				if result.EnrichErr != nil || result.PromptOnlyErr != nil {
					fmt.Println("ClickHouse was unreachable -- turns went out without cost/tool context, and prompt-only agents were skipped this run.")
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress the summary (for cron)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be pushed without pushing or advancing the watermark")
	cmd.Flags().StringVar(&since, "since", "", "override the watermark, e.g. 24h or 7d")

	return cmd
}

func memoryStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the configured space, last sync, and what's pending",
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := requireCredentials()
			if err != nil {
				return err
			}

			fmt.Printf("%-16s %s (%s)\n", "space", creds.SpaceName, creds.SpaceID)
			if creds.Watermark.IsZero() {
				fmt.Printf("%-16s never\n", "last sync")
			} else {
				fmt.Printf("%-16s %s\n", "last sync", creds.Watermark.Format(time.RFC3339))
			}
			for i, project := range creds.Projects {
				label := ""
				if i == 0 {
					label = "projects"
				}
				fmt.Printf("%-16s %s\n", label, project)
			}

			source, err := memory.NewClaudeCodeSource()
			if err != nil {
				return err
			}
			result, err := memory.Sync(
				creds,
				newClient(creds),
				[]memory.TranscriptSource{source},
				memory.NewClickHouse(),
				memory.Options{DryRun: true},
			)
			if err != nil {
				return err
			}
			fmt.Printf("%-16s %d\n", "pending turns", result.Pushed)

			if result.PromptOnlyErr != nil {
				fmt.Println()
				fmt.Println("ClickHouse is unreachable, so prompt-only agents (Cursor, Copilot, Codex, OpenCode, Gemini CLI) contribute nothing.")
			} else if result.Pushed == 0 {
				fmt.Println()
				fmt.Println("Nothing pending. Note that non-Claude-Code agents only contribute prompts if you enabled")
				fmt.Println("prompt logging when connecting them (OTEL_LOG_USER_PROMPTS, telemetry.logPrompts, or")
				fmt.Println("declining the hook shim's mask-prompts question) -- all of those default to off.")
			}
			return nil
		},
	}
}

func memoryDisconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect",
		Short: "Delete the local AnonaMemory config (nothing is removed server-side)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := memory.CredentialsPath()
			if err != nil {
				return err
			}
			if err := memory.DeleteCredentials(); err != nil {
				return err
			}
			fmt.Printf("Removed %s. Memories already pushed to AnonaMemory are untouched.\n", path)
			return nil
		},
	}
}
```

- [ ] **Step 2: Register the command**

In `cli/cmd/agentobs/main.go`, add one line after `root.AddCommand(commands.ConfigAlertsCmd())`:

```go
	root.AddCommand(commands.MemoryCmd())
```

- [ ] **Step 3: Verify it builds and the tests still pass**

Run: `cd cli && go build -o agentobs ./cmd/agentobs && go test ./... && go vet ./...`
Expected: builds, all tests PASS, vet clean.

- [ ] **Step 4: Verify the command surface by hand**

Run:
```bash
cd cli && ./agentobs memory --help && ./agentobs memory status
```
Expected: help lists `sync`, `status`, `disconnect`. `status` exits non-zero with `not connected to AnonaMemory -- run 'agentobs connect' and answer yes, or see docs/anonamemory.md`.

- [ ] **Step 5: Commit**

```bash
git add cli/internal/commands/memory.go cli/cmd/agentobs/main.go
git commit -m "Add agentobs memory sync/status/disconnect

status runs a dry-run sync to report pending turns, and calls out that
non-Claude-Code agents contribute nothing unless prompt logging was
enabled -- otherwise an empty result looks like a bug."
```

---

### Task 8: Connect wizard

**Files:**
- Create: `cli/internal/memory/wizard.go`
- Modify: `cli/internal/commands/connect.go`

**Interfaces:**
- Consumes: `Client`, `Credentials`, `Sync` from Tasks 1–6.
- Produces: `func OfferConnect(cwd string, nonInteractive bool, enabled *bool) error`

`enabled` is the tri-state flag pointer the codebase already uses (`nil` means the user didn't pass `--memory`). Under `--non-interactive` the wizard runs only if `enabled` is non-nil and true; since it needs an API key it can't obtain non-interactively, that combination returns a clear error rather than half-configuring.

- [ ] **Step 1: Write the wizard**

Create `cli/internal/memory/wizard.go`:

```go
package memory

import (
	"fmt"
	"time"

	"github.com/AlecAivazis/survey/v2"
)

const signupURL = "https://docs.anonalabs.com/quickstart"

// createNewSpaceOption is the sentinel entry in the space picker.
const createNewSpaceOption = "Create a new space..."

// OfferConnect asks whether to push conversation turns to AnonaMemory and,
// if so, walks through key entry, space selection, the project allowlist,
// and a first sync. It never returns an error that should fail `connect` --
// telemetry setup has already succeeded by this point, and an optional
// add-on must not undo it. Failures are printed and swallowed.
func OfferConnect(cwd string, nonInteractive bool, enabled *bool) error {
	if nonInteractive {
		if enabled != nil && *enabled {
			fmt.Println("Skipping AnonaMemory: --memory needs an API key, which can't be collected non-interactively.")
			fmt.Println("Run `agentobs connect` interactively, or write ~/.config/agentobs/memory.json by hand (see docs/anonamemory.md).")
		}
		return nil
	}

	if enabled == nil {
		want := false
		prompt := &survey.Confirm{
			Message: "Also push prompts and responses to AnonaMemory?",
			Default: false,
		}
		if err := survey.AskOne(prompt, &want); err != nil {
			return nil
		}
		if !want {
			return nil
		}
	} else if !*enabled {
		return nil
	}

	if err := runWizard(cwd); err != nil {
		fmt.Printf("AnonaMemory setup didn't complete: %v\n", err)
		fmt.Println("Telemetry is still wired up. Re-run `agentobs connect` to try again.")
	}
	return nil
}

func runWizard(cwd string) error {
	fmt.Println()
	fmt.Printf("Create an API key at %s (signing up doesn't make one for you).\n", signupURL)

	var apiKey string
	if err := survey.AskOne(&survey.Password{Message: "AnonaMemory API key:"}, &apiKey, survey.WithValidator(survey.Required)); err != nil {
		return err
	}

	client := NewClient(apiKey)

	spaces, err := client.ListSpaces()
	if err != nil {
		return fmt.Errorf("listing spaces: %w", err)
	}

	spaceID, spaceName, err := chooseSpace(client, spaces)
	if err != nil {
		return err
	}

	projects, err := chooseProjects(cwd)
	if err != nil {
		return err
	}

	creds := &Credentials{
		APIKey:      apiKey,
		SpaceID:     spaceID,
		SpaceName:   spaceName,
		Projects:    projects,
		RecentTurns: map[string]time.Time{},
	}
	if err := SaveCredentials(creds); err != nil {
		return err
	}

	path, _ := CredentialsPath()
	fmt.Printf("Saved %s (mode 0600).\n", path)

	source, err := NewClaudeCodeSource()
	if err != nil {
		return err
	}
	result, syncErr := Sync(creds, client, []TranscriptSource{source}, NewClickHouse(), Options{})
	if err := SaveCredentials(creds); err != nil {
		return err
	}
	if syncErr != nil {
		return fmt.Errorf("first sync: %w", syncErr)
	}

	fmt.Printf("Pushed %d turns to %s.\n", result.Pushed, spaceName)
	if result.PromptOnlyErr != nil {
		fmt.Println("ClickHouse was unreachable, so only Claude Code transcripts were read this run.")
	}
	fmt.Println()
	fmt.Println("Keep it current with an hourly cron entry:")
	fmt.Println("  0 * * * * agentobs memory sync --quiet")
	fmt.Println("Check state any time with `agentobs memory status`.")
	return nil
}

// chooseSpace shows existing spaces plus a create option. With no spaces at
// all, it goes straight to creation -- an empty picker is a dead end.
func chooseSpace(client *Client, spaces []Space) (string, string, error) {
	if len(spaces) == 0 {
		fmt.Println("No spaces on this account yet.")
		return createSpace(client)
	}

	options := make([]string, 0, len(spaces)+1)
	byName := map[string]string{}
	for _, space := range spaces {
		options = append(options, space.Name)
		byName[space.Name] = space.SpaceID
	}
	options = append(options, createNewSpaceOption)

	var chosen string
	prompt := &survey.Select{Message: "Space to record into:", Options: options}
	if err := survey.AskOne(prompt, &chosen); err != nil {
		return "", "", err
	}
	if chosen == createNewSpaceOption {
		return createSpace(client)
	}
	return byName[chosen], chosen, nil
}

func createSpace(client *Client) (string, string, error) {
	var name string
	prompt := &survey.Input{Message: "New space name:", Default: "coding-agents"}
	if err := survey.AskOne(prompt, &name, survey.WithValidator(survey.Required)); err != nil {
		return "", "", err
	}
	space, err := client.CreateSpace(name, "AI coding agent conversation history")
	if err != nil {
		return "", "", fmt.Errorf("creating space: %w", err)
	}
	fmt.Printf("Created space %s (%s).\n", space.Name, space.SpaceID)
	return space.SpaceID, space.Name, nil
}

// chooseProjects collects the allowlist. It is deny-by-default: only turns
// whose working directory sits inside one of these paths are ever pushed.
func chooseProjects(cwd string) ([]string, error) {
	fmt.Println()
	fmt.Println("Only sessions under these directories are pushed. Everything else stays local.")

	var raw string
	prompt := &survey.Input{
		Message: "Project directories (comma-separated):",
		Default: cwd,
	}
	if err := survey.AskOne(prompt, &raw, survey.WithValidator(survey.Required)); err != nil {
		return nil, err
	}

	var projects []string
	for _, part := range splitAndTrim(raw) {
		projects = append(projects, ExpandHome(part))
	}
	if len(projects) == 0 {
		return nil, fmt.Errorf("at least one project directory is required")
	}
	return projects, nil
}
```

Add the two small helpers this uses to the bottom of `cli/internal/memory/credentials.go`:

```go
// splitAndTrim splits a comma-separated list, dropping empty entries.
func splitAndTrim(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// ExpandHome resolves a leading ~/ the same way internal/agents does.
func ExpandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}
```

- [ ] **Step 2: Wire it into connect**

In `cli/internal/commands/connect.go`:

Add the import:

```go
	"github.com/anonalabs/agent-observability/cli/internal/memory"
```

Add the flag variable to the `ConnectCmd` declaration line that already declares `writeShellRcFlag, logUserPromptsFlag, logToolDetailsFlag`:

```go
	var writeShellRcFlag, logUserPromptsFlag, logToolDetailsFlag, memoryFlag *bool
```

Register it alongside the other flags:

```go
	memoryFlag = cmd.Flags().Bool("memory", false, "also push prompts and responses to AnonaMemory (interactive only)")
```

Replace the four direct returns at the end of `RunE`. The current shape is:

```go
			if hookAgent, ok := hookAgents()[agentName]; ok {
				return connectHookAgent(hookAgent, endpoint, yamlConfig, nonInteractive, authTokenFlag)
			}
			if agentName == "opencode" {
				return connectOpenCode(endpoint, yamlConfig, nonInteractive, authTokenFlag)
			}

			spec, ok := reg.Get(agentName)
			if !ok {
				return fmt.Errorf("agent '%s' not found in registry", agentName)
			}

			flagAnswers := map[string]*bool{
				"log_user_prompts": flagOrNil(cmd, "log-user-prompts", logUserPromptsFlag),
				"log_tool_details": flagOrNil(cmd, "log-tool-details", logToolDetailsFlag),
			}

			switch spec.Kind {
			case "json-merge":
				return connectJSONMerge(*spec, endpoint, yamlConfig, nonInteractive, flagAnswers, authTokenFlag)
			case "env":
				return connectEnv(*spec, endpoint, yamlConfig, nonInteractive, flagAnswers, flagOrNil(cmd, "write-shell-rc", writeShellRcFlag), authTokenFlag)
			default:
				return fmt.Errorf("agent %q has unknown kind %q (expected \"env\" or \"json-merge\")", spec.Name, spec.Kind)
			}
```

Becomes — every path assigns to `connectErr`, then one shared tail runs the wizard:

```go
			var connectErr error

			switch {
			case hookAgents()[agentName] != nil:
				connectErr = connectHookAgent(hookAgents()[agentName], endpoint, yamlConfig, nonInteractive, authTokenFlag)
			case agentName == "opencode":
				connectErr = connectOpenCode(endpoint, yamlConfig, nonInteractive, authTokenFlag)
			default:
				spec, ok := reg.Get(agentName)
				if !ok {
					return fmt.Errorf("agent '%s' not found in registry", agentName)
				}

				flagAnswers := map[string]*bool{
					"log_user_prompts": flagOrNil(cmd, "log-user-prompts", logUserPromptsFlag),
					"log_tool_details": flagOrNil(cmd, "log-tool-details", logToolDetailsFlag),
				}

				switch spec.Kind {
				case "json-merge":
					connectErr = connectJSONMerge(*spec, endpoint, yamlConfig, nonInteractive, flagAnswers, authTokenFlag)
				case "env":
					connectErr = connectEnv(*spec, endpoint, yamlConfig, nonInteractive, flagAnswers, flagOrNil(cmd, "write-shell-rc", writeShellRcFlag), authTokenFlag)
				default:
					return fmt.Errorf("agent %q has unknown kind %q (expected \"env\" or \"json-merge\")", spec.Name, spec.Kind)
				}
			}

			if connectErr != nil {
				return connectErr
			}

			// Offered only after telemetry is wired up, so a declined or
			// failed AnonaMemory setup never undoes a successful connect.
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			return memory.OfferConnect(cwd, nonInteractive, flagOrNil(cmd, "memory", memoryFlag))
```

- [ ] **Step 3: Verify it builds and the tests still pass**

Run: `cd cli && go build -o agentobs ./cmd/agentobs && go test ./... && go vet ./...`
Expected: builds, all tests PASS, vet clean.

- [ ] **Step 4: Verify the non-interactive path doesn't prompt**

Run:
```bash
cd cli && ./agentobs connect --agent claude-code --non-interactive --endpoint http://localhost:4317
```
Expected: prints the export lines and next steps, then exits without asking anything about AnonaMemory.

- [ ] **Step 5: Commit**

```bash
git add cli/internal/memory/wizard.go cli/internal/memory/credentials.go cli/internal/commands/connect.go
git commit -m "Offer AnonaMemory setup at the end of connect

Key entry, space pick-or-create, project allowlist, first sync, then the
cron line. Runs after telemetry is wired so a failure here never undoes a
successful connect."
```

---

### Task 9: Documentation

**Files:**
- Create: `docs/anonamemory.md`
- Modify: `README.md`, `CLAUDE.md`

- [ ] **Step 1: Write the user guide**

Create `docs/anonamemory.md`:

```markdown
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

Config lands at `~/.config/agentobs/memory.json`, mode 0600. It holds your API key.

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

One record per turn: the prompt, the response when there is one, and metadata for session id, agent, model, tokens, cost, tool names, working directory, and git branch.

Coverage is uneven, because the agents differ in what they expose:

| Agent | Prompt | Response |
|---|---|---|
| Claude Code | yes | yes — read from `~/.claude/projects/*/*.jsonl` |
| Cursor, Copilot, Codex, OpenCode | yes, if prompt logging is on | no |
| Gemini CLI | yes, if `logPrompts` is on | no |

Only Claude Code writes a session transcript this can read, so it is the only agent that contributes full exchanges today. The rest contribute prompt-only records, tagged `metadata.has_response: false` so you can filter them.

**Those prompt-only records depend on a setting that defaults to off.** Unless you enabled `OTEL_LOG_USER_PROMPTS` (Claude Code), `telemetry.logPrompts` (Gemini CLI), or declined the mask-prompts question (the hook-shim agents), their prompt text was never captured and there is nothing to push. `agentobs memory status` says so rather than reporting an empty sync.

## Privacy

- Off unless you turn it on, per machine.
- The project allowlist is deny-by-default. Turns from directories you did not list never leave.
- Home-directory usernames and email addresses are masked before sending, reusing the same patterns as the hook shim's `--mask-prompts`.
- Conversation text is never written to ClickHouse or Prometheus. It goes from the transcript straight to AnonaMemory.
- `agentobs memory disconnect` removes the local config. Deleting memories already recorded is done in the AnonaMemory dashboard.

## Notes

- AnonaMemory has no OAuth. API key only.
- Writes are async: the API returns a `job_id`, not a memory id.
- Deduplication is local, keyed on turn id, since the batch endpoint has no idempotency key. Deleting `memory.json` and reconnecting can re-push recent turns.
```

- [ ] **Step 2: Add the README entries**

In `README.md`, add a bullet to the Features list after the Agent Leaderboard bullet:

```markdown
- **Optional AnonaMemory push**: opt in at the end of `agentobs connect` and your agents' prompts and responses become a queryable memory layer, scoped to a project allowlist and masked before they leave the machine. See [docs/anonamemory.md](docs/anonamemory.md).
```

And a row to the Docs table, after the `docs/other-agents.md` row:

```markdown
| [docs/anonamemory.md](docs/anonamemory.md) | Push prompts + responses to AnonaMemory |
```

- [ ] **Step 3: Add the CLAUDE.md entries**

In `CLAUDE.md`, add to the Commands section after the `config-alerts` line:

```
./cli/agentobs memory sync        # push turns to AnonaMemory (opt-in, see docs/anonamemory.md)
```

And add a subsection at the end of the Architecture section, before "Invariants worth preserving":

```markdown
### AnonaMemory connector (`cli/internal/memory/`)

Optional, opt-in, and off by default. `Sync` gathers turns from two readers — `ClaudeCodeSource` parses `~/.claude/projects/*/*.jsonl` for paired prompt+response text, `ClickHouse` supplies prompt-only turns for every other agent plus per-session cost — then filters by a deny-by-default project allowlist, masks with `MaskText`, and writes them in chunks of 100 to `POST /v1/record/batch`.

Conversation text deliberately never enters ClickHouse: transcripts are read at sync time and go straight to the API. Dedup is local (`Credentials.RecentTurns`, a 24h window) because the batch endpoint has no idempotency key. A failed write leaves the watermark unmoved so the next run retries; a ClickHouse outage degrades the run rather than ending it.
```

- [ ] **Step 4: Verify the docs match the code**

Run: `cd cli && ./agentobs memory --help && ./agentobs memory sync --help`
Expected: the flags shown match those documented in `docs/anonamemory.md` (`--quiet`, `--dry-run`, `--since`).

- [ ] **Step 5: Commit**

```bash
git add docs/anonamemory.md README.md CLAUDE.md
git commit -m "Document the AnonaMemory connector

Covers setup, the sync commands, and the uneven response coverage --
only Claude Code writes a readable transcript, so everything else is
prompt-only and depends on a prompt-logging setting that defaults off."
```

---

## Verification

After Task 9, the whole feature should hold together:

```bash
cd cli && go build -o agentobs ./cmd/agentobs && go test ./... && go vet ./...
```

Manual end-to-end, requiring a real AnonaMemory API key:

```bash
./cli/agentobs connect --agent claude-code   # answer yes at the AnonaMemory prompt
./cli/agentobs memory status
./cli/agentobs memory sync --dry-run
./cli/agentobs memory sync
```

Then confirm in the AnonaMemory dashboard that records landed in the chosen space with `metadata.agent_id` set and `metadata.has_response` true for Claude Code turns.
