package commands

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/spf13/cobra"

	"github.com/anonalabs/agent-observability/cli/internal/compose"
)

var requiredPorts = map[int]string{
	4317: "OTLP gRPC",
	4318: "OTLP HTTP",
	8889: "Prometheus exporter",
	9090: "Prometheus UI",
	8123: "ClickHouse HTTP",
	9000: "ClickHouse native",
	3000: "Grafana",
}

func portInUse(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func InstallCmd() *cobra.Command {
	var composeFile string
	var detach bool
	var nonInteractive bool
	var secure bool
	var cloud string

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Bring up the collector/prometheus/clickhouse/grafana stack",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !compose.DockerAvailable() {
				return fmt.Errorf("docker isn't available (checked `docker info`); install/start Docker and retry")
			}

			var busy []string
			for port, desc := range requiredPorts {
				if portInUse(port) {
					busy = append(busy, fmt.Sprintf("%d (%s)", port, desc))
				}
			}
			if len(busy) > 0 {
				fmt.Printf("Ports already in use: %v\n", busy)
				if !nonInteractive {
					proceed := false
					if err := survey.AskOne(&survey.Confirm{
						Message: "Continue anyway?",
						Default: false,
					}, &proceed); err != nil {
						return err
					}
					if !proceed {
						return fmt.Errorf("aborted")
					}
				}
			}

			if secure && cloud != "" {
				return fmt.Errorf("--secure and --cloud can't be combined yet -- both override the collector config; pick one")
			}

			resolved, err := compose.ResolveComposeFile(composeFile)
			if err != nil {
				return err
			}
			composeFiles := []string{resolved}

			if secure {
				overlay, err := compose.SecureOverlayFile(resolved)
				if err != nil {
					return err
				}
				composeFiles = append(composeFiles, overlay)
				if os.Getenv("AGENTOBS_AUTH_TOKEN") == "" || os.Getenv("GRAFANA_ADMIN_PASSWORD") == "" {
					return fmt.Errorf(
						"--secure requires AGENTOBS_AUTH_TOKEN and GRAFANA_ADMIN_PASSWORD to be set in the environment; " +
							"see docs/security.md",
					)
				}
				fmt.Println("Secure mode: anonymous Grafana access disabled, OTLP ingest requires a bearer token.")
			}

			if cloud != "" {
				overlay, err := compose.CloudOverlayFile(resolved, cloud)
				if err != nil {
					return err
				}
				composeFiles = append(composeFiles, overlay)
				fmt.Printf("Cloud export: %s (fanning out alongside local Prometheus/ClickHouse -- see docs/architecture.md).\n", cloud)
			}

			fmt.Printf("Starting stack via %v ...\n", composeFiles)
			if err := compose.Up(composeFiles, detach); err != nil {
				return err
			}

			fmt.Println("Stack is up.")
			fmt.Println("Grafana:    http://localhost:3000")
			fmt.Println("Prometheus: http://localhost:9090")
			fmt.Println()
			if secure {
				fmt.Println("Next: run `agentobs connect --auth-token \"$AGENTOBS_AUTH_TOKEN\"` to wire up an agent's telemetry.")
			} else {
				fmt.Println("Next: run `agentobs connect` to wire up an agent's telemetry.")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&composeFile, "compose-file", "", "")
	cmd.Flags().BoolVar(&detach, "detach", true, "")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "")
	cmd.Flags().BoolVar(&nonInteractive, "yes", false, "")
	cmd.Flags().BoolVar(&secure, "secure", false, "require auth (Grafana login, collector bearer token); needs AGENTOBS_AUTH_TOKEN + GRAFANA_ADMIN_PASSWORD env vars")
	cmd.Flags().StringVar(&cloud, "cloud", "", "also export to aws, gcp, or azure (see docs/architecture.md for required env vars per cloud)")

	return cmd
}
