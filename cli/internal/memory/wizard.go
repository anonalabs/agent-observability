package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/AlecAivazis/survey/v2"

	"github.com/anonalabs/agent-observability/cli/internal/agents"
)

const signupURL = "https://docs.anonalabs.com/quickstart"

// createNewSpaceOption is the sentinel entry in the space picker.
const createNewSpaceOption = "Create a new space..."

// OfferConnect asks whether to push conversation turns to AnonaMemory and,
// if so, walks through key entry, space selection, the project allowlist,
// and a first sync. It never returns an error that should fail `connect` --
// telemetry setup has already succeeded by this point, and an optional
// add-on must not undo it. Failures are printed and swallowed.
func OfferConnect(cwd string, nonInteractive bool, enabled *bool) error {
	if nonInteractive {
		if enabled != nil && *enabled {
			fmt.Println("Skipping AnonaMemory: --memory needs an API key, which can't be collected non-interactively.")
			fmt.Println("Run `agentobs connect` interactively, or write ~/.config/agentobs/memory.json by hand (see docs/anonamemory.md).")
		}
		return nil
	}

	if enabled == nil {
		want := false
		prompt := &survey.Confirm{
			Message: "Also push prompts and responses to AnonaMemory?",
			Default: false,
		}
		if err := survey.AskOne(prompt, &want); err != nil {
			return nil
		}
		if !want {
			return nil
		}
	} else if !*enabled {
		return nil
	}

	if err := runWizard(cwd); err != nil {
		fmt.Printf("AnonaMemory setup didn't complete: %v\n", err)
		fmt.Println("Telemetry is still wired up. Re-run `agentobs connect` to try again.")
	}
	return nil
}

func runWizard(cwd string) error {
	fmt.Println()
	fmt.Printf("Create an API key at %s (signing up doesn't make one for you).\n", signupURL)

	var apiKey string
	if err := survey.AskOne(&survey.Password{Message: "AnonaMemory API key:"}, &apiKey, survey.WithValidator(survey.Required)); err != nil {
		return err
	}

	client := NewClient(apiKey)

	spaces, err := client.ListSpaces()
	if err != nil {
		return fmt.Errorf("listing spaces: %w", err)
	}

	spaceID, spaceName, err := chooseSpace(client, spaces)
	if err != nil {
		return err
	}

	projects, err := chooseProjects(cwd)
	if err != nil {
		return err
	}

	creds := &Credentials{
		Version: configVersion,
		APIKey:  apiKey,
	}
	for _, path := range projects {
		creds.Projects = append(creds.Projects, Project{
			Path:        path,
			SpaceID:     spaceID,
			RecentTurns: map[string]time.Time{},
		})
	}
	if err := SaveCredentials(creds); err != nil {
		return err
	}

	path, _ := CredentialsPath()
	fmt.Printf("Saved %s (mode 0600).\n", path)

	source, err := NewClaudeCodeSource()
	if err != nil {
		return err
	}
	results, syncErr := SyncAll(creds, client, []TranscriptSource{source}, NewClickHouse(), Options{})
	if err := SaveCredentials(creds); err != nil {
		return err
	}
	if syncErr != nil {
		return fmt.Errorf("first sync: %w", syncErr)
	}

	pushed := 0
	var enrichErr, promptOnlyErr error
	for _, result := range results {
		pushed += result.Pushed
		if result.EnrichErr != nil {
			enrichErr = result.EnrichErr
		}
		if result.PromptOnlyErr != nil {
			promptOnlyErr = result.PromptOnlyErr
		}
	}

	fmt.Printf("Pushed %d turns to %s.\n", pushed, spaceName)
	// Mirrors the gating and wording `agentobs memory sync` uses for these
	// two failure modes, so the user doesn't see contradictory phrasing
	// depending on which command happened to run the sync.
	if enrichErr != nil && pushed > 0 {
		fmt.Println("ClickHouse was unreachable for cost/token enrichment -- turns went out without that context.")
	}
	if promptOnlyErr != nil {
		fmt.Println("ClickHouse was unreachable, so only Claude Code transcripts were read this run.")
	}
	autoSync := false
	if err := survey.AskOne(&survey.Confirm{
		Message: "Sync automatically when a Claude Code session ends?",
		Default: true,
	}, &autoSync); err == nil && autoSync {
		path, err := agents.RegisterStopHook()
		if err != nil {
			fmt.Printf("Could not register the Stop hook: %v\n", err)
			fmt.Println("Sync still works manually with `agentobs memory sync`.")
		} else {
			fmt.Printf("Registered a Stop hook in %s. New sessions sync when they end.\n", path)
		}
	}

	fmt.Println()
	fmt.Println("Keep it current with an hourly cron entry:")
	fmt.Println("  0 * * * * agentobs memory sync --quiet")
	fmt.Println("Check state any time with `agentobs memory status`.")
	return nil
}

