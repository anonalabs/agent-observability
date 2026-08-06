package memory

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- fakes ---

// fakeSource records every "since" it was called with, in addition to
// filtering like a real TranscriptSource would. Tests use sinceCalls to
// assert Sync queries a trailing lookback window rather than the raw
// watermark (finding C1) -- a fake that silently discarded the argument,
// as this one used to, can't catch a regression back to no lookback at all.
type fakeSource struct {
	turns      []Turn
	skipped    int
	sinceCalls []time.Time
}

func (*fakeSource) Name() string { return "fake" }
func (f *fakeSource) Turns(since time.Time) ([]Turn, int, error) {
	f.sinceCalls = append(f.sinceCalls, since)
	var out []Turn
	for _, turn := range f.turns {
		if since.IsZero() || turn.Timestamp.After(since) {
			out = append(out, turn)
		}
	}
	return out, f.skipped, nil
}

// partialBatchRecorder simulates RecordBatch's documented contract for a
// multi-chunk write where an early chunk is accepted and a later one fails:
// it returns (accepted, err) directly, the same shape *Client.RecordBatch
// returns after internally chunking -- Recorder callers never see the
// chunking itself, only this pair.
type fakeRecorder struct {
	items    []RecordItem
	err      error
	accepted int // used only when err != nil; a nil err always accepts everything
}

func (r *fakeRecorder) RecordBatch(spaceID string, items []RecordItem) (int, error) {
	if r.err != nil {
		accepted := r.accepted
		if accepted > len(items) {
			accepted = len(items)
		}
		r.items = append(r.items, items[:accepted]...)
		return accepted, r.err
	}
	r.items = append(r.items, items...)
	return len(items), nil
}

// fakeEnricher records every "since" it was called with -- see fakeSource's
// comment; the original version of this fake dropped the argument entirely,
// which is why the missing-lookback bug in Sync survived per-task review.
type fakeEnricher struct {
	promptOnly    []Turn
	promptSkipped int
	stats         map[string]SessionStats
	statsSkipped  int
	promptErr     error
	statsErr      error

	promptSinceCalls []time.Time
	statsSinceCalls  []time.Time
}

