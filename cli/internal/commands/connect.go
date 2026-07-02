package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/spf13/cobra"

	"github.com/anonalabs/agent-observability/cli/internal/agents"
	"github.com/anonalabs/agent-observability/cli/internal/config"
)

const defaultEndpoint = "http://localhost:4317"

var agentNames = []string{"claude-code", "cursor", "gemini-cli"}

func detectAgent() string {
	all := []interface {
		Name() string
		Detect() bool
	}{
		agents.ClaudeCodeAgent{}, agents.CursorAgent{}, agents.GeminiCliAgent{},
	}
	for _, a := range all {
		if a.Detect() {
			return a.Name()
		}
	}
	return ""
}

func shellRcPath() string {
	home, _ := os.UserHomeDir()
	shell := os.Getenv("SHELL")
	if strings.Contains(shell, "zsh") {
		return filepath.Join(home, ".zshrc")
	}
	return filepath.Join(home, ".bashrc")
}

func backupIfExists(path string) error {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path+".bak", data, 0o644)
}

func ConnectCmd() *cobra.Command {
	var agentFlag, endpointFlag, configFlag string
	var writeShellRcFlag, logUserPromptsFlag, logToolDetailsFlag *bool
	var nonInteractive bool

	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Wire up an agent's telemetry env vars to point at the collector",
		RunE: func(cmd *cobra.Command, args []string) error {
			yamlConfig, err := config.LoadYAML(configFlag)
			if err != nil {
				return err
			}

			var agentFlagPtr *string
			if cmd.Flags().Changed("agent") {
				agentFlagPtr = &agentFlag
			}
			agentName, err := config.Resolve("connect.agent", agentFlagPtr, yamlConfig, promptAgent, nonInteractive, nil)
			if err != nil {
				return err
			}
			if !contains(agentNames, agentName) {
				return fmt.Errorf("unknown agent '%s'. Supported: %s", agentName, strings.Join(agentNames, ", "))
			}

			var endpointFlagPtr *string
			if cmd.Flags().Changed("endpoint") {
				endpointFlagPtr = &endpointFlag
			}
			def := defaultEndpoint
			endpoint, err := config.Resolve("connect.endpoint", endpointFlagPtr, yamlConfig, func() (string, error) {
				var v string
				err := survey.AskOne(&survey.Input{Message: "Collector OTLP endpoint:", Default: defaultEndpoint}, &v)
				return v, err
			}, nonInteractive, &def)
			if err != nil {
				return err
			}

			switch agentName {
			case "cursor":
				return connectCursor(endpoint, yamlConfig, nonInteractive)
			case "gemini-cli":
				return connectGeminiCli(endpoint, yamlConfig, nonInteractive)
			default:
				return connectClaudeCode(endpoint, yamlConfig, nonInteractive,
					flagOrNil(cmd, "log-user-prompts", logUserPromptsFlag),
					flagOrNil(cmd, "log-tool-details", logToolDetailsFlag),
					flagOrNil(cmd, "write-shell-rc", writeShellRcFlag),
				)
			}
		},
	}

	cmd.Flags().StringVar(&agentFlag, "agent", "", "e.g. claude-code")
	cmd.Flags().StringVar(&endpointFlag, "endpoint", "", "")
	cmd.Flags().StringVar(&configFlag, "config", "", "agentobs.yaml path")
	writeShellRcFlag = cmd.Flags().Bool("write-shell-rc", false, "")
	logUserPromptsFlag = cmd.Flags().Bool("log-user-prompts", false, "capture full prompt text (privacy-sensitive)")
	logToolDetailsFlag = cmd.Flags().Bool("log-tool-details", false, "capture bash commands and file paths (privacy-sensitive)")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "")
	cmd.Flags().BoolVar(&nonInteractive, "yes", false, "")

	return cmd
}

