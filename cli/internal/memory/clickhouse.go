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

// firstWorkspaceRoot extracts the first entry from workspace_roots, which
// the hook shim (cursorhook.addCommonAttributes, called on every span it
// emits, including prompt spans) stamps as a JSON array serialized to a
// string — e.g. `["/home/dev/repo"]` — never a bare path. Extraction is
// done here in Go, after the row comes back, rather than in SQL with
// ClickHouse's JSONExtractString: this package's tests run against a mocked
// HTTP server that hands back canned rows directly, with no live
// ClickHouse to execute a query against, so a Go helper is the only form of
// this extraction that can actually be exercised by a unit test. A missing
// or malformed value yields an empty cwd — turns without a workspace root
// are correctly filtered out downstream by Credentials.AllowsPath's
// deny-by-default, and no fallback is invented here.
func firstWorkspaceRoot(raw string) string {
	if raw == "" {
		return ""
	}
	var roots []string
	if err := json.Unmarshal([]byte(raw), &roots); err != nil {
		return ""
	}
	if len(roots) == 0 {
		return ""
	}
	return roots[0]
}

// PromptOnlyTurns returns prompts for every agent except Claude Code, whose
// turns come from transcripts with real response text. Only otel_traces
// (the hook-shim agents) is queried: otel_logs' event.name-based prompt
// rows (Gemini CLI's telemetry) carry no directory attribute anywhere, so
// every turn from that branch would always fail the downstream project
// allowlist — keeping it would mean shipping a branch that provably never
// contributes a synced turn.
//
// The second return value counts rows whose ts didn't parse against
// clickHouseTimeLayout and were dropped entirely, since a Turn without a
// timestamp can't be represented. This package has no logger and must not
// print (a later task owns user-facing output), so the count is the only
// way a caller can notice that ClickHouse's DateTime64 rendering drifted
// and rows are silently vanishing from every sync.
func (ch ClickHouse) PromptOnlyTurns(since time.Time) ([]Turn, int, error) {
	sql := fmt.Sprintf(`
SELECT
  toString(Timestamp) AS ts,
  ServiceName AS agent,
  SpanAttributes['gen_ai.prompt.0.content'] AS prompt,
  SpanAttributes['langsmith.trace.session_id'] AS session,
  SpanAttributes['gen_ai.request.model'] AS model,
  SpanAttributes['langsmith.metadata.workspace_roots'] AS workspace_roots
FROM otel.otel_traces
WHERE SpanAttributes['gen_ai.prompt.0.content'] NOT IN ('', '[MASKED]')
  AND ServiceName != 'claude-code'
  AND %s
ORDER BY ts ASC
FORMAT JSONEachRow`,
		sinceClause("Timestamp", since),
	)

	rows, err := ch.query(sql)
	if err != nil {
		return nil, 0, err
	}

	var turns []Turn
	skipped := 0
	for _, row := range rows {
		timestamp, err := time.Parse(clickHouseTimeLayout, row["ts"])
		if err != nil {
			skipped++
			continue
		}
		turns = append(turns, Turn{
			Agent:     row["agent"],
			SessionID: row["session"],
			TurnID:    promptTurnID(row["agent"], row["session"], row["ts"], row["prompt"]),
			Timestamp: timestamp.UTC(),
			Prompt:    row["prompt"],
			Model:     row["model"],
			CWD:       firstWorkspaceRoot(row["workspace_roots"]),
		})
	}
	return turns, skipped, nil
}

// SessionStats sums cost and tokens per session from Claude Code's
// api_request events, used to enrich turns that already have their text.
//
// The second return value counts rows where cost, input_tokens, or
// output_tokens failed to parse as numbers. Such a row is still kept in the
// result (with the unparseable field defaulting to zero) rather than
// dropping the whole session's stats, since a session with a wrong cost is
// more useful than one silently missing altogether — but the count still
// registers so a caller can surface that some enrichment data is suspect.
func (ch ClickHouse) SessionStats(since time.Time) (map[string]SessionStats, int, error) {
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
		return nil, 0, err
	}

	stats := map[string]SessionStats{}
	skipped := 0
	for _, row := range rows {
		cost, costErr := strconv.ParseFloat(row["cost"], 64)
		input, inputErr := strconv.Atoi(row["input_tokens"])
		output, outputErr := strconv.Atoi(row["output_tokens"])
		if costErr != nil || inputErr != nil || outputErr != nil {
			skipped++
		}
		stats[row["session"]] = SessionStats{
			CostUSD:      cost,
			InputTokens:  input,
			OutputTokens: output,
		}
	}
	return stats, skipped, nil
}
