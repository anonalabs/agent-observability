package agents

import (
	"fmt"
	"os"
	"path/filepath"
)

// OpenCodeAgent wires OpenCode's plugin system. Unlike Cursor/Copilot/Codex,
// OpenCode has no hooks.json-style registration file -- the plugin file
// itself, dropped into its plugins directory, is both the registration and
// the code that pipes each event to our hook binary. Global by default
// (~/.config/opencode/plugins/); for project scope, copy the written file
// into .opencode/plugins/ in the repo instead.
type OpenCodeAgent struct{}

func (OpenCodeAgent) Name() string { return "opencode" }

func (OpenCodeAgent) Detect() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".config", "opencode"))
	return err == nil
}

func (OpenCodeAgent) PluginDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode", "plugins"), nil
}

func (a OpenCodeAgent) PluginPath() (string, error) {
	dir, err := a.PluginDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agentobs-otel-hook.ts"), nil
}

func (a OpenCodeAgent) ConfigPath() (string, error) {
	dir, err := a.PluginDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agentobs-otel-config.json"), nil
}

func (OpenCodeAgent) OtelConfig(endpoint string, maskPrompts bool, authToken string) map[string]interface{} {
	var headers interface{}
	if authToken != "" {
		headers = "Authorization=Bearer " + authToken
	}
	return map[string]interface{}{
		"OTEL_EXPORTER_OTLP_ENDPOINT": endpoint,
		"OTEL_SERVICE_NAME":           "opencode-agent",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
		"OTEL_EXPORTER_OTLP_INSECURE": "true",
		"OTEL_EXPORTER_OTLP_HEADERS":  headers,
		"CURSOR_OTEL_MASK_PROMPTS":    boolStr(maskPrompts),
		"OTEL_EXPORTER_OTLP_TIMEOUT":  "30",
	}
}

// PluginContent generates the plugin TS source with configPath baked in, so
// it needs no env vars or extra setup beyond being present in the plugins
// directory -- OpenCode loads every .ts file there automatically.
func (OpenCodeAgent) PluginContent(configPath string) string {
	return fmt.Sprintf(`import type { Plugin } from "@opencode-ai/plugin"

// Written by "agentobs connect --agent opencode". Pipes OpenCode's own
// plugin events to the agentobs hook binary as JSON on stdin, which builds
// and exports an OTel span per event -- same processing path as the
// Cursor/Copilot/Codex hook integrations, just a different trigger
// mechanism (OpenCode has no hooks.json, plugins are the registration).
export const AgentobsOtelHook: Plugin = async ({ $, directory }) => {
  try {
    await $`+"`which agentobs`"+`.quiet()
  } catch {
    console.warn("[agentobs] agentobs not found in PATH -- plugin disabled")
    return {}
  }

  // "directory" is PluginInput's project root -- OpenCode's equivalent of
  // Cursor's own workspace_roots hook field. Stamping it on every payload
  // (rather than only the prompt event) matches addCommonAttributes'
  // behavior for Cursor, so the AnonaMemory connector's CWD-based allowlist
  // (Credentials.AllowsPath) actually sees a working directory for OpenCode
  // instead of dropping every turn.
  async function invoke(payload: Record<string, unknown>): Promise<void> {
    try {
      await $`+"`agentobs cursor-hook --config %q`"+`.stdin(JSON.stringify({ workspace_roots: [directory], ...payload })).quiet().nothrow()
    } catch {
      // Hook failures must never block the agent.
    }
  }

  return {
    event: async ({ event }) => {
      const props = event.properties as
        | { info?: { id?: string }; message?: { role?: string; sessionID?: string; parts?: Array<{ type: string; text?: string }> } }
        | undefined

      if (event.type === "session.created") {
        await invoke({ hook_event_name: "SessionStart", session_id: props?.info?.id })
      } else if (event.type === "session.deleted" || event.type === "session.error") {
        await invoke({ hook_event_name: "SessionEnd", session_id: props?.info?.id })
      } else if (event.type === "session.idle") {
        await invoke({ hook_event_name: "Stop", session_id: props?.info?.id })
      } else if (event.type === "message.updated" && props?.message?.role === "user") {
        const textPart = props.message.parts?.find((p) => p.type === "text")
        await invoke({ hook_event_name: "UserPromptSubmit", session_id: props.message.sessionID, prompt: textPart?.text })
      } else if (event.type === "file.edited") {
        const filePath = (event.properties as { file?: string } | undefined)?.file
        await invoke({ hook_event_name: "AfterFileEdit", file_path: filePath })
      }
    },

    "tool.execute.before": async (input, output) => {
      await invoke({
        hook_event_name: "PreToolUse",
        session_id: input?.sessionID,
        tool_name: input?.tool,
        tool_input: (output as Record<string, unknown> | undefined)?.args,
      })
    },

    "tool.execute.after": async (input, output) => {
      const outObj = output as Record<string, unknown> | undefined
      const meta = outObj?.metadata as Record<string, unknown> | undefined
      const exitCode = typeof meta?.exit === "number" ? (meta.exit as number) : undefined
      const failed = exitCode !== undefined && exitCode !== 0
      await invoke({
        hook_event_name: failed ? "PostToolUseFailure" : "PostToolUse",
        session_id: input?.sessionID,
        tool_name: input?.tool,
        tool_input: (input as Record<string, unknown> | undefined)?.args,
        tool_output: outObj?.output,
      })
    },
  }
}

export default AgentobsOtelHook
`, configPath)
}