func flagOrNil(cmd *cobra.Command, name string, val *bool) *bool {
	if cmd.Flags().Changed(name) {
		return val
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func promptAgent() (string, error) {
	detected := detectAgent()
	if detected != "" {
		fmt.Printf("Detected agent: %s\n", detected)
	} else {
		detected = "claude-code"
	}
	var v string
	err := survey.AskOne(&survey.Select{
		Message: "Agent to configure:",
		Options: agentNames,
		Default: detected,
	}, &v)
	return v, err
}

func connectClaudeCode(endpoint string, yamlConfig map[string]interface{}, nonInteractive bool, logUserPromptsFlag, logToolDetailsFlag, writeShellRcFlag *bool) error {
	logUserPrompts, err := config.Resolve("connect.log_user_prompts", logUserPromptsFlag, yamlConfig, func() (bool, error) {
		var v bool
		err := survey.AskOne(&survey.Confirm{Message: "Capture full user prompt text? (privacy-sensitive)", Default: false}, &v)
		return v, err
	}, nonInteractive, boolPtr(false))
	if err != nil {
		return err
	}

	logToolDetails, err := config.Resolve("connect.log_tool_details", logToolDetailsFlag, yamlConfig, func() (bool, error) {
		var v bool
		err := survey.AskOne(&survey.Confirm{Message: "Capture tool details (bash commands, file paths)? (privacy-sensitive)", Default: false}, &v)
		return v, err
	}, nonInteractive, boolPtr(false))
	if err != nil {
		return err
	}

	agent := agents.ClaudeCodeAgent{}
	envVars := agent.EnvVars(endpoint, logUserPrompts, logToolDetails)
	var exportLines []string
	for _, ev := range envVars {
		exportLines = append(exportLines, fmt.Sprintf("export %s=\"%s\"", ev.Key, ev.Value))
	}

	rcPath := shellRcPath()
	shouldWrite, err := config.Resolve("connect.write_shell_rc", writeShellRcFlag, yamlConfig, func() (bool, error) {
		var v bool
		err := survey.AskOne(&survey.Confirm{Message: fmt.Sprintf("Append these to %s?", rcPath), Default: false}, &v)
		return v, err
	}, nonInteractive, boolPtr(false))
	if err != nil {
		return err
	}

	if shouldWrite {
		f, err := os.OpenFile(rcPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := f.WriteString("\n# added by `agentobs connect`\n" + strings.Join(exportLines, "\n") + "\n"); err != nil {
			return err
		}
		fmt.Printf("Wrote env vars to %s. Restart your shell or `source %s`.\n", rcPath, rcPath)
	} else {
		fmt.Println("Copy these into your shell:")
		fmt.Println()
		for _, line := range exportLines {
			fmt.Println(line)
		}
	}
	printNextSteps("Use `claude` as normal", "Token & Cost Usage")
	return nil
}

// printNextSteps is shown at the end of every connect path so it's always
// clear where to actually look afterward, not just that files were written.
func printNextSteps(activateHint, dashboard string) {
	fmt.Println()
	fmt.Printf("Next: %s, then open Grafana at http://localhost:3000 -> \"%s\" dashboard.\n", activateHint, dashboard)
	fmt.Println("(Data won't appear until you've actually used the agent for a bit -- give it 30-60s after your first prompt/tool call.)")
}

func connectGeminiCli(endpoint string, yamlConfig map[string]interface{}, nonInteractive bool) error {
	logPrompts, err := config.Resolve("connect.log_prompts", (*bool)(nil), yamlConfig, func() (bool, error) {
		var v bool
		err := survey.AskOne(&survey.Confirm{Message: "Log full prompt text? (privacy-sensitive)", Default: false}, &v)
		return v, err
	}, nonInteractive, boolPtr(false))
	if err != nil {
		return err
	}

	agent := agents.GeminiCliAgent{}
	settingsPath, err := agent.SettingsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return err
	}

	var existing map[string]interface{}
	if data, err := os.ReadFile(settingsPath); err == nil {
		if err := backupIfExists(settingsPath); err != nil {
			return err
		}
		if err := json.Unmarshal(data, &existing); err != nil {
			return fmt.Errorf("parsing existing %s: %w", settingsPath, err)
		}
	}

	merged := agent.MergeSettings(existing, logPrompts)
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(settingsPath, out, 0o644); err != nil {
		return err
	}

	fmt.Printf("Wrote %s (merged, not replaced).\n", settingsPath)
	fmt.Printf("Also export: export OTEL_EXPORTER_OTLP_ENDPOINT=\"%s\"\n", endpoint)
	fmt.Println("(only needed if the collector isn't at the settings.json default of localhost:4317)")
	printNextSteps("Use `gemini` as normal", "Token & Cost Usage")
	return nil
}

func connectCursor(endpoint string, yamlConfig map[string]interface{}, nonInteractive bool) error {
	agent := agents.CursorAgent{}

	if _, err := exec.LookPath("agentobs"); err != nil {
		exe, _ := os.Executable()
		fmt.Printf(
			"Warning: `agentobs` isn't on PATH -- Cursor's hook wrapper script execs it by name and will fail.\n"+
				"Fix: run `go install ./cli/cmd/agentobs` (adds to $GOPATH/bin), or add %s's directory to your PATH.\n",
			exe,
		)
	}

	maskPrompts, err := config.Resolve("connect.mask_prompts", (*bool)(nil), yamlConfig, func() (bool, error) {
		var v bool
		err := survey.AskOne(&survey.Confirm{Message: "Mask prompts/file paths/emails? (privacy)", Default: false}, &v)
		return v, err
	}, nonInteractive, boolPtr(false))
	if err != nil {
		return err
	}

	hooksDir, err := agent.HooksDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return err
	}

	configPath, err := agent.ConfigPath()
	if err != nil {
		return err
	}
	if err := backupIfExists(configPath); err != nil {
		return err
	}
	cfgOut, err := json.MarshalIndent(agent.OtelConfig(endpoint, maskPrompts), "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(configPath, cfgOut, 0o644); err != nil {
		return err
	}

	wrapperPath, err := agent.WrapperScriptPath()
	if err != nil {
		return err
	}
	if err := backupIfExists(wrapperPath); err != nil {
		return err
	}
	wrapperContent, err := agent.WrapperScript()
	if err != nil {
		return err
	}
	if err := os.WriteFile(wrapperPath, []byte(wrapperContent), 0o755); err != nil {
		return err
	}

	hooksJSONPath, err := agent.HooksJSONPath()
	if err != nil {
		return err
	}
	var existingHooks map[string]interface{}
	if data, err := os.ReadFile(hooksJSONPath); err == nil {
		if err := backupIfExists(hooksJSONPath); err != nil {
			return err
		}
		if err := json.Unmarshal(data, &existingHooks); err != nil {
			return fmt.Errorf("parsing existing %s: %w", hooksJSONPath, err)
		}
	}
	merged, err := agent.HooksJSON(existingHooks)
	if err != nil {
		return err
	}
	hooksOut, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(hooksJSONPath, hooksOut, 0o644); err != nil {
		return err
	}

	fmt.Printf("Wrote %s, %s, and %s (merged, not replaced).\n", configPath, wrapperPath, hooksJSONPath)
	printNextSteps("Restart Cursor IDE to pick up the new hooks", "Cursor Traces")
	return nil
}

func boolPtr(b bool) *bool { return &b }
