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
			`{"ts":"2026-08-06 12:00:00.000000000","agent":"cursor-agent","prompt":"refactor this","session":"sess-9","model":"claude-opus-5","workspace_roots":"[\"/home/dev/repo\"]"}`,
			`{"ts":"2026-08-06 12:05:00.000000000","agent":"windsurf-agent","prompt":"explain","session":"sess-10","model":""}`,
		}, "\n"))
	}))
	defer srv.Close()

	ch := ClickHouse{BaseURL: srv.URL, HTTP: srv.Client()}
	turns, skipped, err := ch.PromptOnlyTurns(time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}

	if !strings.Contains(gotQuery, "claude-code") {
		t.Error("query must exclude claude-code, whose turns come from transcripts")
	}
	if !strings.Contains(gotQuery, "[MASKED]") {
		t.Error("query must exclude prompts the hook shim already masked")
	}
	if !strings.Contains(gotQuery, "otel_traces") {
		t.Error("query must read from otel_traces, the hook-shim agents' table")
	}
	if strings.Contains(gotQuery, "otel_logs") {
		t.Error("query must not read from otel_logs: its rows carry no directory attribute and can never pass the allowlist")
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
	if turns[1].CWD != "" {
		t.Errorf("turn 1 cwd = %q, want empty: its span carries no workspace_roots", turns[1].CWD)
	}
}

func TestPromptOnlyTurnsCWDTakesFirstWorkspaceRoot(t *testing.T) {
	row := `{"ts":"2026-08-06 12:00:00.000000000","agent":"cursor-agent","prompt":"refactor this","session":"sess-9","model":"","workspace_roots":"[\"/home/dev/repo\",\"/home/dev/other\"]"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, row)
	}))
	defer srv.Close()

	ch := ClickHouse{BaseURL: srv.URL, HTTP: srv.Client()}
	turns, _, err := ch.PromptOnlyTurns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}
	if turns[0].CWD != "/home/dev/repo" {
		t.Errorf("cwd = %q, want first entry /home/dev/repo", turns[0].CWD)
	}
}

func TestPromptOnlyTurnsTurnIDIsStable(t *testing.T) {
	row := `{"ts":"2026-08-06 12:00:00.000000000","agent":"cursor-agent","prompt":"same","session":"sess-9","model":"","cwd":""}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, row)
	}))
	defer srv.Close()

	ch := ClickHouse{BaseURL: srv.URL, HTTP: srv.Client()}
	first, _, err := ch.PromptOnlyTurns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, _, err := ch.PromptOnlyTurns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first[0].TurnID != second[0].TurnID {
		t.Errorf("turn id must be deterministic: %q vs %q", first[0].TurnID, second[0].TurnID)
	}
}

func TestPromptOnlyTurnsCountsUnparseableTimestamps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Join([]string{
			`{"ts":"not-a-timestamp","agent":"cursor-agent","prompt":"refactor this","session":"sess-9","model":"","cwd":""}`,
			`{"ts":"2026-08-06 12:05:00.000000000","agent":"gemini-cli","prompt":"explain","session":"sess-10","model":"","cwd":""}`,
		}, "\n"))
	}))
	defer srv.Close()

	ch := ClickHouse{BaseURL: srv.URL, HTTP: srv.Client()}
	turns, skipped, err := ch.PromptOnlyTurns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}
	if turns[0].SessionID != "sess-10" {
		t.Errorf("turn 0 = %+v, want the row with a valid timestamp", turns[0])
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
	stats, skipped, err := ch.SessionStats(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
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

func TestSessionStatsCountsUnparseableNumbers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Join([]string{
			`{"session":"sess-1","cost":"not-a-number","input_tokens":"300","output_tokens":"120"}`,
			`{"session":"sess-2","cost":"0.0100","input_tokens":"50","output_tokens":"20"}`,
		}, "\n"))
	}))
	defer srv.Close()

	ch := ClickHouse{BaseURL: srv.URL, HTTP: srv.Client()}
	stats, skipped, err := ch.SessionStats(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
	// The row is kept, with the unparseable field defaulting to zero, rather
	// than dropping the whole session's stats.
	if len(stats) != 2 {
		t.Fatalf("got %d sessions, want 2", len(stats))
	}
	if stats["sess-1"].CostUSD != 0 {
		t.Errorf("sess-1 cost = %v, want 0", stats["sess-1"].CostUSD)
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
	if _, _, err := ch.SessionStats(time.Time{}); err == nil {
		t.Fatal("expected an error, got nil")
	}
}