// chooseSpace shows existing spaces plus a create option. With no spaces at
// all, it goes straight to creation -- an empty picker is a dead end.
func chooseSpace(client *Client, spaces []Space) (string, string, error) {
	if len(spaces) == 0 {
		fmt.Println("No spaces on this account yet.")
		return createSpace(client)
	}

	options := make([]string, 0, len(spaces)+1)
	byName := map[string]string{}
	for _, space := range spaces {
		options = append(options, space.Name)
		byName[space.Name] = space.SpaceID
	}
	options = append(options, createNewSpaceOption)

	var chosen string
	prompt := &survey.Select{Message: "Space to record into:", Options: options}
	if err := survey.AskOne(prompt, &chosen); err != nil {
		return "", "", err
	}
	if chosen == createNewSpaceOption {
		return createSpace(client)
	}
	return byName[chosen], chosen, nil
}

func createSpace(client *Client) (string, string, error) {
	var name string
	prompt := &survey.Input{Message: "New space name:", Default: "coding-agents"}
	if err := survey.AskOne(prompt, &name, survey.WithValidator(survey.Required)); err != nil {
		return "", "", err
	}
	space, err := client.CreateSpace(name, "AI coding agent conversation history")
	if err != nil {
		return "", "", fmt.Errorf("creating space: %w", err)
	}
	fmt.Printf("Created space %s (%s).\n", space.Name, space.SpaceID)
	return space.SpaceID, space.Name, nil
}

// chooseProjects collects the allowlist. It is deny-by-default: only turns
// whose working directory sits inside one of these paths are ever pushed.
func chooseProjects(cwd string) ([]string, error) {
	fmt.Println()
	fmt.Println("Only sessions under these directories are pushed. Everything else stays local.")

	var raw string
	prompt := &survey.Input{
		Message: "Project directories (comma-separated):",
		Default: cwd,
	}
	if err := survey.AskOne(prompt, &raw, survey.WithValidator(survey.Required)); err != nil {
		return nil, err
	}

	return absolutizeProjects(raw)
}

// absolutizeProjects turns the comma-separated raw input into absolute
// directory paths. It's split out from chooseProjects so this parsing --
// the part that actually matters for correctness -- can be unit tested
// without stubbing survey's interactive prompt.
//
// AllowsPath does a component-wise comparison against absolute paths stored
// in memory.json, so anything relative here would silently match nothing --
// deny-by-default fails closed, but the symptom is a baffling "0 turns
// pushed" with no indication why. ExpandHome only resolves a leading "~/";
// a bare "~" needs its own case before filepath.Abs, which would otherwise
// leave it as a literal "~" directory relative to cwd.
func absolutizeProjects(raw string) ([]string, error) {
	var projects []string
	for _, part := range splitAndTrim(raw) {
		expanded := ExpandHome(part)
		if expanded == "~" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("resolving ~: %w", err)
			}
			expanded = home
		}
		abs, err := filepath.Abs(expanded)
		if err != nil {
			return nil, fmt.Errorf("resolving %q: %w", part, err)
		}
		projects = append(projects, abs)
	}
	if len(projects) == 0 {
		return nil, fmt.Errorf("at least one project directory is required")
	}
	return projects, nil
}
