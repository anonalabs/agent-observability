package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTranscript creates <root>/<project>/<name>.jsonl with the given lines.
func writeTranscript(t *testing.T, root, project, name, content string) {
	t.Helper()
	dir := filepath.Join(root, project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".jsonl"), []byte(content), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClaudeCodeSourcePairsPromptAndResponse(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "sess-1", `
{"type":"user","uuid":"u1","timestamp":"2026-08-06T12:00:00Z","sessionId":"sess-1","cwd":"/home/dev/repo","gitBranch":"main","message":{"role":"user","content":"add a test"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-08-06T12:00:05Z","sessionId":"sess-1","cwd":"/home/dev/repo","gitBranch":"main","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"added"}],"usage":{"input_tokens":120,"output_tokens":45}}}
`)

	turns, skipped, err := ClaudeCodeSource{Root: root}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1: %+v", len(turns), turns)
	}

	got := turns[0]
	if got.Agent != "claude-code" {
		t.Errorf("agent = %q", got.Agent)
	}
	if got.TurnID != "a1" {
		t.Errorf("turn_id = %q, want the assistant uuid a1", got.TurnID)
	}
	if got.Prompt != "add a test" || got.Response != "added" {
		t.Errorf("prompt/response = %q / %q", got.Prompt, got.Response)
	}
	if got.SessionID != "sess-1" || got.CWD != "/home/dev/repo" || got.GitBranch != "main" {
		t.Errorf("metadata fields = %+v", got)
	}
	if got.Model != "claude-opus-5" {
		t.Errorf("model = %q", got.Model)
	}
	if got.InputTokens != 120 || got.OutputTokens != 45 {
		t.Errorf("tokens = %d / %d", got.InputTokens, got.OutputTokens)
	}
	if !got.Timestamp.Equal(time.Date(2026, 8, 6, 12, 0, 5, 0, time.UTC)) {
		t.Errorf("timestamp = %v, want the assistant line's", got.Timestamp)
	}
}

func TestClaudeCodeSourceJoinsMultipleTextBlocksAndCollectsTools(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "sess-2", `
{"type":"user","uuid":"u1","timestamp":"2026-08-06T12:00:00Z","sessionId":"sess-2","cwd":"/home/dev/repo","message":{"role":"user","content":"read then edit"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-08-06T12:00:01Z","sessionId":"sess-2","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read"}]}}
{"type":"assistant","uuid":"a2","timestamp":"2026-08-06T12:00:02Z","sessionId":"sess-2","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit"}]}}
{"type":"assistant","uuid":"a3","timestamp":"2026-08-06T12:00:03Z","sessionId":"sess-2","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}}
`)

	turns, _, err := ClaudeCodeSource{Root: root}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}
	if turns[0].Response != "first\nsecond" {
		t.Errorf("response = %q, want the text blocks joined", turns[0].Response)
	}
	if len(turns[0].Tools) != 2 || turns[0].Tools[0] != "Read" || turns[0].Tools[1] != "Edit" {
		t.Errorf("tools = %v, want [Read Edit]", turns[0].Tools)
	}
}

func TestClaudeCodeSourceSkipsSidechains(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "sess-3", `
{"type":"user","uuid":"u1","isSidechain":true,"timestamp":"2026-08-06T12:00:00Z","sessionId":"sess-3","cwd":"/home/dev/repo","message":{"role":"user","content":"subagent prompt"}}
{"type":"assistant","uuid":"a1","isSidechain":true,"timestamp":"2026-08-06T12:00:01Z","sessionId":"sess-3","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"subagent reply"}]}}
`)

	turns, _, err := ClaudeCodeSource{Root: root}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turns) != 0 {
		t.Fatalf("got %d turns, want 0 -- sidechains are subagent traffic", len(turns))
	}
}

func TestClaudeCodeSourceHonoursSince(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "sess-4", `
{"type":"user","uuid":"u1","timestamp":"2026-08-06T10:00:00Z","sessionId":"sess-4","cwd":"/home/dev/repo","message":{"role":"user","content":"old"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-08-06T10:00:01Z","sessionId":"sess-4","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"old reply"}]}}
{"type":"user","uuid":"u2","timestamp":"2026-08-06T14:00:00Z","sessionId":"sess-4","cwd":"/home/dev/repo","message":{"role":"user","content":"new"}}
{"type":"assistant","uuid":"a2","timestamp":"2026-08-06T14:00:01Z","sessionId":"sess-4","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"new reply"}]}}
`)

	since := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	turns, _, err := ClaudeCodeSource{Root: root}.Turns(since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}
	if turns[0].Prompt != "new" {
		t.Errorf("prompt = %q, want the turn after since", turns[0].Prompt)
	}
}

func TestClaudeCodeSourceCountsMalformedFilesWithoutFailing(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "good", `
{"type":"user","uuid":"u1","timestamp":"2026-08-06T12:00:00Z","sessionId":"s","cwd":"/home/dev/repo","message":{"role":"user","content":"hi"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-08-06T12:00:01Z","sessionId":"s","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}
`)
	writeTranscript(t, root, "-home-dev-repo", "bad", "{not json at all\n")

	turns, skipped, err := ClaudeCodeSource{Root: root}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turns) != 1 {
		t.Errorf("got %d turns, want the one good file's turn", len(turns))
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
}

func TestClaudeCodeSourceMissingRootIsNotAnError(t *testing.T) {
	turns, skipped, err := ClaudeCodeSource{Root: filepath.Join(t.TempDir(), "absent")}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turns) != 0 || skipped != 0 {
		t.Errorf("turns = %d, skipped = %d, want 0/0", len(turns), skipped)
	}
}
