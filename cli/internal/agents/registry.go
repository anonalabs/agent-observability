package agents

import (
	_ "embed"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed builtin.yaml
var builtinYAML []byte

type specFile struct {
	Agents []AgentSpec `yaml:"agents"`
}

// Registry holds every declaratively-defined agent (builtin + user-extended
// via ~/.config/agentobs/agents.yaml or --agents-file). Cursor is not in
// here -- it needs real span-emitting code, not just config, and keeps its
// own dedicated path in cli/internal/agents/cursor.go and cmd's connect flow.
type Registry struct {
	specs []AgentSpec
}

// LoadRegistry loads the builtin specs, then appends any from userFile (if
// non-empty and it exists -- silently skipped otherwise, since the default
// path may not exist for most users).
func LoadRegistry(userFile string) (*Registry, error) {
	var builtin specFile
	if err := yaml.Unmarshal(builtinYAML, &builtin); err != nil {
		return nil, fmt.Errorf("parsing builtin agent specs: %w", err)
	}

	specs := builtin.Agents

	if userFile != "" {
		if data, err := os.ReadFile(userFile); err == nil {
			var user specFile
			if err := yaml.Unmarshal(data, &user); err != nil {
				return nil, fmt.Errorf("parsing %s: %w", userFile, err)
			}
			specs = append(specs, user.Agents...)
		}
	}

	return &Registry{specs: specs}, nil
}

func (r *Registry) Get(name string) (*AgentSpec, bool) {
	for i := range r.specs {
		if r.specs[i].Name == name {
			return &r.specs[i], true
		}
	}
	return nil, false
}

func (r *Registry) Names() []string {
	names := make([]string, len(r.specs))
	for i, s := range r.specs {
		names[i] = s.Name
	}
	return names
}

func (r *Registry) All() []AgentSpec {
	return r.specs
}

// ResolveEnvVars returns the ordered env vars for an "env"-kind spec, given
// the collector endpoint and resolved prompt answers (keyed by PromptSpec.Name).
func (s AgentSpec) ResolveEnvVars(endpoint string, answers map[string]bool) []EnvVar {
	data := map[string]interface{}{"Endpoint": endpoint}
	for k, v := range answers {
		data[k] = v
	}

	var vars []EnvVar
	for _, v := range s.Vars {
		if v.When != "" && !answers[v.When] {
			continue
		}
		val := resolveValue(v.Value, data)
		vars = append(vars, EnvVar{Key: v.Key, Value: fmt.Sprintf("%v", val)})
	}
	return vars
}

// ResolveJSONMerge returns the merged settings map for a "json-merge"-kind
// spec -- existing keys are preserved, only the spec's own paths are set.
func (s AgentSpec) ResolveJSONMerge(existing map[string]interface{}, endpoint string, answers map[string]bool) map[string]interface{} {
	data := map[string]interface{}{"Endpoint": endpoint}
	for k, v := range answers {
		data[k] = v
	}

	merged := map[string]interface{}{}
	for k, v := range existing {
		merged[k] = v
	}
	for _, set := range s.Set {
		setNestedPath(merged, set.Path, resolveValue(set.Value, data))
	}
	return merged
}

func setNestedPath(m map[string]interface{}, dotted string, value interface{}) {
	parts := strings.Split(dotted, ".")
	cur := m
	for i, p := range parts {
		if i == len(parts)-1 {
			cur[p] = value
			return
		}
		next, ok := cur[p].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			cur[p] = next
		}
		cur = next
	}
}
