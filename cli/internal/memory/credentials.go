package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// dedupWindow bounds how long a synced turn id is remembered. The batch
// endpoint has no idempotency key, so dedup is entirely local; the window
// keeps the file small while still covering out-of-order transcript writes.
const dedupWindow = 24 * time.Hour

// configVersion is the current on-disk schema. A file without it (or with
// a lower number) is migrated on load.
const configVersion = 2

// Project is one directory tree and the space its turns are recorded into.
// Each carries its own watermark and dedup set: a shared watermark would let
// one project's sync skip another project's turns, since advancing it past a
// timestamp hides everything older from every source.
type Project struct {
	// Path is an absolute directory. A turn is pushed only if its working
	// directory is this path or nested under it.
	Path        string               `json:"path"`
	SpaceID     string               `json:"space_id"`
	Watermark   time.Time            `json:"watermark"`
	RecentTurns map[string]time.Time `json:"recent_turns,omitempty"`
}

// Credentials is the on-disk state at ~/.config/agentobs/memory.json.
// It holds an API key, so it is always written 0600.
type Credentials struct {
	Version int    `json:"version"`
	APIKey  string `json:"api_key"`
	// BaseURL overrides DefaultBaseURL, for staging or the legacy host.
	BaseURL string `json:"base_url,omitempty"`
	// Projects is deny-by-default: a turn whose working directory matches
	// no entry is never pushed. An empty list pushes nothing.
	Projects []Project `json:"projects"`
}

// ProjectFor returns the entry whose Path is the longest prefix of cwd, or
// nil when none matches. Longest-prefix matters for nested projects: a
// worktree inside a parent repo routes to its own space when it has an entry.
func (c *Credentials) ProjectFor(cwd string) *Project {
	if cwd == "" {
		return nil
	}
	var best *Project
	for i := range c.Projects {
		if !c.Projects[i].AllowsPath(cwd) {
			continue
		}
		if best == nil || len(c.Projects[i].Path) > len(best.Path) {
			best = &c.Projects[i]
		}
	}
	return best
}

// AllowsPath reports whether p sits inside this project. It compares cleaned
// paths component-wise so /repo-two doesn't match /repo.
func (p *Project) AllowsPath(path string) bool {
	if path == "" || p.Path == "" {
		return false
	}
	target := filepath.Clean(path)
	root := filepath.Clean(p.Path)
	if target == root {
		return true
	}
	return strings.HasPrefix(target, root+string(filepath.Separator))
}

func (p *Project) Seen(turnID string) bool {
	_, ok := p.RecentTurns[turnID]
	return ok
}

// MarkSynced records the given turn ids, advances this project's watermark to
// the newest of them (never backwards), and prunes ids older than the dedup
// window.
func (p *Project) MarkSynced(ids map[string]time.Time) {
	if p.RecentTurns == nil {
		p.RecentTurns = map[string]time.Time{}
	}
	for id, ts := range ids {
		p.RecentTurns[id] = ts
		if ts.After(p.Watermark) {
			p.Watermark = ts
		}
	}

	cutoff := p.Watermark.Add(-dedupWindow)
	for id, ts := range p.RecentTurns {
		if ts.Before(cutoff) {
			delete(p.RecentTurns, id)
		}
	}
}

// credentialsV1 is the original flat schema: one space, one watermark, and
// projects as bare path strings.
type credentialsV1 struct {
	APIKey      string               `json:"api_key"`
	SpaceID     string               `json:"space_id"`
	SpaceName   string               `json:"space_name"`
	BaseURL     string               `json:"base_url"`
	Projects    []string             `json:"projects"`
	Watermark   time.Time            `json:"watermark"`
	RecentTurns map[string]time.Time `json:"recent_turns"`
}

// migrateV1 turns the flat schema into per-project entries, giving every old
// path the old space, watermark, and dedup set. Carrying the watermark
// forward matters: dropping it would re-push the user's entire history, and
// raising it would silently skip turns.
func migrateV1(data []byte) (*Credentials, error) {
	var old credentialsV1
	if err := json.Unmarshal(data, &old); err != nil {
		return nil, err
	}

	creds := &Credentials{
		Version: configVersion,
		APIKey:  old.APIKey,
		BaseURL: old.BaseURL,
	}
	for _, path := range old.Projects {
		recent := make(map[string]time.Time, len(old.RecentTurns))
		for id, ts := range old.RecentTurns {
			recent[id] = ts
		}
		creds.Projects = append(creds.Projects, Project{
			Path:        path,
			SpaceID:     old.SpaceID,
			Watermark:   old.Watermark,
			RecentTurns: recent,
		})
	}
	return creds, nil
}

// CredentialsPath honours AGENTOBS_MEMORY_CONFIG so tests (and anyone
// running more than one space) never touch the real config.
func CredentialsPath() (string, error) {
	if override := os.Getenv("AGENTOBS_MEMORY_CONFIG"); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "agentobs", "memory.json"), nil
}

// LoadCredentials returns (nil, nil) when no config exists -- "not
// connected" is an ordinary state, not an error.
func LoadCredentials() (*Credentials, error) {
	path, err := CredentialsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, err
	}

	if probe.Version < configVersion {
		// Back up before rewriting: this file holds an API key, and a
		// botched migration would cost the user their credential.
		if err := os.WriteFile(path+".bak", data, 0o600); err != nil {
			return nil, err
		}
		creds, err := migrateV1(data)
		if err != nil {
			return nil, err
		}
		if err := SaveCredentials(creds); err != nil {
			return nil, err
		}
		return creds, nil
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	for i := range creds.Projects {
		if creds.Projects[i].RecentTurns == nil {
			creds.Projects[i].RecentTurns = map[string]time.Time{}
		}
	}
	return &creds, nil
}

func SaveCredentials(c *Credentials) error {
	path, err := CredentialsPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// MkdirAll/WriteFile only apply the given mode when they create the
	// path; if it already existed with looser permissions, that would
	// silently persist. Re-assert both modes on every save so the 0600
	// invariant holds even for a file created outside SaveCredentials.
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func DeleteCredentials() error {
	path, err := CredentialsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// splitAndTrim splits a comma-separated list, dropping empty entries.
func splitAndTrim(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// ExpandHome resolves a leading ~/ the same way internal/agents does.
func ExpandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}
