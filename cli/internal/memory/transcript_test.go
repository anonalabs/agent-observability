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

// TestClaudeCodeSourceAccumulatesTextAcrossToolRounds is finding blocker
// 4's core regression test: closing a turn at the first text block captures
// only the assistant's preamble and silently drops the real answer that
// follows a round of tool use. text -> tool_use -> text -> tool_use -> text
// must all belong to the same turn.
func TestClaudeCodeSourceAccumulatesTextAcrossToolRounds(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "sess-6", `
{"type":"user","uuid":"u1","timestamp":"2026-08-06T12:00:00Z","sessionId":"sess-6","cwd":"/home/dev/repo","message":{"role":"user","content":"investigate and fix"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-08-06T12:00:01Z","sessionId":"sess-6","cwd":"/home/dev/repo","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"Let me look into this."}],"usage":{"input_tokens":10,"output_tokens":5}}}
{"type":"assistant","uuid":"a2","timestamp":"2026-08-06T12:00:02Z","sessionId":"sess-6","cwd":"/home/dev/repo","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","name":"Read"}],"usage":{"input_tokens":20,"output_tokens":8}}}
{"type":"user","uuid":"u2","timestamp":"2026-08-06T12:00:03Z","sessionId":"sess-6","cwd":"/home/dev/repo","message":{"role":"user","content":[{"type":"tool_result","text":"file contents"}]}}
{"type":"assistant","uuid":"a3","timestamp":"2026-08-06T12:00:04Z","sessionId":"sess-6","cwd":"/home/dev/repo","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"Found the issue, fixing it now."}],"usage":{"input_tokens":30,"output_tokens":12}}}
{"type":"assistant","uuid":"a4","timestamp":"2026-08-06T12:00:05Z","sessionId":"sess-6","cwd":"/home/dev/repo","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","name":"Edit"}],"usage":{"input_tokens":40,"output_tokens":15}}}
{"type":"user","uuid":"u3","timestamp":"2026-08-06T12:00:06Z","sessionId":"sess-6","cwd":"/home/dev/repo","message":{"role":"user","content":[{"type":"tool_result","text":"edit applied"}]}}
{"type":"assistant","uuid":"a5","timestamp":"2026-08-06T12:00:07Z","sessionId":"sess-6","cwd":"/home/dev/repo","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"Done, the fix is in place."}],"usage":{"input_tokens":50,"output_tokens":20}}}
`)

	turns, skipped, err := ClaudeCodeSource{Root: root}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1 -- text/tool rounds around one prompt must merge into one turn: %+v", len(turns), turns)
	}

	got := turns[0]
	wantResponse := "Let me look into this.\nFound the issue, fixing it now.\nDone, the fix is in place."
	if got.Response != wantResponse {
		t.Errorf("response = %q, want %q", got.Response, wantResponse)
	}
	if len(got.Tools) != 2 || got.Tools[0] != "Read" || got.Tools[1] != "Edit" {
		t.Errorf("tools = %v, want [Read Edit]", got.Tools)
	}
	// TurnID and Timestamp both come from the last text-bearing assistant
	// message (a5), deliberately: they trace back to the same transcript
	// line rather than two different ones.
	if got.TurnID != "a5" {
		t.Errorf("turn_id = %q, want a5 (the last text-bearing assistant message)", got.TurnID)
	}
	if !got.Timestamp.Equal(time.Date(2026, 8, 6, 12, 0, 7, 0, time.UTC)) {
		t.Errorf("timestamp = %v, want a5's timestamp", got.Timestamp)
	}
	if got.Model != "claude-opus-5" {
		t.Errorf("model = %q", got.Model)
	}
	// Tokens sum across every assistant message in the turn (a1, a2, a3,
	// a4, a5): input 10+20+30+40+50=150, output 5+8+12+15+20=60.
	if got.InputTokens != 150 {
		t.Errorf("input_tokens = %d, want 150 (summed across all 5 assistant messages)", got.InputTokens)
	}
	if got.OutputTokens != 60 {
		t.Errorf("output_tokens = %d, want 60 (summed across all 5 assistant messages)", got.OutputTokens)
	}
}

