package commands

import (
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

var healthChecks = []struct {
	Name string
	URL  string
}{
	{"otel-collector", "http://localhost:4318/v1/logs"},
	{"prometheus", "http://localhost:9090/-/healthy"},
	{"clickhouse", "http://localhost:8123/ping"},
	{"grafana", "http://localhost:3000/api/health"},
}

func isUp(url string) bool {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500
}

func StatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Health-check the collector/prometheus/clickhouse/grafana stack",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("%-16s %s\n", "SERVICE", "STATUS")
			for _, check := range healthChecks {
				status := "down"
				if isUp(check.URL) {
					status = "up"
				}
				fmt.Printf("%-16s %s\n", check.Name, status)
			}
			return nil
		},
	}
}
