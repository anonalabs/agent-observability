package agents

import (
	"fmt"
	"os"
	"path/filepath"
)

var HookEvents = []string{
	"sessionStart",
	"sessionEnd",
	"preToolUse",
	"postToolUse",
	"postToolUseFailure",
	"beforeShellExecution",
	"afterShellExecution",
	"beforeMCPExecution",
	"afterMCPExecution",
	"beforeReadFile",
	"afterFileEdit",
	"beforeSubmitPrompt",
	"preCompact",
	"stop",
	"subagentStart",
	"subagentStop",
}

type CursorAgent struct{}

func (CursorAgent) Name() string { return "cursor" }

func (CursorAgent) Detect() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".cursor"))
	return err == nil
}

func (CursorAgent) HooksDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cursor", "hooks"), nil
}

func (a CursorAgent) WrapperScriptPath() (string, error) {
	dir, err := a.HooksDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "otel_hook.sh"), nil
}

func (a CursorAgent) ConfigPath() (string, error) {
	dir, err := a.HooksDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "otel_config.json"), nil
}

func (CursorAgent) HooksJSONPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cursor", "hooks.json"), nil
}

func (CursorAgent) OtelConfig(endpoint string, maskPrompts bool, authToken string) map[string]interface{} {
	var headers interface{}
	if authToken != "" {
		headers = "Authorization=Bearer " + authToken
	}
	return map[string]interface{}{
		"OTEL_EXPORTER_OTLP_ENDPOINT": endpoint,
		"OTEL_SERVICE_NAME":           "cursor-agent",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
		"OTEL_EXPORTER_OTLP_INSECURE": "true",
		"OTEL_EXPORTER_OTLP_HEADERS":  headers,
		"CURSOR_OTEL_MASK_PROMPTS":    boolStr(maskPrompts),
		"OTEL_EXPORTER_OTLP_TIMEOUT":  "30",
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// HooksJSON merges our hook entry into each event's list -- never replaces
// whatever else (other tools, e.g. a memory-retention plugin) already
// registered for that event, since Cursor supports multiple commands per event.
func (a CursorAgent) HooksJSON(existing map[string]interface{}) (map[string]interface{}, error) {
	wrapper, err := a.WrapperScriptPath()
	if err != nil {
		return nil, err
	}
	entry := map[string]interface{}{"command": wrapper, "timeout": 5}

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

	for _, event := range HookEvents {
		entries, _ := hooks[event].([]interface{})
		alreadyPresent := false
		for _, e := range entries {
			if m, ok := e.(map[string]interface{}); ok {
				if cmd, ok := m["command"].(string); ok && cmd == wrapper {
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

func (a CursorAgent) WrapperScript() (string, error) {
	configPath, err := a.ConfigPath()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("#!/bin/bash\nexec agentobs cursor-hook --config \"%s\" \"$@\"\n", configPath), nil
}
