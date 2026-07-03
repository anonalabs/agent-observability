package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/anonalabs/agent-observability/cli/internal/commands"
)

func main() {
	root := &cobra.Command{
		Use:   "agentobs",
		Short: "Install, connect, and export telemetry for AI coding agents",
	}

	root.AddCommand(commands.InstallCmd())
	root.AddCommand(commands.ConnectCmd())
	root.AddCommand(commands.ExportCmd())
	root.AddCommand(commands.StatusCmd())
	root.AddCommand(commands.CursorHookCmd())
	root.AddCommand(commands.AgentsCmd())
	root.AddCommand(commands.ConfigAlertsCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