// TestClaudeCodeSourceEmitsFinalTurnWithNoTrailingPrompt covers the
// end-of-file case explicitly: a turn is closed by either the next real
// prompt or EOF, and the second (last) exchange here has no prompt after
// it, so it must still be emitted rather than silently dropped.
func TestClaudeCodeSourceEmitsFinalTurnWithNoTrailingPrompt(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "sess-7", `
{"type":"user","uuid":"u1","timestamp":"2026-08-06T12:00:00Z","sessionId":"sess-7","cwd":"/home/dev/repo","message":{"role":"user","content":"first"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-08-06T12:00:01Z","sessionId":"sess-7","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"first reply"}]}}
{"type":"user","uuid":"u2","timestamp":"2026-08-06T12:00:02Z","sessionId":"sess-7","cwd":"/home/dev/repo","message":{"role":"user","content":"second, and the file ends mid-tool-round"}}
{"type":"assistant","uuid":"a2","timestamp":"2026-08-06T12:00:03Z","sessionId":"sess-7","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash"}]}}
{"type":"assistant","uuid":"a3","timestamp":"2026-08-06T12:00:04Z","sessionId":"sess-7","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"second reply"}]}}
`)

	turns, _, err := ClaudeCodeSource{Root: root}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2 (including the last, with no trailing prompt): %+v", len(turns), turns)
	}
	if turns[1].Prompt != "second, and the file ends mid-tool-round" || turns[1].Response != "second reply" {
		t.Errorf("second turn = %+v, want the final exchange with its tool and text", turns[1])
	}
	if len(turns[1].Tools) != 1 || turns[1].Tools[0] != "Bash" {
		t.Errorf("tools = %v, want [Bash]", turns[1].Tools)
	}
}

// TestClaudeCodeSourceNoCrossTalkBetweenConsecutivePrompts guards against a
// regression where accumulation could leak text or tool names across a
// prompt boundary: two prompts each answered before the next arrives must
// produce two turns, neither containing the other's content.
func TestClaudeCodeSourceNoCrossTalkBetweenConsecutivePrompts(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "sess-8", `
{"type":"user","uuid":"u1","timestamp":"2026-08-06T12:00:00Z","sessionId":"sess-8","cwd":"/home/dev/repo","message":{"role":"user","content":"first question"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-08-06T12:00:01Z","sessionId":"sess-8","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"tool_use","name":"Grep"}]}}
{"type":"assistant","uuid":"a2","timestamp":"2026-08-06T12:00:02Z","sessionId":"sess-8","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"first answer"}]}}
{"type":"user","uuid":"u2","timestamp":"2026-08-06T12:00:03Z","sessionId":"sess-8","cwd":"/home/dev/repo","message":{"role":"user","content":"second question"}}
{"type":"assistant","uuid":"a3","timestamp":"2026-08-06T12:00:04Z","sessionId":"sess-8","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"tool_use","name":"Write"}]}}
{"type":"assistant","uuid":"a4","timestamp":"2026-08-06T12:00:05Z","sessionId":"sess-8","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"second answer"}]}}
`)

	turns, _, err := ClaudeCodeSource{Root: root}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2", len(turns))
	}
	first, second := turns[0], turns[1]
	if first.Response != "first answer" {
		t.Errorf("first response = %q, want it isolated from the second exchange", first.Response)
	}
	if second.Response != "second answer" {
		t.Errorf("second response = %q, want it isolated from the first exchange", second.Response)
	}
	if len(first.Tools) != 1 || first.Tools[0] != "Grep" {
		t.Errorf("first tools = %v, want [Grep] only", first.Tools)
	}
	if len(second.Tools) != 1 || second.Tools[0] != "Write" {
		t.Errorf("second tools = %v, want [Write] only -- must not include Grep from the first turn", second.Tools)
	}
}

