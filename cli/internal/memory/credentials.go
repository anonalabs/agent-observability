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

// Credentials is the on-disk state at ~/.config/agentobs/memory.json.
// It holds an API key, so it is always written 0600.
type Credentials struct {
	APIKey    string `json:"api_key"`
	SpaceID   string `json:"space_id"`
	SpaceName string `json:"space_name"`
	// BaseURL overrides DefaultBaseURL, for staging or the legacy host.
	BaseURL string `json:"base_url,omitempty"`
	// Projects are absolute directory paths. A turn is pushed only if its
	// working directory is one of these or nested under one. An empty list
	// means nothing is pushed -- it is never implicitly "all".
	Projects    []string             `json:"projects"`
	Watermark   time.Time            `json:"watermark"`
	RecentTurns map[string]time.Time `json:"recent_turns,omitempty"`
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
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	if creds.RecentTurns == nil {
		creds.RecentTurns = map[string]time.Time{}
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

// AllowsPath reports whether p sits inside the project allowlist. It
// compares cleaned paths component-wise so /repo-two doesn't match /repo.
func (c *Credentials) AllowsPath(p string) bool {
	if p == "" {
		return false
	}
	target := filepath.Clean(p)
	for _, project := range c.Projects {
		root := filepath.Clean(project)
		if target == root {
			return true
		}
		if strings.HasPrefix(target, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (c *Credentials) Seen(turnID string) bool {
	_, ok := c.RecentTurns[turnID]
	return ok
}

// MarkSynced records the given turn ids, advances the watermark to the
// newest of them (never backwards, so a late-arriving turn can't rewind
// progress), and prunes ids older than the dedup window.
func (c *Credentials) MarkSynced(ids map[string]time.Time) {
	if c.RecentTurns == nil {
		c.RecentTurns = map[string]time.Time{}
	}
	for id, ts := range ids {
		c.RecentTurns[id] = ts
		if ts.After(c.Watermark) {
			c.Watermark = ts
		}
	}

	cutoff := c.Watermark.Add(-dedupWindow)
	for id, ts := range c.RecentTurns {
		if ts.Before(cutoff) {
			delete(c.RecentTurns, id)
		}
	}
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
