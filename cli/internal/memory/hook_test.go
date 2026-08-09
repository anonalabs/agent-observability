package memory

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// lineRe matches one well-formed fixture line from
// TestHookLogIsTruncatedWhenOversized -- "line 00042", never a fragment of
// one such as "2" or "e 00042".
var lineRe = regexp.MustCompile(`^line \d{5}$`)

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

	// Build a realistic, line-oriented fixture: many newline-terminated,
	// individually distinguishable lines, totalling more than
	// maxHookLogBytes. A block of zero bytes (with no newline anywhere)
	// can never exercise truncateLogFront's newline-boundary skip, since
	// indexByte would return -1 on it regardless of whether that branch
	// works.
	const lineWidth = 11 // "line %05d\n"
	numLines := (maxHookLogBytes+1024)/lineWidth + 100
	if numLines%2 == 0 {
		// With fixed-width lines starting at byte 0, an even line count
		// makes len(data)/2 land exactly on a line boundary regardless of
		// whether the newline-skip branch runs -- that would make this
		// test pass even with the branch broken. An odd line count (with
		// an odd lineWidth) guarantees the halfway point falls inside a
		// line instead, so the skip is actually required.
		numLines++
	}

	var buf bytes.Buffer
	buf.Grow(numLines * lineWidth)
	for i := 0; i < numLines; i++ {
		fmt.Fprintf(&buf, "line %05d\n", i)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
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

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "after truncation") {
		t.Error("the newest entry (written by LogHook after truncation) must survive")
	}

	newest := fmt.Sprintf("line %05d", numLines-1)
	if !strings.Contains(content, newest) {
		t.Errorf("newest pre-existing line %q did not survive truncation", newest)
	}

	if strings.Contains(content, "line 00000") {
		t.Error("oldest line should have been dropped by truncation")
	}

	// The retained content must start at a line boundary: the first line
	// in the file has to be a complete, well-formed fixture line, not a
	// fragment left over from slicing mid-line. This is the assertion
	// that actually exercises truncateLogFront's newline skip.
	firstLine := content
	if i := strings.IndexByte(content, '\n'); i >= 0 {
		firstLine = content[:i]
	}
	if !lineRe.MatchString(firstLine) {
		t.Errorf("first line after truncation = %q, want a complete line matching %s (newline-boundary skip did not run)", firstLine, lineRe)
	}
}
