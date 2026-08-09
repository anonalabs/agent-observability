package memory

import (
	"sort"
	"time"
)

// Enricher supplies the ClickHouse-derived parts of a sync. It is an
// interface so tests can run without a ClickHouse, and so a failure to
// reach it degrades the sync instead of ending it. The int returns count
// ClickHouse rows that could not be used -- see SkippedRows on Result.
type Enricher interface {
	PromptOnlyTurns(since time.Time) ([]Turn, int, error)
	SessionStats(since time.Time) (map[string]SessionStats, int, error)
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
	Pushed   int
	Deduped  int
	Filtered int
	// SkippedFiles counts transcript files that could not be read or
	// parsed by a TranscriptSource.
	SkippedFiles int
	// SkippedRows counts ClickHouse rows that could not be used -- a
	// non-numeric cost/token field, or a timestamp that failed to parse --
	// as returned by the Enricher's PromptOnlyTurns and SessionStats. This
	// is distinct from SkippedFiles, which counts unreadable transcript
	// files rather than unusable database rows.
	SkippedRows int
	// EnrichErr and PromptOnlyErr record a degraded run: the turns that
	// were available still went out, without ClickHouse's contribution.
	EnrichErr     error
	PromptOnlyErr error
	// SyncErr is set by SyncAll when this project's own sync failed, so a
	// caller iterating results can report which spaces are broken without
	// the whole run being an error.
	SyncErr error
}

// SyncProject gathers turns from every source, filters them by allowlist and
// watermark, masks them, enriches them, and writes them. It mutates p's
// watermark on success but does not persist -- the caller decides that, so
// a dry run leaves the config file alone.
func SyncProject(p *Project, rec Recorder, sources []TranscriptSource, enricher Enricher, opts Options) (Result, error) {
	var result Result

	since := p.Watermark
	if opts.Since != nil {
		since = *opts.Since
	}

	// Sources are queried from a point dedupWindow earlier than the
	// watermark, not the watermark itself. Sources have very different
	// latencies -- a transcript line lands on disk instantly, the
	// equivalent hook-shim span reaches ClickHouse only after OTLP batching
	// plus insert -- so a strict watermark cutoff permanently loses
	// anything that was still in flight when the watermark last advanced.
	// Re-reading the trailing window and relying on Credentials.Seen to
	// reject what was already pushed is what makes that window (and
	// RecentTurns) do anything at all. A zero watermark means "never
	// synced" and must stay zero -- subtracting from it would produce a
	// non-zero time.Time that accidentally starts filtering.
	//
	// This lookback applies only to the watermark path, not to an explicit
	// opts.Since: --since names a manual backfill window the user chose on
	// purpose ("everything from the last 7 days"), and it doesn't have a
	// "turn still in flight when it was set" problem to correct for -- there
	// was no prior sync run whose watermark it's catching up on. Silently
	// widening it by dedupWindow would fetch (and upload to a third party)
	// more than the user asked for.
	queryFrom := since
	if opts.Since == nil && !since.IsZero() {
		queryFrom = since.Add(-dedupWindow)
	}

	var candidates []Turn

	for _, source := range sources {
		turns, skipped, err := source.Turns(queryFrom)
		if err != nil {
			return result, err
		}
		result.SkippedFiles += skipped
		candidates = append(candidates, turns...)
	}

	if enricher != nil {
		promptOnly, skipped, err := enricher.PromptOnlyTurns(queryFrom)
		if err != nil {
			// Only count skipped rows for a call that actually succeeded --
			// an Enricher that errors is not guaranteed to report a
			// meaningful skipped count alongside that error, so folding it
			// in here could corrupt the total.
			result.PromptOnlyErr = err
		} else {
			result.SkippedRows += skipped
			candidates = append(candidates, promptOnly...)
		}
	}

	var stats map[string]SessionStats
	if enricher != nil {
		s, skipped, err := enricher.SessionStats(queryFrom)
		if err != nil {
			result.EnrichErr = err
		} else {
			result.SkippedRows += skipped
			stats = s
		}
	}

	var selected []Turn
	for _, turn := range candidates {
		if !p.AllowsPath(turn.CWD) {
			result.Filtered++
			continue
		}
		if p.Seen(turn.TurnID) {
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

	accepted, err := rec.RecordBatch(p.SpaceID, items)
	if err != nil {
		result.Pushed = accepted
		// RecordBatch writes in chunks of up to 100 and returns how many
		// items landed before the failing chunk. selected is sorted oldest
		// first (see above), so items[:accepted] is exactly the contiguous
		// prefix the server already accepted. Advancing the watermark and
		// RecentTurns over that prefix -- even though the overall call is
		// erroring -- is what stops the next run from re-pushing chunks
		// that already succeeded; nothing after it is marked, so the next
		// run naturally retries from the failure point. This is safe only
		// because since is now queried with a trailing lookback (see
		// above): re-marking a turn "seen" here doesn't risk losing it if a
		// slower source's copy of the same turn shows up later.
		if accepted > len(selected) {
			// Defensive only -- a well-behaved Recorder never accepts more
			// than it was given -- but slicing past len(selected) would
			// panic, and this is watermark-advancing code that must not.
			accepted = len(selected)
		}
		if accepted > 0 {
			acceptedSynced := make(map[string]time.Time, accepted)
			for _, turn := range selected[:accepted] {
				acceptedSynced[turn.TurnID] = turn.Timestamp
			}
			p.MarkSynced(acceptedSynced)
		}
		return result, err
	}

	p.MarkSynced(synced)
	return result, nil
}

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
