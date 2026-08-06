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
//
// The second return value counts rows whose ts didn't parse against
// clickHouseTimeLayout and were dropped entirely, since a Turn without a
// timestamp can't be represented. This package has no logger and must not
// print (a later task owns user-facing output), so the count is the only
// way a caller can notice that ClickHouse's DateTime64 rendering drifted
// and rows are silently vanishing from every sync.
func (ch ClickHouse) PromptOnlyTurns(since time.Time) ([]Turn, int, error) {
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
			CWD:       row["cwd"],
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
