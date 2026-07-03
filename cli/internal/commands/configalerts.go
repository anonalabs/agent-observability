package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/AlecAivazis/survey/v2"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/anonalabs/agent-observability/cli/internal/compose"
	"github.com/anonalabs/agent-observability/cli/internal/config"
)

// --- YAML shapes, matching Grafana's alert-rule / contact-point provisioning
// schema exactly -- these are marshaled, never hand-written as raw strings,
// so thresholds/webhook are never hardcoded anywhere in this binary.

type ruleFile struct {
	APIVersion int         `yaml:"apiVersion"`
	Groups     []ruleGroup `yaml:"groups"`
}

type ruleGroup struct {
	OrgID    int         `yaml:"orgId"`
	Name     string      `yaml:"name"`
	Folder   string      `yaml:"folder"`
	Interval string      `yaml:"interval"`
	Rules    []alertRule `yaml:"rules"`
}

type alertRule struct {
	UID          string            `yaml:"uid"`
	Title        string            `yaml:"title"`
	Condition    string            `yaml:"condition"`
	Data         []queryData       `yaml:"data"`
	NoDataState  string            `yaml:"noDataState"`
	ExecErrState string            `yaml:"execErrState"`
	For          string            `yaml:"for"`
	Labels       map[string]string `yaml:"labels"`
	Annotations  map[string]string `yaml:"annotations"`
}

type queryData struct {
	RefID             string                 `yaml:"refId"`
	RelativeTimeRange timeRange              `yaml:"relativeTimeRange"`
	DatasourceUID     string                 `yaml:"datasourceUid"`
	Model             map[string]interface{} `yaml:"model"`
}

type timeRange struct {
	From int `yaml:"from"`
	To   int `yaml:"to"`
}

type contactPointsFile struct {
	APIVersion    int                 `yaml:"apiVersion"`
	ContactPoints []contactPointGroup `yaml:"contactPoints"`
	Policies      []policy            `yaml:"policies"`
}

type contactPointGroup struct {
	OrgID     int        `yaml:"orgId"`
	Name      string     `yaml:"name"`
	Receivers []receiver `yaml:"receivers"`
}

type receiver struct {
	UID      string            `yaml:"uid"`
	Type     string            `yaml:"type"`
	Settings map[string]string `yaml:"settings"`
}

type policy struct {
	OrgID          int      `yaml:"orgId"`
	Receiver       string   `yaml:"receiver"`
	GroupBy        []string `yaml:"group_by,omitempty"`
	GroupWait      string   `yaml:"group_wait,omitempty"`
	GroupInterval  string   `yaml:"group_interval,omitempty"`
	RepeatInterval string   `yaml:"repeat_interval,omitempty"`
	Routes         []route  `yaml:"routes,omitempty"`
}

type route struct {
	Receiver string   `yaml:"receiver"`
	Matchers []string `yaml:"matchers"`
	Continue bool     `yaml:"continue,omitempty"`
}

