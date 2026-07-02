package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// AgentSpec is a declarative description of how to wire up an agent's
// telemetry -- either exporting shell env vars ("env" kind) or merging a
// key into a JSON settings file ("json-merge" kind). Any tool that follows
// one of these two conventions works via config alone, no Go code needed;
// this is what makes adding a new agent (beyond the 3 built in) a config
// change rather than a code change.
type AgentSpec struct {
	Name    string       `yaml:"name"`
	Kind    string       `yaml:"kind"` // "env" or "json-merge"
	Detect  DetectSpec   `yaml:"detect"`
	Vars    []VarSpec    `yaml:"vars,omitempty"`   // kind: env
	Target  string       `yaml:"target,omitempty"` // kind: json-merge
	Set     []SetSpec    `yaml:"set,omitempty"`    // kind: json-merge
	Prompts []PromptSpec `yaml:"prompts,omitempty"`
}

// EnvVar preserves insertion order (unlike a map), matching the order the
// printed/written export lines rely on.
type EnvVar struct {
	Key   string
	Value string
}

type DetectSpec struct {
	Binary string `yaml:"binary,omitempty"`
	Path   string `yaml:"path,omitempty"` // may start with ~/
}

func (d DetectSpec) Detect() bool {
	if d.Binary != "" {
		if _, err := exec.LookPath(d.Binary); err == nil {
			return true
		}
	}
	if d.Path != "" {
		if _, err := os.Stat(ExpandHome(d.Path)); err == nil {
			return true
		}
	}
	return false
}

// VarSpec is one env var assignment for an "env"-kind spec. Value may be a
// literal, "{{.Endpoint}}", or "{{.<prompt_name>}}" (substituted with the
// resolved prompt answer, type-preserved). When, if set, names a prompt that
// must resolve true for this var to be included.
type VarSpec struct {
	Key   string      `yaml:"key"`
	Value interface{} `yaml:"value"`
	When  string      `yaml:"when,omitempty"`
}

// SetSpec is one dotted-path assignment for a "json-merge"-kind spec, e.g.
// path "telemetry.logPrompts" with value "{{.log_prompts}}".
type SetSpec struct {
	Path  string      `yaml:"path"`
	Value interface{} `yaml:"value"`
}

// PromptSpec is one yes/no question asked during `connect` (unless overridden
// by a flag, YAML config, or --non-interactive).
type PromptSpec struct {
	Name    string `yaml:"name"`
	Message string `yaml:"message"`
	Default bool   `yaml:"default"`
}

func ExpandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}

// resolveValue substitutes "{{.Endpoint}}" or "{{.<name>}}" (an exact,
// whole-value template reference) with its typed value from data, preserving
// bool/string/number types rather than stringifying everything. Anything
// else is returned as a literal, unmodified.
func resolveValue(value interface{}, data map[string]interface{}) interface{} {
	s, ok := value.(string)
	if !ok {
		return value
	}
	if strings.HasPrefix(s, "{{.") && strings.HasSuffix(s, "}}") {
		key := strings.TrimSuffix(strings.TrimPrefix(s, "{{."), "}}")
		if v, ok := data[key]; ok {
			return v
		}
	}
	return value
}
