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

func defaultAgentsFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "agentobs", "agents.yaml")
}

func loadRegistry(agentsFile string) (*agents.Registry, error) {
	if agentsFile == "" {
		agentsFile = defaultAgentsFile()
	}
	return agents.LoadRegistry(agentsFile)
}

// hookAgents are agents wired via a hooks.json + wrapper script + own
// otel config (as opposed to the declarative env/json-merge AgentSpec
// system): Cursor needs this because it has no native OTel export at all,
// Copilot and Codex because their hook JSON schemas need array-merge logic
// json-merge's flat key-path setter can't express.
func hookAgents() map[string]agents.HookAgent {
	return map[string]agents.HookAgent{
		"cursor":  agents.CursorAgent{},
		"copilot": agents.CopilotAgent{},
		"codex":   agents.CodexAgent{},
	}
}

func agentNames(reg *agents.Registry) []string {
	names := reg.Names()
	for name := range hookAgents() {
		names = append(names, name)
	}
	return append(names, "opencode")
}

func detectAgent(reg *agents.Registry) string {
	for _, spec := range reg.All() {
		if spec.Detect.Detect() {
			return spec.Name
		}
	}
	for name, agent := range hookAgents() {
		if detector, ok := agent.(interface{ Detect() bool }); ok && detector.Detect() {
			return name
		}
	}
	if (agents.OpenCodeAgent{}).Detect() {
		return "opencode"
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

// writeShellRcBlock writes exportLines between a pair of markers scoped to
// agentName, replacing any previous block for that agent instead of blindly
// appending -- otherwise running `connect` twice (e.g. after toggling a
// prompt like log_user_prompts) duplicates every export line each time.
func writeShellRcBlock(rcPath, agentName string, exportLines []string) error {
	begin := fmt.Sprintf("# BEGIN agentobs connect (%s)", agentName)
	end := fmt.Sprintf("# END agentobs connect (%s)", agentName)
	block := begin + "\n" + strings.Join(exportLines, "\n") + "\n" + end

	existing, err := os.ReadFile(rcPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var newContent string
	if existing != nil && strings.Contains(string(existing), begin) {
		start := strings.Index(string(existing), begin)
		stop := strings.Index(string(existing), end)
		if stop == -1 || stop < start {
			return fmt.Errorf("found %q without matching %q in %s -- fix or remove that block by hand", begin, end, rcPath)
		}
		stop += len(end)
		newContent = string(existing)[:start] + block + string(existing)[stop:]
	} else {
		newContent = string(existing) + "\n" + block + "\n"
	}

	return os.WriteFile(rcPath, []byte(newContent), 0o644)
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
	var agentFlag, endpointFlag, configFlag, agentsFileFlag, authTokenFlag string
	var writeShellRcFlag, logUserPromptsFlag, logToolDetailsFlag *bool
	var nonInteractive bool

	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Wire up an agent's telemetry to point at the collector",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := loadRegistry(agentsFileFlag)
			if err != nil {
				return err
			}

			yamlConfig, err := config.LoadYAML(configFlag)
			if err != nil {
				return err
			}

			var agentFlagPtr *string
			if cmd.Flags().Changed("agent") {
				agentFlagPtr = &agentFlag
			}
			agentName, err := config.Resolve("connect.agent", agentFlagPtr, yamlConfig, func() (string, error) {
				return promptAgent(reg)
			}, nonInteractive, nil)
			if err != nil {
				return err
			}
			names := agentNames(reg)
			if !contains(names, agentName) {
				return fmt.Errorf("unknown agent '%s'. Supported: %s", agentName, strings.Join(names, ", "))
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

			if hookAgent, ok := hookAgents()[agentName]; ok {
				return connectHookAgent(hookAgent, endpoint, yamlConfig, nonInteractive, authTokenFlag)
			}
			if agentName == "opencode" {
				return connectOpenCode(endpoint, yamlConfig, nonInteractive, authTokenFlag)
			}

			spec, ok := reg.Get(agentName)
			if !ok {
				return fmt.Errorf("agent '%s' not found in registry", agentName)
			}

			flagAnswers := map[string]*bool{
				"log_user_prompts": flagOrNil(cmd, "log-user-prompts", logUserPromptsFlag),
				"log_tool_details": flagOrNil(cmd, "log-tool-details", logToolDetailsFlag),
			}

			switch spec.Kind {
			case "json-merge":
				return connectJSONMerge(*spec, endpoint, yamlConfig, nonInteractive, flagAnswers, authTokenFlag)
			case "env":
				return connectEnv(*spec, endpoint, yamlConfig, nonInteractive, flagAnswers, flagOrNil(cmd, "write-shell-rc", writeShellRcFlag), authTokenFlag)
			default:
				return fmt.Errorf("agent %q has unknown kind %q (expected \"env\" or \"json-merge\")", spec.Name, spec.Kind)
			}
		},
	}

	cmd.Flags().StringVar(&agentFlag, "agent", "", "e.g. claude-code")
	cmd.Flags().StringVar(&endpointFlag, "endpoint", "", "")
	cmd.Flags().StringVar(&configFlag, "config", "", "agentobs.yaml path")
	cmd.Flags().StringVar(&agentsFileFlag, "agents-file", "", "extra agent specs (default: ~/.config/agentobs/agents.yaml)")
	cmd.Flags().StringVar(&authTokenFlag, "auth-token", "", "bearer token for a collector running in --secure mode")
	writeShellRcFlag = cmd.Flags().Bool("write-shell-rc", false, "")
	logUserPromptsFlag = cmd.Flags().Bool("log-user-prompts", false, "capture full prompt text (privacy-sensitive, claude-code only)")
	logToolDetailsFlag = cmd.Flags().Bool("log-tool-details", false, "capture bash commands and file paths (privacy-sensitive, claude-code only)")
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

func promptAgent(reg *agents.Registry) (string, error) {
	names := agentNames(reg)
	detected := detectAgent(reg)
	if detected != "" {
		fmt.Printf("Detected agent: %s\n", detected)
	} else {
		detected = names[0]
	}
	var v string
	err := survey.AskOne(&survey.Select{
		Message: "Agent to configure:",
		Options: names,
		Default: detected,
	}, &v)
	return v, err
}

// connectEnv handles any "env"-kind spec (Claude Code, and any future
// env-based agent added via agents.yaml): resolve its prompts, build the
// ordered env vars, then either write them to the shell rc or print them.
func connectEnv(spec agents.AgentSpec, endpoint string, yamlConfig map[string]interface{}, nonInteractive bool, flagAnswers map[string]*bool, writeShellRcFlag *bool, authToken string) error {
	answers := map[string]bool{}
	for _, p := range spec.Prompts {
		def := p.Default
		v, err := config.Resolve("connect."+p.Name, flagAnswers[p.Name], yamlConfig, func() (bool, error) {
			var vv bool
			err := survey.AskOne(&survey.Confirm{Message: p.Message, Default: p.Default}, &vv)
			return vv, err
		}, nonInteractive, &def)
		if err != nil {
			return err
		}
		answers[p.Name] = v
	}

	envVars := spec.ResolveEnvVars(endpoint, answers)
	if authToken != "" {
		envVars = append(envVars, agents.EnvVar{Key: "OTEL_EXPORTER_OTLP_HEADERS", Value: "Authorization=Bearer " + authToken})
	}
	var exportLines []string
	for _, ev := range envVars {
		// Values can come from an untrusted YAML config (--config, or a
		// downloaded agents.yaml via --agents-file), so they're single-quote
		// escaped rather than interpolated into a double-quoted string --
		// double quotes still allow $(...) / `...` command substitution,
		// which would execute when this gets written to a shell rc file.
		if !isShellSafeIdentifier(ev.Key) {
			return fmt.Errorf("invalid env var name %q from agent spec %q", ev.Key, spec.Name)
		}
		exportLines = append(exportLines, fmt.Sprintf("export %s=%s", ev.Key, shellQuote(ev.Value)))
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
		if err := writeShellRcBlock(rcPath, spec.Name, exportLines); err != nil {
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
	printNextSteps(fmt.Sprintf("Use %s as normal", spec.Name), "Token & Cost Usage")
	return nil
}

// connectJSONMerge handles any "json-merge"-kind spec (Gemini CLI, and any
// future settings.json-style agent): resolve its prompts, merge the spec's
// keys into whatever's already at the target path, backing up first.
func connectJSONMerge(spec agents.AgentSpec, endpoint string, yamlConfig map[string]interface{}, nonInteractive bool, flagAnswers map[string]*bool, authToken string) error {
	answers := map[string]bool{}
	for _, p := range spec.Prompts {
		def := p.Default
		v, err := config.Resolve("connect."+p.Name, flagAnswers[p.Name], yamlConfig, func() (bool, error) {
			var vv bool
			err := survey.AskOne(&survey.Confirm{Message: p.Message, Default: p.Default}, &vv)
			return vv, err
		}, nonInteractive, &def)
		if err != nil {
			return err
		}
		answers[p.Name] = v
	}

	targetPath := agents.ExpandHome(spec.Target)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}

	var existing map[string]interface{}
	if data, err := os.ReadFile(targetPath); err == nil {
		if err := backupIfExists(targetPath); err != nil {
			return err
		}
		if err := json.Unmarshal(data, &existing); err != nil {
			return fmt.Errorf("parsing existing %s: %w", targetPath, err)
		}
	}

	merged := spec.ResolveJSONMerge(existing, endpoint, answers)
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(targetPath, out, 0o644); err != nil {
		return err
	}

	fmt.Printf("Wrote %s (merged, not replaced).\n", targetPath)
	if !specSetsEndpoint(spec) {
		fmt.Printf("Also export: export OTEL_EXPORTER_OTLP_ENDPOINT=%s\n", shellQuote(endpoint))
		fmt.Println("(only needed if the collector isn't at this agent's default endpoint)")
	}
	if authToken != "" {
		fmt.Printf("Also export: export OTEL_EXPORTER_OTLP_HEADERS=%s\n", shellQuote("Authorization=Bearer "+authToken))
		fmt.Println("(only if this agent reads that env var for auth headers; not all json-merge agents do)")
	}
	printNextSteps(fmt.Sprintf("Use %s as normal", spec.Name), "Token & Cost Usage")
	return nil
}

func specSetsEndpoint(spec agents.AgentSpec) bool {
	for _, s := range spec.Set {
		if str, ok := s.Value.(string); ok && str == "{{.Endpoint}}" {
			return true
		}
	}
	return false
}

// printNextSteps is shown at the end of every connect path so it's always
// clear where to actually look afterward, not just that files were written.
func printNextSteps(activateHint, dashboard string) {
	fmt.Println()
	fmt.Printf("Next: %s, then open Grafana at http://localhost:3000 -> \"%s\" dashboard.\n", activateHint, dashboard)
	fmt.Println("(Data won't appear until you've actually used the agent for a bit -- give it 30-60s after your first prompt/tool call.)")
}

// hookAgentNextStep is what to tell the user to do after wiring up each
// hook-based agent -- restarting is only needed for Cursor (a running IDE
// process); Copilot/Codex just re-read hooks.json on their next invocation.
var hookAgentNextStep = map[string]string{
	"cursor":  "Restart Cursor IDE to pick up the new hooks",
	"copilot": "Use the Copilot coding agent in this repo as normal",
	"codex":   "Use Codex as normal",
}

func connectHookAgent(agent agents.HookAgent, endpoint string, yamlConfig map[string]interface{}, nonInteractive bool, authToken string) error {
	if _, err := exec.LookPath("agentobs"); err != nil {
		exe, _ := os.Executable()
		fmt.Printf(
			"Warning: `agentobs` isn't on PATH -- %s's hook wrapper script execs it by name and will fail.\n"+
				"Fix: run `go install ./cli/cmd/agentobs` (adds to $GOPATH/bin), or add %s's directory to your PATH.\n",
			agent.Name(), exe,
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
	cfgOut, err := json.MarshalIndent(agent.OtelConfig(endpoint, maskPrompts, authToken), "", "  ")
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

	if codex, ok := agent.(agents.CodexAgent); ok {
		tomlPath, err := codex.TOMLConfigPath()
		if err != nil {
			return err
		}
		if err := agents.EnableHooksFeature(tomlPath); err != nil {
			return err
		}
		fmt.Printf("Enabled codex_hooks in %s.\n", tomlPath)
	}

	dashboard := "Agent Leaderboard (unified)"
	if agent.Name() == "cursor" {
		dashboard = "Cursor Traces"
	}
	printNextSteps(hookAgentNextStep[agent.Name()], dashboard)
	return nil
}

// connectOpenCode wires OpenCode's plugin system. It doesn't fit the
// HookAgent interface -- there's no hooks.json to merge into, the plugin
// file itself (dropped into OpenCode's plugins directory) is the whole
// registration -- so it's its own small path, same shape as connectHookAgent
// otherwise (own config.json, backup-before-overwrite, same next-steps).
func connectOpenCode(endpoint string, yamlConfig map[string]interface{}, nonInteractive bool, authToken string) error {
	agent := agents.OpenCodeAgent{}

	if _, err := exec.LookPath("agentobs"); err != nil {
		exe, _ := os.Executable()
		fmt.Printf(
			"Warning: `agentobs` isn't on PATH -- OpenCode's plugin execs it by name and will fail.\n"+
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

	pluginDir, err := agent.PluginDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		return err
	}

	configPath, err := agent.ConfigPath()
	if err != nil {
		return err
	}
	if err := backupIfExists(configPath); err != nil {
		return err
	}
	cfgOut, err := json.MarshalIndent(agent.OtelConfig(endpoint, maskPrompts, authToken), "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(configPath, cfgOut, 0o644); err != nil {
		return err
	}

	pluginPath, err := agent.PluginPath()
	if err != nil {
		return err
	}
	if err := backupIfExists(pluginPath); err != nil {
		return err
	}
	if err := os.WriteFile(pluginPath, []byte(agent.PluginContent(configPath)), 0o644); err != nil {
		return err
	}

	fmt.Printf("Wrote %s and %s.\n", configPath, pluginPath)
	fmt.Println("(Global scope. For project scope instead, move both files into .opencode/plugins/ in this repo.)")
	printNextSteps("Restart OpenCode to load the new plugin", "Agent Leaderboard (unified)")
	return nil
}

func boolPtr(b bool) *bool { return &b }

func isShellSafeIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
		isDigit := c >= '0' && c <= '9'
		if i == 0 && !isLetter {
			return false
		}
		if !isLetter && !isDigit {
			return false
		}
	}
	return true
}

// shellQuote wraps a value in single quotes for safe embedding in a POSIX
// shell script -- single quotes disable all expansion (including $(...) and
// backticks), unlike the double quotes used previously.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
