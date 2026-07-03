package agents

import (
	"fmt"
	"os"
	"path/filepath"
)

// CopilotHookEvents are GitHub Copilot coding agent's own hook event names --
// already the same camelCase vocabulary as Cursor's for the events both
// support (sessionStart/sessionEnd/preToolUse/postToolUse), plus
// userPromptSubmitted and errorOccurred which Cursor doesn't have.
var CopilotHookEvents = []string{
	"sessionStart",
	"sessionEnd",
	"userPromptSubmitted",
	"preToolUse",
	"postToolUse",
	"errorOccurred",
}

// CopilotAgent wires GitHub Copilot's coding agent hooks. Copilot only
// supports repository-scoped hooks (no global/user-level config), so unlike
// Cursor/Claude/Gemini this always writes into the current repo.
type CopilotAgent struct{}

func (CopilotAgent) Name() string { return "copilot" }

func (CopilotAgent) Detect() bool {
	_, err := os.Stat(".github")
	return err == nil
}

func (CopilotAgent) HooksDir() (string, error) {
	return filepath.Join(".github", "hooks"), nil
}

func (a CopilotAgent) WrapperScriptPath() (string, error) {
	dir, err := a.HooksDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "otel_hook.sh"), nil
}

func (a CopilotAgent) ConfigPath() (string, error) {
	dir, err := a.HooksDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "otel_config.json"), nil
}

func (CopilotAgent) HooksJSONPath() (string, error) {
	return filepath.Join(".github", "hooks", "otel-hooks.json"), nil
}

func (CopilotAgent) OtelConfig(endpoint string, maskPrompts bool, authToken string) map[string]interface{} {
	var headers interface{}
	if authToken != "" {
		headers = "Authorization=Bearer " + authToken
	}
	return map[string]interface{}{
		"OTEL_EXPORTER_OTLP_ENDPOINT": endpoint,
		"OTEL_SERVICE_NAME":           "copilot-agent",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
		"OTEL_EXPORTER_OTLP_INSECURE": "true",
		"OTEL_EXPORTER_OTLP_HEADERS":  headers,
		"CURSOR_OTEL_MASK_PROMPTS":    boolStr(maskPrompts),
		"OTEL_EXPORTER_OTLP_TIMEOUT":  "30",
	}
}

// HooksJSON merges our hook entry into each event's flat entry list --
// Copilot's hook schema is simpler than Cursor's or Codex's: one flat array
// of {"type":"command","bash":...,"timeoutSec":...} per event, no matcher
// nesting. Never replaces other tools' entries for the same event, same
// idempotent by-command check as Cursor's HooksJSON.
func (a CopilotAgent) HooksJSON(existing map[string]interface{}) (map[string]interface{}, error) {
	wrapper, err := a.WrapperScriptPath()
	if err != nil {
		return nil, err
	}
	entry := map[string]interface{}{"type": "command", "bash": wrapper, "timeoutSec": 30}

	merged := map[string]interface{}{"version": 1}
	hooks := map[string]interface{}{}

	if existing != nil {
		if v, ok := existing["version"]; ok {
			merged["version"] = v
		}
		if existingHooks, ok := existing["hooks"].(map[string]interface{}); ok {
			for event, rawEntries := range existingHooks {
				if entries, ok := rawEntries.([]interface{}); ok {
					copied := make([]interface{}, len(entries))
					copy(copied, entries)
					hooks[event] = copied
				}
			}
		}
	}

	for _, event := range CopilotHookEvents {
		entries, _ := hooks[event].([]interface{})
		alreadyPresent := false
		for _, e := range entries {
			if m, ok := e.(map[string]interface{}); ok {
				if bash, ok := m["bash"].(string); ok && bash == wrapper {
					alreadyPresent = true
					break
				}
			}
		}
		if !alreadyPresent {
			entries = append(entries, entry)
		}
		hooks[event] = entries
	}

	merged["hooks"] = hooks
	return merged, nil
}

func (a CopilotAgent) WrapperScript() (string, error) {
	configPath, err := a.ConfigPath()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("#!/bin/bash\nexec agentobs cursor-hook --config \"%s\" \"$@\"\n", configPath), nil
}
