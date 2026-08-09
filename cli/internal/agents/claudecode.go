package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// StopHookCommand is what Claude Code runs when a session ends. It is
// matched exactly when deciding whether our entry is already registered, so
// re-running connect cannot add it twice.
func StopHookCommand() string { return "agentobs memory hook" }

func ClaudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// MergeStopHook adds our Stop hook to an existing settings map without
// disturbing anything else. Real installs carry unrelated hooks and settings
// in this file, so it merges rather than replaces -- the same discipline
// connect already applies to every other agent's config.
func MergeStopHook(existing map[string]interface{}) map[string]interface{} {
	merged := map[string]interface{}{}
	for k, v := range existing {
		merged[k] = v
	}

	hooks, _ := merged["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = map[string]interface{}{}
	} else {
		copied := map[string]interface{}{}
		for k, v := range hooks {
			copied[k] = v
		}
		hooks = copied
	}

	stop, _ := hooks["Stop"].([]interface{})
	for _, entry := range stop {
		m, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		inner, ok := m["hooks"].([]interface{})
		if !ok {
			continue
		}
		for _, h := range inner {
			hm, ok := h.(map[string]interface{})
			if ok && hm["command"] == StopHookCommand() {
				// Already registered; leave the file untouched.
				merged["hooks"] = hooks
				return merged
			}
		}
	}

	stop = append(stop, map[string]interface{}{
		"hooks": []interface{}{
			map[string]interface{}{"type": "command", "command": StopHookCommand()},
		},
	})
	hooks["Stop"] = stop
	merged["hooks"] = hooks
	return merged
}

// RegisterStopHook merges the hook into ~/.claude/settings.json, backing the
// file up first.
func RegisterStopHook() (string, error) {
	path, err := ClaudeSettingsPath()
	if err != nil {
		return "", err
	}
	return registerStopHookAt(path)
}

// registerStopHookAt does the actual read-merge-write against an explicit
// path, so it can be exercised against a t.TempDir() instead of the real
// ~/.claude/settings.json. The write is atomic: we marshal into a temp file
// next to the target and os.Rename it over -- rename within a filesystem is
// atomic, so a crash, OOM kill, or full disk between those steps leaves the
// original file completely intact rather than truncated. The temp file has
// to live in the same directory as the target because os.Rename fails
// across filesystems, and a bare os.WriteFile(path, ...) would otherwise
// truncate settings.json before writing its replacement -- a file another
// tool (and Claude Code itself) depends on for model, permissions, MCP
// servers, and other hooks.
func registerStopHookAt(path string) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	existing := map[string]interface{}{}
	if data, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".bak", data, 0o644); err != nil {
			return "", err
		}
		if err := json.Unmarshal(data, &existing); err != nil {
			return "", err
		}
	}

	out, err := json.MarshalIndent(MergeStopHook(existing), "", "  ")
	if err != nil {
		return "", err
	}

	tmp, err := os.CreateTemp(dir, ".settings.json.tmp-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	return path, nil
}