func buildRules(costThreshold float64, rateLimitThreshold, toolFailureThreshold int) ruleFile {
	return ruleFile{
		APIVersion: 1,
		Groups: []ruleGroup{
			{
				OrgID:    1,
				Name:     "agentobs-alerts",
				Folder:   "AI Agent Telemetry",
				Interval: "1m",
				Rules: []alertRule{
					{
						UID:       "agentobs-cost-spike",
						Title:     "Claude Code cost spike",
						Condition: "C",
						Data: []queryData{
							{
								RefID:             "A",
								RelativeTimeRange: timeRange{From: 3600, To: 0},
								DatasourceUID:     "Prometheus",
								Model: map[string]interface{}{
									"refId":   "A",
									"expr":    "increase(claude_code_cost_usage_USD_total[1h])",
									"instant": true,
								},
							},
							{
								RefID:             "C",
								RelativeTimeRange: timeRange{From: 3600, To: 0},
								DatasourceUID:     "__expr__",
								Model: map[string]interface{}{
									"refId":      "C",
									"type":       "threshold",
									"expression": "A",
									"conditions": []map[string]interface{}{
										{"evaluator": map[string]interface{}{"type": "gt", "params": []float64{costThreshold}}},
									},
								},
							},
						},
						NoDataState:  "OK",
						ExecErrState: "Error",
						For:          "5m",
						Labels:       map[string]string{"severity": "warning"},
						Annotations: map[string]string{
							"summary": fmt.Sprintf("Claude Code cost rose by ${{ $values.A }} in the last hour (threshold: $%s). See the token-cost-usage dashboard.", strconv.FormatFloat(costThreshold, 'f', -1, 64)),
						},
					},
					{
						UID:       "agentobs-rate-limit-spike",
						Title:     "Claude Code hitting API rate limits (429s)",
						Condition: "C",
						Data: []queryData{
							{
								RefID:             "A",
								RelativeTimeRange: timeRange{From: 300, To: 0},
								DatasourceUID:     "ClickHouse",
								Model: map[string]interface{}{
									"refId":  "A",
									"rawSql": "SELECT count() AS value FROM otel.otel_logs WHERE LogAttributes['event.name']='api_error' AND LogAttributes['status_code']='429'",
									"format": 1,
								},
							},
							{
								RefID:             "C",
								RelativeTimeRange: timeRange{From: 300, To: 0},
								DatasourceUID:     "__expr__",
								Model: map[string]interface{}{
									"refId":      "C",
									"type":       "threshold",
									"expression": "A",
									"conditions": []map[string]interface{}{
										{"evaluator": map[string]interface{}{"type": "gt", "params": []int{rateLimitThreshold}}},
									},
								},
							},
						},
						NoDataState:  "OK",
						ExecErrState: "Error",
						For:          "1m",
						Labels:       map[string]string{"severity": "critical"},
						Annotations: map[string]string{
							"summary": fmt.Sprintf("Claude Code hit {{ $values.A }} API rate-limit (429) errors in the last 5 minutes (threshold: %d). See the events-detail dashboard.", rateLimitThreshold),
						},
					},
					{
						UID:       "agentobs-tool-failure-spike",
						Title:     "AI agent tool call failures",
						Condition: "C",
						Data: []queryData{
							{
								RefID:             "A",
								RelativeTimeRange: timeRange{From: 900, To: 0},
								DatasourceUID:     "ClickHouse",
								Model: map[string]interface{}{
									"refId": "A",
									"rawSql": "SELECT count() AS value FROM (" +
										"SELECT Timestamp FROM otel.otel_logs WHERE LogAttributes['event.name']='tool_result' AND LogAttributes['success']='false' " +
										"UNION ALL " +
										"SELECT Timestamp FROM otel.otel_traces WHERE StatusCode='STATUS_CODE_ERROR' AND SpanAttributes['gen_ai.operation.name']='tool')",
									"format": 1,
								},
							},
							{
								RefID:             "C",
								RelativeTimeRange: timeRange{From: 900, To: 0},
								DatasourceUID:     "__expr__",
								Model: map[string]interface{}{
									"refId":      "C",
									"type":       "threshold",
									"expression": "A",
									"conditions": []map[string]interface{}{
										{"evaluator": map[string]interface{}{"type": "gt", "params": []int{toolFailureThreshold}}},
									},
								},
							},
						},
						NoDataState:  "OK",
						ExecErrState: "Error",
						For:          "5m",
						Labels:       map[string]string{"severity": "warning"},
						Annotations: map[string]string{
							"summary": fmt.Sprintf("{{ $values.A }} tool calls failed across agents in the last 15 minutes (threshold: %d). See the agent-leaderboard dashboard.", toolFailureThreshold),
						},
					},
				},
			},
		},
	}
}

// slackTitle/slackText override Grafana's default notification body, which
// dumps every label plus raw refId values (e.g. "A=4, C=1") -- clear to
// someone reading the rule definition, meaningless in a Slack notification.
// This renders just the rule name and each alert's own descriptive summary
// (which already has the real count and threshold baked in via $values),
// plus a link back to Grafana.
const slackTitle = "{{ .CommonLabels.alertname }} ({{ .Status }})"
const slackText = `{{ range .Alerts }}{{ .Annotations.summary }}
{{ end }}<{{ .ExternalURL }}|View in Grafana>`