// TestClaudeCodeSourceTurnIDIsDeterministic pins that TurnID is stable
// across repeated parses of the same file, since it's the dedup key that
// every later sync run relies on to avoid re-pushing the same turn.
func TestClaudeCodeSourceTurnIDIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "sess-9", `
{"type":"user","uuid":"u1","timestamp":"2026-08-06T12:00:00Z","sessionId":"sess-9","cwd":"/home/dev/repo","message":{"role":"user","content":"do it"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-08-06T12:00:01Z","sessionId":"sess-9","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"working on it"}]}}
{"type":"assistant","uuid":"a2","timestamp":"2026-08-06T12:00:02Z","sessionId":"sess-9","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read"}]}}
{"type":"assistant","uuid":"a3","timestamp":"2026-08-06T12:00:03Z","sessionId":"sess-9","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}
`)

	first, _, err := ClaudeCodeSource{Root: root}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, _, err := ClaudeCodeSource{Root: root}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("got %d / %d turns, want 1 / 1", len(first), len(second))
	}
	if first[0].TurnID != second[0].TurnID {
		t.Errorf("turn_id = %q then %q, want it identical across re-parses of the same file", first[0].TurnID, second[0].TurnID)
	}
	if first[0].TurnID != "a3" {
		t.Errorf("turn_id = %q, want a3 (the last text-bearing message)", first[0].TurnID)
	}
}

func TestClaudeCodeSourceSkipsSidechains(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "sess-3", `
{"type":"user","uuid":"u1","isSidechain":true,"timestamp":"2026-08-06T12:00:00Z","sessionId":"sess-3","cwd":"/home/dev/repo","message":{"role":"user","content":"subagent prompt"}}
{"type":"assistant","uuid":"a1","isSidechain":true,"timestamp":"2026-08-06T12:00:01Z","sessionId":"sess-3","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"subagent reply"}]}}
`)

	turns, skipped, err := ClaudeCodeSource{Root: root}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turns) != 0 {
		t.Fatalf("got %d turns, want 0 -- sidechains are subagent traffic", len(turns))
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0 -- a sidechain-only file is structurally fine, not corrupt", skipped)
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

func TestClaudeCodeSourceSinceFilteredFileIsNotSkipped(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "sess-5", `
{"type":"user","uuid":"u1","timestamp":"2026-08-06T10:00:00Z","sessionId":"sess-5","cwd":"/home/dev/repo","message":{"role":"user","content":"old"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-08-06T10:00:01Z","sessionId":"sess-5","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"old reply"}]}}
`)

	since := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	turns, skipped, err := ClaudeCodeSource{Root: root}.Turns(since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("got %d turns, want 0 -- the only turn is before since", len(turns))
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0 -- the file is structurally fine, just filtered by since", skipped)
	}
}

func TestClaudeCodeSourceCountsUnparseableTimestampsAsSkipped(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "bad-timestamps", `
{"type":"assistant","uuid":"a1","timestamp":"not-a-timestamp","sessionId":"s","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}
{"type":"assistant","uuid":"a2","timestamp":"also-not-a-timestamp","sessionId":"s","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"there"}]}}
`)

	turns, skipped, err := ClaudeCodeSource{Root: root}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("got %d turns, want 0", len(turns))
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 -- every line failed to parse a usable timestamp", skipped)
	}
}

func TestClaudeCodeSourceCountsUnreadableFileAsSkipped(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root bypasses file permission bits")
	}

	root := t.TempDir()
	writeTranscript(t, root, "-home-dev-repo", "good", `
{"type":"user","uuid":"u1","timestamp":"2026-08-06T12:00:00Z","sessionId":"s","cwd":"/home/dev/repo","message":{"role":"user","content":"hi"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-08-06T12:00:01Z","sessionId":"s","cwd":"/home/dev/repo","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}
`)
	writeTranscript(t, root, "-home-dev-repo", "unreadable", `
{"type":"user","uuid":"u2","timestamp":"2026-08-06T12:00:00Z","sessionId":"s2","cwd":"/home/dev/repo","message":{"role":"user","content":"hi"}}
`)
	unreadablePath := filepath.Join(root, "-home-dev-repo", "unreadable.jsonl")
	if err := os.Chmod(unreadablePath, 0o000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() { os.Chmod(unreadablePath, 0o644) })

	turns, skipped, err := ClaudeCodeSource{Root: root}.Turns(time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turns) != 1 {
		t.Errorf("got %d turns, want the readable file's turn", len(turns))
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 for the unreadable file", skipped)
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