func (e *fakeEnricher) PromptOnlyTurns(since time.Time) ([]Turn, int, error) {
	e.promptSinceCalls = append(e.promptSinceCalls, since)
	return e.promptOnly, e.promptSkipped, e.promptErr
}
func (e *fakeEnricher) SessionStats(since time.Time) (map[string]SessionStats, int, error) {
	e.statsSinceCalls = append(e.statsSinceCalls, since)
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
	src := &fakeSource{turns: []Turn{
		turnAt("in", "/home/dev/repo/cli", now),
		turnAt("out", "/home/dev/elsewhere", now),
	}}

	result, err := Sync(creds, rec, []TranscriptSource{src}, &fakeEnricher{}, Options{})
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
	src := &fakeSource{turns: []Turn{
		turnAt("seen", "/home/dev/repo", now),
		turnAt("fresh", "/home/dev/repo", now.Add(time.Minute)),
	}}

	result, err := Sync(creds, rec, []TranscriptSource{src}, &fakeEnricher{}, Options{})
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

	if _, err := Sync(creds, rec, []TranscriptSource{&fakeSource{turns: []Turn{turn}}}, &fakeEnricher{}, Options{}); err != nil {
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
	enricher := &fakeEnricher{stats: map[string]SessionStats{
		"sess-1": {CostUSD: 0.25, InputTokens: 900, OutputTokens: 300},
	}}

	src := &fakeSource{turns: []Turn{turnAt("t1", "/home/dev/repo", now)}}
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
	enricher := &fakeEnricher{
		statsErr:  errors.New("clickhouse down"),
		promptErr: errors.New("clickhouse down"),
	}

	src := &fakeSource{turns: []Turn{turnAt("t1", "/home/dev/repo", now)}}
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
	src := &fakeSource{turns: []Turn{turnAt("t1", "/home/dev/repo", now)}}

	result, err := Sync(creds, rec, []TranscriptSource{src}, &fakeEnricher{}, Options{DryRun: true})
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
	src := &fakeSource{turns: []Turn{turnAt("t1", "/home/dev/repo", now)}}

	if _, err := Sync(creds, rec, []TranscriptSource{src}, &fakeEnricher{}, Options{}); err == nil {
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

	result, err := Sync(creds, rec, []TranscriptSource{&fakeSource{skipped: 3}}, &fakeEnricher{}, Options{})
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

	if _, err := Sync(creds, rec, []TranscriptSource{&fakeSource{turns: []Turn{turn}}}, &fakeEnricher{}, Options{}); err != nil {
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
	enricher := &fakeEnricher{promptSkipped: 2, statsSkipped: 5}

	src := &fakeSource{turns: []Turn{turnAt("t1", "/home/dev/repo", now)}}
	result, err := Sync(creds, rec, []TranscriptSource{src}, enricher, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SkippedRows != 7 {
		t.Errorf("skipped rows = %d, want 7 (2 from PromptOnlyTurns + 5 from SessionStats)", result.SkippedRows)
	}
}

// TestSyncQueriesSourcesWithLookbackWindow is finding C1's core regression
// test: Sync must not query sources with the raw watermark, or anything
// that landed at or before it (a slower source's copy of a turn a faster
// source already advanced the watermark past, or a second concurrent
// session) is lost forever and RecentTurns/Seen never gets a chance to
// dedup it.
func TestSyncQueriesSourcesWithLookbackWindow(t *testing.T) {
	watermark := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	creds := &Credentials{SpaceID: "spc_a1", Projects: []string{"/home/dev/repo"}, Watermark: watermark}
	rec := &fakeRecorder{}
	src := &fakeSource{}
	enricher := &fakeEnricher{}

	if _, err := Sync(creds, rec, []TranscriptSource{src}, enricher, Options{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantSince := watermark.Add(-dedupWindow)
	if len(src.sinceCalls) != 1 || !src.sinceCalls[0].Equal(wantSince) {
		t.Errorf("transcript source queried with since=%v, want %v (watermark - dedupWindow)", src.sinceCalls, wantSince)
	}
	if len(enricher.promptSinceCalls) != 1 || !enricher.promptSinceCalls[0].Equal(wantSince) {
		t.Errorf("PromptOnlyTurns queried with since=%v, want %v", enricher.promptSinceCalls, wantSince)
	}
	if len(enricher.statsSinceCalls) != 1 || !enricher.statsSinceCalls[0].Equal(wantSince) {
		t.Errorf("SessionStats queried with since=%v, want %v", enricher.statsSinceCalls, wantSince)
	}
}

// TestSyncNeverSyncedQueriesWithZeroSince guards the zero-watermark case: a
// never-synced install must still request "everything" (zero time), not
// dedupWindow-before-the-zero-value, which is a different, non-zero
// time.Time that would start filtering things out.
func TestSyncNeverSyncedQueriesWithZeroSince(t *testing.T) {
	creds := &Credentials{SpaceID: "spc_a1", Projects: []string{"/home/dev/repo"}}
	rec := &fakeRecorder{}
	src := &fakeSource{}

	if _, err := Sync(creds, rec, []TranscriptSource{src}, &fakeEnricher{}, Options{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(src.sinceCalls) != 1 || !src.sinceCalls[0].IsZero() {
		t.Errorf("since = %v, want the zero value on a never-synced install", src.sinceCalls)
	}
}

// TestSyncExplicitSinceSkipsLookback pins the fix for the regression the
// lookback introduced: --since is a user-named manual-backfill window
// ("everything from the last 7 days"), not a watermark that a slower
// source might still be catching up on, so widening it by dedupWindow would
// silently fetch -- and upload to a third party -- more than the user
// asked for. Only the stored-watermark path gets the lookback.
func TestSyncExplicitSinceSkipsLookback(t *testing.T) {
	explicitSince := time.Date(2026, 8, 6, 11, 0, 0, 0, time.UTC)
	creds := &Credentials{SpaceID: "spc_a1", Projects: []string{"/home/dev/repo"}}
	rec := &fakeRecorder{}
	src := &fakeSource{}
	enricher := &fakeEnricher{}

	if _, err := Sync(creds, rec, []TranscriptSource{src}, enricher, Options{Since: &explicitSince}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(src.sinceCalls) != 1 || !src.sinceCalls[0].Equal(explicitSince) {
		t.Errorf("transcript source queried with since=%v, want the explicit %v with no lookback subtracted", src.sinceCalls, explicitSince)
	}
	if len(enricher.promptSinceCalls) != 1 || !enricher.promptSinceCalls[0].Equal(explicitSince) {
		t.Errorf("PromptOnlyTurns queried with since=%v, want %v", enricher.promptSinceCalls, explicitSince)
	}
	if len(enricher.statsSinceCalls) != 1 || !enricher.statsSinceCalls[0].Equal(explicitSince) {
		t.Errorf("SessionStats queried with since=%v, want %v", enricher.statsSinceCalls, explicitSince)
	}
}

// TestSyncExplicitSinceExcludesTurnsOutsideTheNamedWindow is the end-to-end
// version of the regression: a fresh install (zero watermark) run with
// --since 1h must not pick up a turn from 20 hours ago just because that
// falls inside dedupWindow's 24h span -- --since names the window itself.
func TestSyncExplicitSinceExcludesTurnsOutsideTheNamedWindow(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	since1h := now.Add(-1 * time.Hour)
	creds := &Credentials{SpaceID: "spc_a1", Projects: []string{"/home/dev/repo"}}
	rec := &fakeRecorder{}
	old := turnAt("old", "/home/dev/repo", now.Add(-20*time.Hour))
	src := &fakeSource{turns: []Turn{old}}

	result, err := Sync(creds, rec, []TranscriptSource{src}, &fakeEnricher{}, Options{Since: &since1h})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Pushed != 0 {
		t.Errorf("pushed = %d, want 0 -- a turn from 20h ago is outside an explicit --since 1h window", result.Pushed)
	}
	if len(rec.items) != 0 {
		t.Errorf("recorded %d items, want 0", len(rec.items))
	}
}

// TestSyncRePushesLateArrivingTurnBelowWatermark is C1's end-to-end proof:
// a turn whose timestamp is at or below the watermark, and that was never
// previously synced, must still go out on a later run -- this is the "two
// concurrent Claude Code sessions" and "slow ClickHouse insert" scenario
// from the finding, and the reason RecentTurns/Seen exist at all.
func TestSyncRePushesLateArrivingTurnBelowWatermark(t *testing.T) {
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	creds := &Credentials{
		SpaceID:  "spc_a1",
		Projects: []string{"/home/dev/repo"},
		// The watermark already advanced past "late" from an earlier,
		// faster-arriving turn -- but "late" itself was never pushed.
		Watermark: base,
	}
	rec := &fakeRecorder{}
	late := turnAt("late", "/home/dev/repo", base.Add(-30*time.Minute))
	src := &fakeSource{turns: []Turn{late}}

	result, err := Sync(creds, rec, []TranscriptSource{src}, &fakeEnricher{}, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Pushed != 1 {
		t.Fatalf("pushed = %d, want 1 (the late-arriving turn)", result.Pushed)
	}
	if len(rec.items) != 1 || rec.items[0].Metadata["turn_id"] != "late" {
		t.Errorf("recorded items = %+v, want the late turn pushed", rec.items)
	}
}

// TestSyncPartialBatchFailureMarksAcceptedPrefixSynced is finding I1's
// regression test. RecordBatch's contract (client.go) is that on error it
// still returns how many items landed before the failing chunk; a first run
// that accepts 100 of 150 turns and then hard-fails must not force a second
// run to re-push those 100, since AnonaMemory has no server-side
// idempotency key and would record them twice.
func TestSyncPartialBatchFailureMarksAcceptedPrefixSynced(t *testing.T) {
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	creds := &Credentials{SpaceID: "spc_a1", Projects: []string{"/home/dev/repo"}}

	const total = 150
	const acceptedInRun1 = 100
	turns := make([]Turn, total)
	for i := 0; i < total; i++ {
		turns[i] = turnAt(fmt.Sprintf("t%03d", i), "/home/dev/repo", base.Add(time.Duration(i)*time.Minute))
	}

	// Run 1: the first 100 (oldest-first) are accepted, the rest hard-fail.
	run1Rec := &fakeRecorder{err: errors.New("400 body too large"), accepted: acceptedInRun1}
	run1Src := &fakeSource{turns: turns}

	result, err := Sync(creds, run1Rec, []TranscriptSource{run1Src}, &fakeEnricher{}, Options{})
	if err == nil {
		t.Fatal("expected run 1's hard failure to surface")
	}
	if result.Pushed != acceptedInRun1 {
		t.Fatalf("run 1 pushed = %d, want %d", result.Pushed, acceptedInRun1)
	}
	if len(run1Rec.items) != acceptedInRun1 {
		t.Fatalf("run 1 recorded %d items, want %d", len(run1Rec.items), acceptedInRun1)
	}
	for i := 0; i < acceptedInRun1; i++ {
		if !creds.Seen(fmt.Sprintf("t%03d", i)) {
			t.Errorf("t%03d was accepted in run 1 and should be marked seen", i)
		}
	}
	if creds.Watermark.IsZero() {
		t.Fatal("run 1's accepted prefix should have advanced the watermark")
	}

	// Run 2: a fresh recorder that accepts everything offered. The source
	// still has all 150 turns (a real ClaudeCodeSource re-reads its file
	// from wherever queryFrom lands); only the un-accepted 50 should
	// actually go out.
	run2Rec := &fakeRecorder{}
	run2Src := &fakeSource{turns: turns}

	if _, err := Sync(creds, run2Rec, []TranscriptSource{run2Src}, &fakeEnricher{}, Options{}); err != nil {
		t.Fatalf("run 2: unexpected error: %v", err)
	}

	wantRun2 := total - acceptedInRun1
	if len(run2Rec.items) != wantRun2 {
		t.Fatalf("run 2 recorded %d items, want %d (the un-accepted remainder)", len(run2Rec.items), wantRun2)
	}
	for _, item := range run2Rec.items {
		id, _ := item.Metadata["turn_id"].(string)
		var idx int
		if _, err := fmt.Sscanf(id, "t%03d", &idx); err != nil {
			t.Fatalf("unparseable turn id %q: %v", id, err)
		}
		if idx < acceptedInRun1 {
			t.Errorf("run 2 re-pushed %q, which run 1 already got accepted", id)
		}
	}
}
