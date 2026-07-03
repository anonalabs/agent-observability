package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CodexHookEvents are Codex's own hook event names -- PascalCase, distinct
// from Cursor's/Copilot's camelCase. cursorhook.normalizeEvent maps these
// onto our canonical camelCase vocabulary at processing time.
var CodexHookEvents = []string{
	"SessionStart",
	"PreToolUse",
	"PermissionRequest",
	"PostToolUse",
	"UserPromptSubmit",
	"Stop",
}

// codexMatchers gives each matcher-taking event its matcher pattern; events
// absent from this map (UserPromptSubmit, Stop) take no matcher field at all.
var codexMatchers = map[string]string{
	"SessionStart":      "startup|resume|clear",
	"PreToolUse":        "*",
	"PermissionRequest": "*",
	"PostToolUse":       "*",
}

type CodexAgent struct{}

func (CodexAgent) Name() string { return "codex" }

func (CodexAgent) Detect() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".codex"))
	return err == nil
}

func (CodexAgent) HooksDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "hooks"), nil
}

func (a CodexAgent) WrapperScriptPath() (string, error) {
	dir, err := a.HooksDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "otel_hook.sh"), nil
}

func (a CodexAgent) ConfigPath() (string, error) {
	dir, err := a.HooksDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "otel_config.json"), nil
}

func (CodexAgent) HooksJSONPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "hooks.json"), nil
}

// TOMLConfigPath is Codex's own config.toml, where hooks must be explicitly
// turned on via `[features]\ncodex_hooks = true` -- unlike Cursor/Copilot,
// writing hooks.json alone isn't enough for Codex to actually run them.
func (CodexAgent) TOMLConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

func (CodexAgent) OtelConfig(endpoint string, maskPrompts bool, authToken string) map[string]interface{} {
	var headers interface{}
	if authToken != "" {
		headers = "Authorization=Bearer " + authToken
	}
	return map[string]interface{}{
		"OTEL_EXPORTER_OTLP_ENDPOINT": endpoint,
		"OTEL_SERVICE_NAME":           "codex-agent",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
		"OTEL_EXPORTER_OTLP_INSECURE": "true",
		"OTEL_EXPORTER_OTLP_HEADERS":  headers,
		"CURSOR_OTEL_MASK_PROMPTS":    boolStr(maskPrompts),
		"OTEL_EXPORTER_OTLP_TIMEOUT":  "30",
	}
}

// HooksJSON merges our hook entry into each event's matcher-group list.
// Codex nests one level deeper than Cursor/Copilot: each event has a list of
// {matcher, hooks: [...]} groups rather than a flat list of commands, so the
// idempotency check has to look inside every group's nested hooks array.
func (a CodexAgent) HooksJSON(existing map[string]interface{}) (map[string]interface{}, error) {
	wrapper, err := a.WrapperScriptPath()
	if err != nil {
		return nil, err
	}
	entry := map[string]interface{}{"type": "command", "command": wrapper, "timeout": 30}

	hooks := map[string]interface{}{}
	if existing != nil {
		if existingHooks, ok := existing["hooks"].(map[string]interface{}); ok {
			for event, rawGroups := range existingHooks {
				if groups, ok := rawGroups.([]interface{}); ok {
					copied := make([]interface{}, len(groups))
					copy(copied, groups)
					hooks[event] = copied
				}
			}
		}
	}

	for _, event := range CodexHookEvents {
		groups, _ := hooks[event].([]interface{})

		alreadyPresent := false
		for _, g := range groups {
			group, ok := g.(map[string]interface{})
			if !ok {
				continue
			}
			nested, _ := group["hooks"].([]interface{})
			for _, h := range nested {
				if m, ok := h.(map[string]interface{}); ok {
					if cmd, ok := m["command"].(string); ok && cmd == wrapper {
						alreadyPresent = true
					}
				}
			}
		}

		if !alreadyPresent {
			group := map[string]interface{}{"hooks": []interface{}{entry}}
			if matcher, ok := codexMatchers[event]; ok {
				group["matcher"] = matcher
			}
			groups = append(groups, group)
		}
		hooks[event] = groups
	}

	return map[string]interface{}{"hooks": hooks}, nil
}

func (a CodexAgent) WrapperScript() (string, error) {
	configPath, err := a.ConfigPath()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("#!/bin/bash\nexec agentobs cursor-hook --config \"%s\" \"$@\"\n", configPath), nil
}

// EnableHooksFeature adds `[features]\ncodex_hooks = true` to config.toml if
// not already present. Deliberately a targeted text patch, not a full TOML
// parse/rewrite -- pulling in a TOML library for one boolean flag isn't worth
// the dependency, and this never touches any other key.
func EnableHooksFeature(tomlPath string) error {
	data, err := os.ReadFile(tomlPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(data)

	if strings.Contains(content, "codex_hooks") {
		return nil
	}

	if strings.Contains(content, "[features]") {
		content = strings.Replace(content, "[features]", "[features]\ncodex_hooks = true", 1)
	} else {
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n[features]\ncodex_hooks = true\n"
	}

	if err := os.MkdirAll(filepath.Dir(tomlPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(tomlPath, []byte(content), 0o644)
}
