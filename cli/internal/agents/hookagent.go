package agents

// HookAgent is implemented by every hook-JSON-based agent (Cursor, Copilot,
// Codex): each writes its own wrapper script + otel config + hooks.json in
// its own shape, but connect.go drives all three through this one interface
// instead of one hardcoded function per agent.
type HookAgent interface {
	Name() string
	HooksDir() (string, error)
	WrapperScriptPath() (string, error)
	ConfigPath() (string, error)
	HooksJSONPath() (string, error)
	OtelConfig(endpoint string, maskPrompts bool, authToken string) map[string]interface{}
	HooksJSON(existing map[string]interface{}) (map[string]interface{}, error)
	WrapperScript() (string, error)
}
