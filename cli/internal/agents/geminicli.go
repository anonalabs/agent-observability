package agents

import (
	"os"
	"os/exec"
	"path/filepath"
)

type GeminiCliAgent struct{}

func (GeminiCliAgent) Name() string { return "gemini-cli" }

func (GeminiCliAgent) Detect() bool {
	if _, err := exec.LookPath("gemini"); err == nil {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".gemini"))
	return err == nil
}

func (GeminiCliAgent) SettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "settings.json"), nil
}

// EndpointEnvVar is the one thing Gemini CLI reads from the environment;
// everything else (enabled, target, logPrompts) lives in settings.json.
func (GeminiCliAgent) EndpointEnvVar(endpoint string) EnvVar {
	return EnvVar{"OTEL_EXPORTER_OTLP_ENDPOINT", endpoint}
}

// MergeSettings merges telemetry config into whatever settings.json already
// contains -- never replaces other keys (model choice, sandbox, mcpServers, etc).
func (GeminiCliAgent) MergeSettings(existing map[string]interface{}, logPrompts bool) map[string]interface{} {
	merged := map[string]interface{}{}
	for k, v := range existing {
		merged[k] = v
	}
	merged["telemetry"] = map[string]interface{}{
		"enabled":    true,
		"target":     "local",
		"logPrompts": logPrompts,
	}
	return merged
}