func buildContactPoints(contactType, webhookURL, criticalWebhookURL string) contactPointsFile {
	receiverSettings := func(url string) map[string]string {
		s := map[string]string{"url": url}
		if contactType == "slack" {
			s["title"] = slackTitle
			s["text"] = slackText
		}
		return s
	}

	contactPoints := []contactPointGroup{
		{
			OrgID: 1,
			Name:  "agentobs-webhook",
			Receivers: []receiver{
				{UID: "agentobs-webhook-1", Type: contactType, Settings: receiverSettings(webhookURL)},
			},
		},
	}

	pol := policy{
		OrgID:          1,
		Receiver:       "agentobs-webhook",
		GroupBy:        []string{"alertname"},
		GroupWait:      "30s",
		GroupInterval:  "5m",
		RepeatInterval: "4h",
	}

	// Critical-severity alerts (currently just the rate-limit rule) can be
	// routed to a separate webhook -- e.g. a paging channel instead of a
	// general one -- if the caller provided one. Otherwise everything stays
	// on the single default receiver.
	if criticalWebhookURL != "" {
		contactPoints = append(contactPoints, contactPointGroup{
			OrgID: 1,
			Name:  "agentobs-webhook-critical",
			Receivers: []receiver{
				{UID: "agentobs-webhook-critical-1", Type: contactType, Settings: receiverSettings(criticalWebhookURL)},
			},
		})
		pol.Routes = []route{
			{Receiver: "agentobs-webhook-critical", Matchers: []string{"severity=critical"}, Continue: false},
		}
	}

	return contactPointsFile{
		APIVersion:    1,
		ContactPoints: contactPoints,
		Policies:      []policy{pol},
	}
}

func alertingDir(baseComposeFile string) string {
	return filepath.Join(filepath.Dir(baseComposeFile), "grafana", "provisioning", "alerting")
}

