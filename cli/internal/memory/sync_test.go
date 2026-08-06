package memory

import (
	"errors"
	"strings"
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
	promptOnly    []Turn
	promptSkipped int
	stats         map[string]SessionStats
	statsSkipped  int
	promptErr     error
	statsErr      error
}

func (e fakeEnricher) PromptOnlyTurns(time.Time) ([]Turn, int, error) {
	return e.promptOnly, e.promptSkipped, e.promptErr
}
func (e fakeEnricher) SessionStats(time.Time) (map[string]SessionStats, int, error) {
	return e.stats, e.statsSkipped, e.statsErr
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

func TestSyncMasksGitBranch(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	creds := &Credentials{SpaceID: "spc_a1", Projects: []string{"/home/dev/repo"}}
	rec := &fakeRecorder{}
	turn := turnAt("t1", "/home/dev/repo", now)
	turn.GitBranch = "wip-/home/bob/personal-branch"

	if _, err := Sync(creds, rec, []TranscriptSource{fakeSource{turns: []Turn{turn}}}, fakeEnricher{}, Options{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(rec.items[0].Context, "bob") {
		t.Errorf("context = %q, want the git branch masked", rec.items[0].Context)
	}
	if branch, _ := rec.items[0].Metadata["git_branch"].(string); strings.Contains(branch, "bob") {
		t.Errorf("metadata git_branch = %q, want it masked", branch)
	}
}

func TestSyncCountsSkippedRows(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	creds := &Credentials{SpaceID: "spc_a1", Projects: []string{"/home/dev/repo"}}
	rec := &fakeRecorder{}
	enricher := fakeEnricher{promptSkipped: 2, statsSkipped: 5}

	src := fakeSource{turns: []Turn{turnAt("t1", "/home/dev/repo", now)}}
	result, err := Sync(creds, rec, []TranscriptSource{src}, enricher, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SkippedRows != 7 {
		t.Errorf("skipped rows = %d, want 7 (2 from PromptOnlyTurns + 5 from SessionStats)", result.SkippedRows)
	}
}
