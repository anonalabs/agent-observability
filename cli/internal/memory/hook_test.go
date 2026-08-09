package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseHookPayload(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantCWD string
		wantErr bool
	}{
		{
			"well formed",
			`{"session_id":"s1","transcript_path":"/home/dev/.claude/projects/x/s1.jsonl","cwd":"/home/dev/repo"}`,
			"/home/dev/repo",
			false,
		},
		{
			"extra fields are ignored",
			`{"session_id":"s1","cwd":"/home/dev/repo","hook_event_name":"Stop","unknown":123}`,
			"/home/dev/repo",
			false,
		},
		{"malformed json", `{not json`, "", true},
		{"empty input", ``, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseHookPayload(strings.NewReader(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.CWD != tt.wantCWD {
				t.Errorf("cwd = %q, want %q", got.CWD, tt.wantCWD)
			}
		})
	}
}

func TestLogHookWritesToConfigDirAndCaps(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTOBS_MEMORY_CONFIG", filepath.Join(dir, "memory.json"))

	LogHook("first entry %d", 1)
	LogHook("second entry")

	path, err := HookLogPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(data), "first entry 1") || !strings.Contains(string(data), "second entry") {
		t.Errorf("log = %q, want both entries", string(data))
	}
}

func TestProjectLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTOBS_MEMORY_CONFIG", filepath.Join(dir, "memory.json"))

	p := &Project{Path: "/home/dev/repo", SpaceID: "repo"}

	first, err := ProjectLock(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first == nil {
		t.Fatal("expected to acquire the lock")
	}
	defer first.Unlock()

	second, err := ProjectLock(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if second != nil {
		t.Error("a second lock on the same project must not be granted")
		second.Unlock()
	}
}

func TestProjectLockDistinctPerProject(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTOBS_MEMORY_CONFIG", filepath.Join(dir, "memory.json"))

	a, err := ProjectLock(&Project{Path: "/home/dev/a", SpaceID: "a"})
	if err != nil || a == nil {
		t.Fatalf("expected lock on a: %v", err)
	}
	defer a.Unlock()

	b, err := ProjectLock(&Project{Path: "/home/dev/b", SpaceID: "b"})
	if err != nil || b == nil {
		t.Fatalf("a different project must be lockable independently: %v", err)
	}
	b.Unlock()
}

func TestHookLogIsTruncatedWhenOversized(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTOBS_MEMORY_CONFIG", filepath.Join(dir, "memory.json"))

	path, err := HookLogPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(path, make([]byte, maxHookLogBytes+1024), 0o600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	LogHook("after truncation %v", time.Now().Year())

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Size() > maxHookLogBytes {
		t.Errorf("log size = %d, want <= %d", info.Size(), maxHookLogBytes)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "after truncation") {
		t.Error("the newest entry must survive truncation")
	}
}
