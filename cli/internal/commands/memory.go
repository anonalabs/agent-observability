package commands

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/anonalabs/agent-observability/cli/internal/memory"
)

// requireCredentials loads the memory config, turning "not connected" into
// an actionable error rather than a nil dereference.
func requireCredentials() (*memory.Credentials, error) {
	creds, err := memory.LoadCredentials()
	if err != nil {
		return nil, err
	}
	if creds == nil {
		return nil, fmt.Errorf("not connected to AnonaMemory -- run `agentobs connect` and answer yes, or see docs/anonamemory.md")
	}
	return creds, nil
}

func newClient(creds *memory.Credentials) *memory.Client {
	client := memory.NewClient(creds.APIKey)
	if creds.BaseURL != "" {
		client.BaseURL = creds.BaseURL
	}
	return client
}

func MemoryCmd() *cobra.Command {
	memoryCmd := &cobra.Command{
		Use:   "memory",
		Short: "Push agent prompts and responses to AnonaMemory",
	}

	memoryCmd.AddCommand(memorySyncCmd())
	memoryCmd.AddCommand(memoryStatusCmd())
	memoryCmd.AddCommand(memoryDisconnectCmd())
	return memoryCmd
}

func memorySyncCmd() *cobra.Command {
	var quiet, dryRun bool
	var since string

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Push every turn newer than the last sync to AnonaMemory",
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := requireCredentials()
			if err != nil {
				return err
			}

			opts := memory.Options{DryRun: dryRun}
			if since != "" {
				seconds, err := parseSince(since)
				if err != nil {
					return err
				}
				at := time.Now().Add(-time.Duration(seconds) * time.Second)
				opts.Since = &at
			}

			source, err := memory.NewClaudeCodeSource()
			if err != nil {
				return err
			}
			clickhouse := memory.NewClickHouse()

			result, syncErr := memory.Sync(
				creds,
				newClient(creds),
				[]memory.TranscriptSource{source},
				clickhouse,
				opts,
			)

			// Persist whatever progress was made before reporting failure,
			// so a partial run isn't repeated from the start.
			if !dryRun {
				if err := memory.SaveCredentials(creds); err != nil {
					return err
				}
			}
			if syncErr != nil {
				return syncErr
			}

			if !quiet {
				verb := "Pushed"
				if dryRun {
					verb = "Would push"
				}
				fmt.Printf("%s %d turns to space %s.\n", verb, result.Pushed, creds.SpaceName)
				if result.Deduped > 0 {
					fmt.Printf("Skipped %d already-synced turns.\n", result.Deduped)
				}
				if result.Filtered > 0 {
					fmt.Printf("Skipped %d turns outside the project allowlist.\n", result.Filtered)
				}
				if result.SkippedFiles > 0 {
					fmt.Printf("Skipped %d unreadable transcript files.\n", result.SkippedFiles)
				}
				if result.SkippedRows > 0 {
					fmt.Printf("ClickHouse returned %d rows that could not be read (bad timestamp or cost) -- those turns' data isn't lost, just missing from this sync.\n", result.SkippedRows)
				}
				if result.EnrichErr != nil && result.Pushed > 0 {
					verb := "went out"
					if dryRun {
						verb = "would go out"
					}
					fmt.Printf("ClickHouse was unreachable for cost/token enrichment -- turns %s without that context.\n", verb)
				}
				if result.PromptOnlyErr != nil {
					fmt.Println("ClickHouse was unreachable for prompt-only turns -- Cursor/Copilot/Codex/OpenCode turns could not be read this run, so only Claude Code transcripts were included.")
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress the summary (for cron)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be pushed without pushing or advancing the watermark")
	cmd.Flags().StringVar(&since, "since", "", "override the watermark, e.g. 24h or 7d")

	return cmd
}

func memoryStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the configured space, last sync, and what's pending",
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := requireCredentials()
			if err != nil {
				return err
			}

			fmt.Printf("%-16s %s (%s)\n", "space", creds.SpaceName, creds.SpaceID)
			if creds.Watermark.IsZero() {
				fmt.Printf("%-16s never\n", "last sync")
			} else {
				fmt.Printf("%-16s %s\n", "last sync", creds.Watermark.Format(time.RFC3339))
			}
			for i, project := range creds.Projects {
				label := ""
				if i == 0 {
					label = "projects"
				}
				fmt.Printf("%-16s %s\n", label, project)
			}

			source, err := memory.NewClaudeCodeSource()
			if err != nil {
				return err
			}
			result, err := memory.Sync(
				creds,
				newClient(creds),
				[]memory.TranscriptSource{source},
				memory.NewClickHouse(),
				memory.Options{DryRun: true},
			)
			if err != nil {
				return err
			}
			fmt.Printf("%-16s %d\n", "pending turns", result.Pushed)

			if result.EnrichErr != nil {
				fmt.Println()
				fmt.Println("ClickHouse is unreachable for cost/token enrichment -- the pending count above is accurate, but synced turns would go out without cost/tool context.")
			}

			if result.PromptOnlyErr != nil {
				fmt.Println()
				fmt.Println("ClickHouse is unreachable, so prompt-only agents (Cursor, Copilot, Codex, OpenCode) contribute nothing.")
			} else if result.Pushed == 0 {
				fmt.Println()
				fmt.Println("Nothing pending. Note that Cursor, Copilot, Codex, and OpenCode only contribute prompts if")
				fmt.Println("you enabled prompt logging when connecting them (declining the hook shim's mask-prompts")
				fmt.Println("question) -- that defaults to off. Gemini CLI is not supported for memory sync.")
			}
			return nil
		},
	}
}

func memoryDisconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect",
		Short: "Delete the local AnonaMemory config (nothing is removed server-side)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := memory.CredentialsPath()
			if err != nil {
				return err
			}
			_, statErr := os.Stat(path)
			existed := statErr == nil
			if err := memory.DeleteCredentials(); err != nil {
				return err
			}
			if existed {
				fmt.Printf("Removed %s. Memories already pushed to AnonaMemory are untouched.\n", path)
			} else {
				fmt.Printf("No local config at %s -- nothing to remove.\n", path)
			}
			return nil
		},
	}
}
