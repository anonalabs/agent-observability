package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveCredentialsUsesOwnerOnlyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "memory.json")
	t.Setenv("AGENTOBS_MEMORY_CONFIG", path)

	err := SaveCredentials(&Credentials{APIKey: "anona_live_x", SpaceID: "spc_a1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 600", got)
	}

	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("dir mode = %o, want 700", got)
	}
}

func TestSaveCredentialsTightensExistingLoosePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	path := filepath.Join(dir, "memory.json")
	t.Setenv("AGENTOBS_MEMORY_CONFIG", path)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err := SaveCredentials(&Credentials{APIKey: "anona_live_x", SpaceID: "spc_a1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 600", got)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("dir mode = %o, want 700", got)
	}
}

func TestLoadCredentialsRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	t.Setenv("AGENTOBS_MEMORY_CONFIG", path)

	when := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	want := &Credentials{
		APIKey:      "anona_live_x",
		SpaceID:     "spc_a1",
		SpaceName:   "work",
		Projects:    []string{"/home/dev/repo"},
		Watermark:   when,
		RecentTurns: map[string]time.Time{"turn-1": when},
	}
	if err := SaveCredentials(want); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := LoadCredentials()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected credentials, got nil")
	}
	if got.APIKey != want.APIKey || got.SpaceID != want.SpaceID || got.SpaceName != want.SpaceName {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if len(got.Projects) != 1 || got.Projects[0] != "/home/dev/repo" {
		t.Errorf("projects = %v", got.Projects)
	}
	if !got.Watermark.Equal(when) {
		t.Errorf("watermark = %v, want %v", got.Watermark, when)
	}
	if !got.Seen("turn-1") {
		t.Error("turn-1 should be seen")
	}
}

func TestLoadCredentialsMissingFileReturnsNil(t *testing.T) {
	t.Setenv("AGENTOBS_MEMORY_CONFIG", filepath.Join(t.TempDir(), "absent.json"))

	got, err := LoadCredentials()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestAllowsPath(t *testing.T) {
	tests := []struct {
		name     string
		projects []string
		path     string
		want     bool
	}{
		{"exact match", []string{"/home/dev/repo"}, "/home/dev/repo", true},
		{"nested under project", []string{"/home/dev/repo"}, "/home/dev/repo/cli/internal", true},
		{"outside project", []string{"/home/dev/repo"}, "/home/dev/other", false},
		{"sibling with shared prefix", []string{"/home/dev/repo"}, "/home/dev/repo-two", false},
		{"empty allowlist denies everything", nil, "/home/dev/repo", false},
		{"empty path denied", []string{"/home/dev/repo"}, "", false},
		{"second project matches", []string{"/a", "/home/dev/repo"}, "/home/dev/repo/x", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Credentials{Projects: tt.projects}
			if got := c.AllowsPath(tt.path); got != tt.want {
				t.Errorf("AllowsPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestMarkSyncedAdvancesWatermarkAndPrunes(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-48 * time.Hour)
	recent := now.Add(-1 * time.Hour)

	c := &Credentials{
		Watermark:   stale,
		RecentTurns: map[string]time.Time{"old-turn": stale},
	}
	c.MarkSynced(map[string]time.Time{"new-turn": recent, "newest-turn": now})

	if !c.Watermark.Equal(now) {
		t.Errorf("watermark = %v, want %v", c.Watermark, now)
	}
	if c.Seen("old-turn") {
		t.Error("old-turn is older than the 24h window and should have been pruned")
	}
	if !c.Seen("new-turn") || !c.Seen("newest-turn") {
		t.Error("newly synced turns should be seen")
	}
}

func TestMarkSyncedNeverMovesWatermarkBackwards(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	c := &Credentials{Watermark: now}

	c.MarkSynced(map[string]time.Time{"late-arriving": now.Add(-2 * time.Hour)})

	if !c.Watermark.Equal(now) {
		t.Errorf("watermark = %v, want it unchanged at %v", c.Watermark, now)
	}
}
