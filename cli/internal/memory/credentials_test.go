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

	err := SaveCredentials(&Credentials{APIKey: "anona_live_x"})
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

	err := SaveCredentials(&Credentials{APIKey: "anona_live_x"})
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
		name string
		root string
		path string
		want bool
	}{
		{"exact match", "/home/dev/repo", "/home/dev/repo", true},
		{"nested under project", "/home/dev/repo", "/home/dev/repo/cli/internal", true},
		{"outside project", "/home/dev/repo", "/home/dev/other", false},
		{"sibling with shared prefix", "/home/dev/repo", "/home/dev/repo-two", false},
		{"empty root denies everything", "", "/home/dev/repo", false},
		{"empty path denied", "/home/dev/repo", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Project{Path: tt.root}
			if got := p.AllowsPath(tt.path); got != tt.want {
				t.Errorf("AllowsPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestMarkSyncedAdvancesWatermarkAndPrunes(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-48 * time.Hour)
	recent := now.Add(-1 * time.Hour)

	p := &Project{
		Watermark:   stale,
		RecentTurns: map[string]time.Time{"old-turn": stale},
	}
	p.MarkSynced(map[string]time.Time{"new-turn": recent, "newest-turn": now})

	if !p.Watermark.Equal(now) {
		t.Errorf("watermark = %v, want %v", p.Watermark, now)
	}
	if p.Seen("old-turn") {
		t.Error("old-turn is older than the 24h window and should have been pruned")
	}
	if !p.Seen("new-turn") || !p.Seen("newest-turn") {
		t.Error("newly synced turns should be seen")
	}
}

func TestMarkSyncedNeverMovesWatermarkBackwards(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	p := &Project{Watermark: now}

	p.MarkSynced(map[string]time.Time{"late-arriving": now.Add(-2 * time.Hour)})

	if !p.Watermark.Equal(now) {
		t.Errorf("watermark = %v, want it unchanged at %v", p.Watermark, now)
	}
}

func TestLoadCredentialsMigratesV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	t.Setenv("AGENTOBS_MEMORY_CONFIG", path)

	v1 := `{
	  "api_key": "anona_live_x",
	  "space_id": "old-space",
	  "space_name": "old-space",
	  "projects": ["/home/dev/repo", "/home/dev/other"],
	  "watermark": "2026-08-06T12:00:00Z",
	  "recent_turns": {"turn-1": "2026-08-06T11:00:00Z"}
	}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := LoadCredentials()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Version != configVersion {
		t.Errorf("version = %d, want %d", got.Version, configVersion)
	}
	if got.APIKey != "anona_live_x" {
		t.Errorf("api_key = %q", got.APIKey)
	}
	if len(got.Projects) != 2 {
		t.Fatalf("got %d projects, want 2: %+v", len(got.Projects), got.Projects)
	}

	want := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	for _, p := range got.Projects {
		if p.SpaceID != "old-space" {
			t.Errorf("%s space_id = %q, want old-space", p.Path, p.SpaceID)
		}
		if !p.Watermark.Equal(want) {
			t.Errorf("%s watermark = %v, want %v", p.Path, p.Watermark, want)
		}
		if !p.Seen("turn-1") {
			t.Errorf("%s should carry the v1 dedup set", p.Path)
		}
	}
}

func TestLoadCredentialsBacksUpBeforeMigrating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	t.Setenv("AGENTOBS_MEMORY_CONFIG", path)

	v1 := `{"api_key":"k","space_id":"s","projects":["/a"],"watermark":"2026-08-06T12:00:00Z"}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := LoadCredentials(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("expected a backup before migrating a file holding an API key: %v", err)
	}
	if string(backup) != v1 {
		t.Errorf("backup = %q, want the original v1 content", string(backup))
	}
}

func TestLoadCredentialsLeavesV2Alone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	t.Setenv("AGENTOBS_MEMORY_CONFIG", path)

	when := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	if err := SaveCredentials(&Credentials{
		Version: configVersion,
		APIKey:  "k",
		Projects: []Project{
			{Path: "/home/dev/repo", SpaceID: "repo-space", Watermark: when},
		},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := LoadCredentials()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Projects) != 1 || got.Projects[0].SpaceID != "repo-space" {
		t.Fatalf("projects = %+v", got.Projects)
	}
	if !got.Projects[0].Watermark.Equal(when) {
		t.Errorf("watermark = %v, want %v", got.Projects[0].Watermark, when)
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("a v2 config must not be backed up -- nothing was migrated")
	}
}

func TestProjectForLongestPrefixWins(t *testing.T) {
	c := &Credentials{Projects: []Project{
		{Path: "/home/dev/repo", SpaceID: "outer"},
		{Path: "/home/dev/repo/.worktrees/feature", SpaceID: "inner"},
	}}

	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{"exact outer", "/home/dev/repo", "outer"},
		{"nested under outer", "/home/dev/repo/cli", "outer"},
		{"exact inner", "/home/dev/repo/.worktrees/feature", "inner"},
		{"nested under inner picks the longer prefix", "/home/dev/repo/.worktrees/feature/cli", "inner"},
		{"unrelated path", "/home/dev/elsewhere", ""},
		{"sibling sharing a prefix", "/home/dev/repo-two", ""},
		{"empty cwd", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := c.ProjectFor(tt.cwd)
			got := ""
			if p != nil {
				got = p.SpaceID
			}
			if got != tt.want {
				t.Errorf("ProjectFor(%q) = %q, want %q", tt.cwd, got, tt.want)
			}
		})
	}
}

func TestProjectWatermarksAreIndependent(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	c := &Credentials{Projects: []Project{
		{Path: "/a", SpaceID: "a"},
		{Path: "/b", SpaceID: "b"},
	}}

	c.Projects[0].MarkSynced(map[string]time.Time{"t1": now})

	if !c.Projects[0].Watermark.Equal(now) {
		t.Errorf("project a watermark = %v, want %v", c.Projects[0].Watermark, now)
	}
	if !c.Projects[1].Watermark.IsZero() {
		t.Errorf("project b watermark = %v, want it untouched", c.Projects[1].Watermark)
	}
	if c.Projects[1].Seen("t1") {
		t.Error("project b must not see project a's synced turns")
	}
}
