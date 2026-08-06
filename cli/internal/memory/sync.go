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
		promptOnly, skipped, err := enricher.PromptOnlyTurns(since)
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
		s, skipped, err := enricher.SessionStats(since)
		if err != nil {
			result.EnrichErr = err
		} else {
			result.SkippedRows += skipped
			stats = s
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
