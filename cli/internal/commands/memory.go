package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	memoryCmd.AddCommand(memoryHookCmd())
	memoryCmd.AddCommand(memoryProjectsCmd())
	return memoryCmd
}

// reportResult prints one project's outcome. Routine bookkeeping goes to
// stdout and is suppressed by --quiet; degradations go to stderr regardless,
// because the cron line the wizard prints uses --quiet and a silently
// degraded sync is worse than a noisy one.
func reportResult(cmd *cobra.Command, path string, result memory.Result, dryRun, quiet bool) {
	if !quiet {
		verb := "Pushed"
		if dryRun {
			verb = "Would push"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s %d turns.\n", path, verb, result.Pushed)
		if result.Deduped > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: skipped %d already-synced turns.\n", path, result.Deduped)
		}
		if result.Filtered > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: skipped %d turns outside this project.\n", path, result.Filtered)
		}
	}
	if result.SkippedFiles > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: skipped %d unreadable transcript files.\n", path, result.SkippedFiles)
	}
	if result.SkippedRows > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: ClickHouse returned %d rows that could not be read.\n", path, result.SkippedRows)
	}
	if result.EnrichErr != nil && result.Pushed > 0 {
		verb := "went out"
		if dryRun {
			verb = "would go out"
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: ClickHouse was unreachable for cost/token enrichment -- turns %s without that context.\n", path, verb)
	}
	if result.PromptOnlyErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: ClickHouse was unreachable for prompt-only turns -- Cursor turns could not be read this run, so only Claude Code transcripts were included.\n", path)
	}
	if result.SyncErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: sync failed: %v\n", path, result.SyncErr)
	}
}

