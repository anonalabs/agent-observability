// Package agents builds the per-agent config that `connect` writes -- either
// shell env vars (Claude Code), a JSON settings merge (Gemini CLI), or a set
// of hook config files (Cursor).
package agents

import (
	"os"
	"os/exec"
	"path/filepath"
)

type ClaudeCodeAgent struct{}

func (ClaudeCodeAgent) Name() string { return "claude-code" }

func (ClaudeCodeAgent) Detect() bool {
	if _, err := exec.LookPath("claude"); err == nil {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".claude"))
	return err == nil
}

// EnvVars returns the ordered env var assignments needed to enable Claude
// Code's native OTel export, pointed at endpoint.
func (ClaudeCodeAgent) EnvVars(endpoint string, logUserPrompts, logToolDetails bool) []EnvVar {
	vars := []EnvVar{
		{"CLAUDE_CODE_ENABLE_TELEMETRY", "1"},
		{"OTEL_METRICS_EXPORTER", "otlp"},
		{"OTEL_LOGS_EXPORTER", "otlp"},
		{"OTEL_EXPORTER_OTLP_PROTOCOL", "grpc"},
		{"OTEL_EXPORTER_OTLP_ENDPOINT", endpoint},
		// Prometheus-style backends drop delta metrics silently without this.
		{"OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE", "cumulative"},
	}
	if logUserPrompts {
		vars = append(vars, EnvVar{"OTEL_LOG_USER_PROMPTS", "1"})
	}
	if logToolDetails {
		vars = append(vars, EnvVar{"OTEL_LOG_TOOL_DETAILS", "1"})
	}
	return vars
}

// EnvVar preserves insertion order (unlike a map), matching the Python dict's
// order-preserving behavior that the printed/written export lines rely on.
type EnvVar struct {
	Key   string
	Value string
}
