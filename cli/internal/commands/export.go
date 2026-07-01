package commands

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	prometheusURL  = "http://localhost:9090"
	clickhouseHost = "localhost"
	clickhousePort = 8123
)

var durationUnits = map[byte]int{'s': 1, 'm': 60, 'h': 3600, 'd': 86400}

func parseSince(since string) (int, error) {
	if since == "" {
		return 0, fmt.Errorf("unrecognized duration ''")
	}
	unit := since[len(since)-1]
	mult, ok := durationUnits[unit]
	if !ok {
		return 0, fmt.Errorf("unrecognized duration '%s', expected e.g. 24h, 30m, 1d", since)
	}
	n, err := strconv.Atoi(since[:len(since)-1])
	if err != nil {
		return 0, fmt.Errorf("unrecognized duration '%s', expected e.g. 24h, 30m, 1d", since)
	}
	return n * mult, nil
}

func fetchMetrics(seconds int) (interface{}, error) {
	now := time.Now()
	start := now.Add(-time.Duration(seconds) * time.Second)

	q := url.Values{}
	q.Set("query", `sum by (type) (rate(claude_code_token_usage_tokens_total[5m]))`)
	q.Set("start", fmt.Sprintf("%d", start.Unix()))
	q.Set("end", fmt.Sprintf("%d", now.Unix()))
	q.Set("step", "60s")

	resp, err := http.Get(prometheusURL + "/api/v1/query_range?" + q.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("prometheus returned HTTP %d", resp.StatusCode)
	}

	var payload struct {
		Data struct {
			Result interface{} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Data.Result == nil {
		return []interface{}{}, nil
	}
	return payload.Data.Result, nil
}

func fetchLogs(seconds int) ([]map[string]interface{}, error) {
	query := fmt.Sprintf(
		"SELECT Timestamp, ServiceName, Body FROM otel.otel_logs "+
			"WHERE Timestamp > now() - INTERVAL %d SECOND "+
			"ORDER BY Timestamp DESC FORMAT JSONEachRow", seconds,
	)
	resp, err := http.Post(fmt.Sprintf("http://%s:%d/", clickhouseHost, clickhousePort), "text/plain", strings.NewReader(query))
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

	logs := []map[string]interface{}{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row map[string]interface{}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, err
		}
		logs = append(logs, row)
	}
	return logs, nil
}

func ExportCmd() *cobra.Command {
	var since, format, out string

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Pull metrics (Prometheus) and events (ClickHouse) out to a file",
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "json" && format != "csv" {
				return fmt.Errorf("--format must be 'json' or 'csv'")
			}

			seconds, err := parseSince(since)
			if err != nil {
				return err
			}

			metrics, err := fetchMetrics(seconds)
			if err != nil {
				fmt.Printf("Couldn't reach Prometheus: %v\n", err)
				metrics = []interface{}{}
			}

			logs, err := fetchLogs(seconds)
			if err != nil {
				fmt.Printf("Couldn't reach ClickHouse: %v\n", err)
				logs = []map[string]interface{}{}
			}

			if format == "json" {
				payload := map[string]interface{}{"metrics": metrics, "logs": logs}
				data, err := json.MarshalIndent(payload, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(out, data, 0o644); err != nil {
					return err
				}
			} else {
				var buf bytes.Buffer
				w := csv.NewWriter(&buf)
				w.Write([]string{"kind", "record"})
				if metricsList, ok := metrics.([]interface{}); ok {
					for _, m := range metricsList {
						b, _ := json.Marshal(m)
						w.Write([]string{"metric", string(b)})
					}
				}
				for _, l := range logs {
					b, _ := json.Marshal(l)
					w.Write([]string{"log", string(b)})
				}
				w.Flush()
				if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
					return err
				}
			}

			metricCount := 0
			if metricsList, ok := metrics.([]interface{}); ok {
				metricCount = len(metricsList)
			}
			fmt.Printf("Wrote %d metric series and %d log rows to %s\n", metricCount, len(logs), out)
			return nil
		},
	}

	cmd.Flags().StringVar(&since, "since", "24h", "")
	cmd.Flags().StringVar(&format, "format", "json", "json or csv")
	cmd.Flags().StringVar(&out, "out", "./export.json", "")

	return cmd
}
