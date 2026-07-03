package commands

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/anonalabs/agent-observability/cli/internal/agents"
)

func AgentsCmd() *cobra.Command {
	agentsCmd := &cobra.Command{
		Use:   "agents",
		Short: "Manage the agent registry",
	}

	var agentsFileFlag string
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List every registered agent (builtin + agents.yaml) and its detection status",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := loadRegistry(agentsFileFlag)
			if err != nil {
				return err
			}

			fmt.Printf("%-16s %-12s %s\n", "AGENT", "KIND", "DETECTED")
			for _, spec := range reg.All() {
				detected := "no"
				if spec.Detect.Detect() {
					detected = "yes"
				}
				fmt.Printf("%-16s %-12s %s\n", spec.Name, spec.Kind, detected)
			}

			names := make([]string, 0, len(hookAgents()))
			for name := range hookAgents() {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				detected := "no"
				if detector, ok := hookAgents()[name].(interface{ Detect() bool }); ok && detector.Detect() {
					detected = "yes"
				}
				fmt.Printf("%-16s %-12s %s\n", name, "hook-shim", detected)
			}

			openCodeDetected := "no"
			if (agents.OpenCodeAgent{}).Detect() {
				openCodeDetected = "yes"
			}
			fmt.Printf("%-16s %-12s %s\n", "opencode", "plugin", openCodeDetected)
			return nil
		},
	}
	listCmd.Flags().StringVar(&agentsFileFlag, "agents-file", "", "extra agent specs (default: ~/.config/agentobs/agents.yaml)")

	agentsCmd.AddCommand(listCmd)
	return agentsCmd
}
