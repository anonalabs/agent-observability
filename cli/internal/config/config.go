// Package config resolves settings using the precedence: CLI flag > YAML file
// > interactive prompt > documented default.
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type MissingConfigError struct {
	Key string
}

func (e *MissingConfigError) Error() string {
	return fmt.Sprintf(
		"missing required value for '%s' and --non-interactive was set "+
			"(pass it as a flag or in --config yaml)", e.Key,
	)
}

// LoadYAML reads a YAML config file into a nested map. An empty path returns
// an empty map (no config file given).
func LoadYAML(path string) (map[string]interface{}, error) {
	if path == "" {
		return map[string]interface{}{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config file not found: %s: %w", path, err)
	}
	var out map[string]interface{}
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("config file %s must contain a mapping at the top level: %w", path, err)
	}
	if out == nil {
		out = map[string]interface{}{}
	}
	return out, nil
}

// GetNested looks up a dotted key (e.g. "connect.endpoint") in a nested map.
func GetNested(data map[string]interface{}, dottedKey string) (interface{}, bool) {
	var node interface{} = data
	for _, part := range strings.Split(dottedKey, ".") {
		m, ok := node.(map[string]interface{})
		if !ok {
			return nil, false
		}
		v, ok := m[part]
		if !ok {
			return nil, false
		}
		node = v
	}
	return node, true
}

// Resolve returns the value for dottedKey under flag > yaml > prompt > default
// precedence. flagValue is a pointer; nil means "not set via flag". def is a
// pointer; nil means "no documented default" (Resolve errors in non-interactive
// mode with no default and no flag/yaml value).
func Resolve[T any](
	dottedKey string,
	flagValue *T,
	yamlConfig map[string]interface{},
	prompt func() (T, error),
	nonInteractive bool,
	def *T,
) (T, error) {
	var zero T

	if flagValue != nil {
		return *flagValue, nil
	}

	if raw, ok := GetNested(yamlConfig, dottedKey); ok {
		if v, ok := raw.(T); ok {
			return v, nil
		}
	}

	if nonInteractive {
		if def != nil {
			return *def, nil
		}
		return zero, &MissingConfigError{Key: dottedKey}
	}

	return prompt()
}