func ConfigAlertsCmd() *cobra.Command {
	var composeFile, webhookURL, criticalWebhookURL, contactType string
	var costThreshold float64
	var rateLimitThreshold, toolFailureThreshold int
	var nonInteractive, restart bool
	var costThresholdSet, rateLimitThresholdSet, toolFailureThresholdSet, contactTypeSet bool

	cmd := &cobra.Command{
		Use:   "config-alerts",
		Short: "Configure alert thresholds and the notification webhook (Slack, Discord, etc.)",
		RunE: func(cmd *cobra.Command, args []string) error {
			var webhookURLFlagPtr *string
			if cmd.Flags().Changed("webhook-url") {
				webhookURLFlagPtr = &webhookURL
			}
			resolvedWebhookURL, err := config.Resolve("config_alerts.webhook_url", webhookURLFlagPtr, nil, func() (string, error) {
				var v string
				err := survey.AskOne(&survey.Input{Message: "Webhook URL (Slack incoming webhook, Discord, PagerDuty, etc.):"}, &v, survey.WithValidator(survey.Required))
				return v, err
			}, nonInteractive, nil)
			if err != nil {
				return err
			}

			var contactTypeFlagPtr *string
			if contactTypeSet {
				contactTypeFlagPtr = &contactType
			}
			defContactType := "slack"
			resolvedContactType, err := config.Resolve("config_alerts.contact_type", contactTypeFlagPtr, nil, func() (string, error) {
				var v string
				err := survey.AskOne(&survey.Select{
					Message: "Webhook format:",
					Options: []string{"slack", "webhook"},
					Default: "slack",
					Description: func(value string, index int) string {
						if value == "slack" {
							return "Slack, or Discord's /slack-compatible webhook URL"
						}
						return "generic JSON POST (PagerDuty, custom receivers, etc.)"
					},
				}, &v)
				return v, err
			}, nonInteractive, &defContactType)
			if err != nil {
				return err
			}

			var costFlagPtr *float64
			if costThresholdSet {
				costFlagPtr = &costThreshold
			}
			defCost := 5.0
			resolvedCost, err := config.Resolve("config_alerts.cost_threshold", costFlagPtr, nil, func() (float64, error) {
				var v string
				err := survey.AskOne(&survey.Input{Message: "Alert when Claude Code cost exceeds this many USD/hour:", Default: "5"}, &v)
				if err != nil {
					return 0, err
				}
				return strconv.ParseFloat(v, 64)
			}, nonInteractive, &defCost)
			if err != nil {
				return err
			}

			var rateLimitFlagPtr *int
			if rateLimitThresholdSet {
				rateLimitFlagPtr = &rateLimitThreshold
			}
			defRateLimit := 0
			resolvedRateLimit, err := config.Resolve("config_alerts.rate_limit_threshold", rateLimitFlagPtr, nil, func() (int, error) {
				var v string
				err := survey.AskOne(&survey.Input{Message: "Alert when 429 rate-limit errors in 5 minutes exceed:", Default: "0"}, &v)
				if err != nil {
					return 0, err
				}
				return strconv.Atoi(v)
			}, nonInteractive, &defRateLimit)
			if err != nil {
				return err
			}

			var toolFailureFlagPtr *int
			if toolFailureThresholdSet {
				toolFailureFlagPtr = &toolFailureThreshold
			}
			defToolFailure := 3
			resolvedToolFailure, err := config.Resolve("config_alerts.tool_failure_threshold", toolFailureFlagPtr, nil, func() (int, error) {
				var v string
				err := survey.AskOne(&survey.Input{Message: "Alert when failed tool calls in 15 minutes exceed:", Default: "3"}, &v)
				if err != nil {
					return 0, err
				}
				return strconv.Atoi(v)
			}, nonInteractive, &defToolFailure)
			if err != nil {
				return err
			}

			resolvedComposeFile, err := compose.ResolveComposeFile(composeFile)
			if err != nil {
				return err
			}
			dir := alertingDir(resolvedComposeFile)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}

			rulesPath := filepath.Join(dir, "rules.yaml")
			rulesOut, err := yaml.Marshal(buildRules(resolvedCost, resolvedRateLimit, resolvedToolFailure))
			if err != nil {
				return err
			}
			if err := os.WriteFile(rulesPath, rulesOut, 0o644); err != nil {
				return err
			}

			contactPath := filepath.Join(dir, "contactpoints.yaml")
			contactOut, err := yaml.Marshal(buildContactPoints(resolvedContactType, resolvedWebhookURL, criticalWebhookURL))
			if err != nil {
				return err
			}
			// 0644, not something tighter: Grafana reads this file from
			// inside a container via a bind mount, running as a UID that
			// doesn't match the host user writing it here. 0600 (tried and
			// reverted -- see git history) means the container literally
			// can't read its own config and Grafana fails to start. The
			// actual protection for this file (it holds a webhook secret)
			// is that it's gitignored, not filesystem permissions.
			if err := os.WriteFile(contactPath, contactOut, 0o644); err != nil {
				return err
			}

			fmt.Printf("Wrote %s and %s.\n", rulesPath, contactPath)

			if restart {
				fmt.Println("Restarting Grafana to apply...")
				if err := compose.Run([]string{resolvedComposeFile}, "restart", "grafana"); err != nil {
					return err
				}
				fmt.Println("Done. Check docs/alerting.md if rules don't show up as expected.")
			} else {
				fmt.Println("Run `docker compose restart grafana` to apply.")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&composeFile, "compose-file", "", "")
	cmd.Flags().StringVar(&webhookURL, "webhook-url", "", "Slack/Discord/etc. webhook URL")
	cmd.Flags().StringVar(&criticalWebhookURL, "critical-webhook-url", "", "optional separate webhook for critical-severity alerts (default: same as --webhook-url)")
	cmd.Flags().StringVar(&contactType, "contact-type", "", "slack or webhook (default: slack)")
	cmd.Flags().Float64Var(&costThreshold, "cost-threshold", 0, "USD/hour cost alert threshold (default: 5)")
	cmd.Flags().IntVar(&rateLimitThreshold, "rate-limit-threshold", 0, "429 count/5min alert threshold (default: 0)")
	cmd.Flags().IntVar(&toolFailureThreshold, "tool-failure-threshold", 0, "failed tool calls/15min alert threshold (default: 3)")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "")
	cmd.Flags().BoolVar(&nonInteractive, "yes", false, "")
	cmd.Flags().BoolVar(&restart, "restart", true, "restart Grafana automatically after writing config")

	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		contactTypeSet = cmd.Flags().Changed("contact-type")
		costThresholdSet = cmd.Flags().Changed("cost-threshold")
		rateLimitThresholdSet = cmd.Flags().Changed("rate-limit-threshold")
		toolFailureThresholdSet = cmd.Flags().Changed("tool-failure-threshold")
	}

	return cmd
}
