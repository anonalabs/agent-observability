package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/anonalabs/agent-observability/cli/internal/cursorhook"
)

func CursorHookCmd() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:    "cursor-hook",
		Short:  "Internal: invoked by Cursor's hooks.json to emit OTel spans",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := cursorhook.Load(configPath)

			input, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}

			var hookData map[string]interface{}
			if err := json.Unmarshal(input, &hookData); err != nil {
				fmt.Fprintf(os.Stderr, "Error: Invalid JSON input: %v\n", err)
				os.Exit(1)
			}

			response, err := cursorhook.ProcessHook(cfg, hookData)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			out, err := json.Marshal(response)
			if err != nil {
				return err
			}
			fmt.Println(string(out))
			return nil
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to JSON configuration file")

	return cmd
}