func memorySyncCmd() *cobra.Command {
	var quiet, dryRun bool
	var since, project string

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

			if project != "" {
				p := creds.ProjectFor(project)
				if p == nil {
					return fmt.Errorf("no configured project matches %q -- run `agentobs memory projects` to see them", project)
				}
				result, err := memory.SyncProject(p, newClient(creds), []memory.TranscriptSource{source}, clickhouse, opts)
				if !dryRun {
					if saveErr := memory.SaveCredentials(creds); saveErr != nil {
						return saveErr
					}
				}
				reportResult(cmd, p.Path, result, dryRun, quiet)
				return err
			}

			results, syncErr := memory.SyncAll(
				creds,
				newClient(creds),
				[]memory.TranscriptSource{source},
				clickhouse,
				opts,
			)

			if !dryRun {
				if err := memory.SaveCredentials(creds); err != nil {
					return err
				}
			}

			for path, result := range results {
				reportResult(cmd, path, result, dryRun, quiet)
			}
			return syncErr
		},
	}

	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress the routine success line (for cron); degradation warnings and unreadable-input counts still print, to stderr")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be pushed without pushing or advancing the watermark")
	cmd.Flags().StringVar(&since, "since", "", "override the watermark, e.g. 24h or 7d")
	cmd.Flags().StringVar(&project, "project", "", "sync only this configured project path")

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

			fmt.Fprintf(cmd.OutOrStdout(), "%-40s %-20s %-24s %s\n", "PROJECT", "SPACE", "LAST SYNC", "PENDING")

			source, err := memory.NewClaudeCodeSource()
			if err != nil {
				return err
			}
			results, _ := memory.SyncAll(
				creds,
				newClient(creds),
				[]memory.TranscriptSource{source},
				memory.NewClickHouse(),
				memory.Options{DryRun: true},
			)

			degraded := false
			for _, p := range creds.Projects {
				last := "never"
				if !p.Watermark.IsZero() {
					last = p.Watermark.Format(time.RFC3339)
				}
				r := results[p.Path]
				if r.EnrichErr != nil || r.PromptOnlyErr != nil {
					degraded = true
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-40s %-20s %-24s %d\n", p.Path, p.SpaceID, last, r.Pushed)
			}

			if degraded {
				fmt.Fprintln(cmd.ErrOrStderr())
				fmt.Fprintln(cmd.ErrOrStderr(), "ClickHouse is unreachable: pending counts above are from transcripts only, and prompt-only agents (Cursor, Copilot, Codex, OpenCode) contribute nothing.")
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

// memoryHookCmd is invoked by Claude Code's Stop hook. It must always exit 0
// and never write to stdout or stderr: Claude Code reads a hook's output, and
// an optional connector must never be able to break the agent.
func memoryHookCmd() *cobra.Command {
	var detached bool
	var payloadFile string

	cmd := &cobra.Command{
		Use:    "hook",
		Short:  "Internal: invoked by Claude Code's Stop hook to sync the finished session",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The parent wrote the payload to payloadFile and handed us its
			// contents on stdin. We're the only side that can safely delete
			// it: the parent exits right after starting us, before it could
			// know we've actually finished reading.
			if detached && payloadFile != "" {
				defer os.Remove(payloadFile)
			}

			payload, err := memory.ParseHookPayload(cmd.InOrStdin())
			if err != nil {
				memory.LogHook("bad payload: %v", err)
				return nil
			}

			creds, err := memory.LoadCredentials()
			if err != nil || creds == nil {
				memory.LogHook("no usable config (%v) -- nothing to sync", err)
				return nil
			}

			p := creds.ProjectFor(payload.CWD)
			if p == nil {
				memory.LogHook("cwd %s matches no configured project", payload.CWD)
				return nil
			}

			if !detached {
				// Claude Code waits for the hook to exit. A sync is network
				// I/O against AnonaMemory, so doing it inline would delay the
				// end of every session. Re-exec detached and return at once.
				if err := respawnDetached(payload); err != nil {
					memory.LogHook("could not detach: %v", err)
				}
				return nil
			}

			lock, err := memory.ProjectLock(p)
			if err != nil {
				memory.LogHook("lock error for %s: %v", p.Path, err)
				return nil
			}
			if lock == nil {
				memory.LogHook("%s already syncing -- skipping this run", p.Path)
				return nil
			}
			defer lock.Unlock()

			source, err := memory.NewClaudeCodeSource()
			if err != nil {
				memory.LogHook("transcript source: %v", err)
				return nil
			}

			result, syncErr := memory.SyncProject(p, newClient(creds), []memory.TranscriptSource{source}, memory.NewClickHouse(), memory.Options{})
			if saveErr := memory.SaveCredentials(creds); saveErr != nil {
				memory.LogHook("saving config: %v", saveErr)
			}
			if syncErr != nil {
				memory.LogHook("%s sync failed after %d turns: %v", p.Path, result.Pushed, syncErr)
				return nil
			}
			memory.LogHook("%s pushed %d turns (session %s)", p.Path, result.Pushed, payload.SessionID)
			return nil
		},
	}

	cmd.Flags().BoolVar(&detached, "detached", false, "internal: the re-exec'd child that performs the sync")
	cmd.Flags().StringVar(&payloadFile, "payload-file", "", "internal: temp file holding the hook payload, removed by the child once read")
	return cmd
}

// respawnDetached re-runs this command with --detached, handing the payload
// to the child on its stdin via a temp file, then returns without waiting.
//
// The payload can't go through an in-memory io.Reader here: when Cmd.Stdin
// is anything other than an *os.File, os/exec feeds it to the child through
// an unawaited background goroutine that copies into a pipe. Since this
// process calls Release and exits immediately after Start -- deliberately,
// so Claude Code is never blocked -- that goroutine is racing process exit,
// and losing the race closes the pipe before anything was written, handing
// the child an empty payload. A real file sidesteps the race entirely: it's
// written and closed before the child ever starts, and os/exec dups an
// *os.File straight into the child with no copy goroutine involved.
func respawnDetached(payload memory.HookPayload) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	// os.TempDir, not the config dir: this file is transient and holds
	// nothing as sensitive as the API key the config dir's 0700/0600
	// permissions are protecting, so it shouldn't share that directory.
	tmp, err := os.CreateTemp("", "agentobs-hook-*.json")
	if err != nil {
		return err
	}
	path := tmp.Name()
	// The payload names a working directory path, not a secret, but 0600
	// costs nothing -- os.CreateTemp already creates it with that mode.
	if _, err := tmp.Write(encoded); err != nil {
		tmp.Close()
		os.Remove(path)
		return err
	}
	// Flush before the child can possibly read it -- this is what makes
	// the handoff race-free.
	if err := tmp.Close(); err != nil {
		os.Remove(path)
		return err
	}

	stdin, err := os.Open(path)
	if err != nil {
		os.Remove(path)
		return err
	}
	defer stdin.Close()

	// --payload-file tells the detached child what to remove once it has
	// read stdin; this process exits too soon after Start to remove it
	// itself.
	child := exec.Command(exe, "memory", "hook", "--detached", "--payload-file", path)
	child.Stdin = stdin
	child.Stdout = nil
	child.Stderr = nil
	child.SysProcAttr = detachedSysProcAttr()
	if err := child.Start(); err != nil {
		os.Remove(path)
		return err
	}
	// Not waited on deliberately: the child outlives this process so Claude
	// Code is never blocked on the sync.
	return child.Process.Release()
}

func memoryProjectsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "projects",
		Short: "List configured projects, their spaces, and when each last synced",
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := requireCredentials()
			if err != nil {
				return err
			}
			if len(creds.Projects) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No projects configured. Run `agentobs connect` to add one.")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%-50s %-24s %s\n", "PROJECT", "SPACE", "LAST SYNC")
			for _, p := range creds.Projects {
				last := "never"
				if !p.Watermark.IsZero() {
					last = p.Watermark.Format(time.RFC3339)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-50s %-24s %s\n", p.Path, p.SpaceID, last)
			}
			return nil
		},
	}
}
