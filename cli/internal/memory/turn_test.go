package memory

import (
	"testing"
	"time"
)

func TestMaskText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			"home path",
			"see /home/srujan/Documents/notes.md",
			"see /home/[USER]/Documents/notes.md",
		},
		{
			"macos path",
			"open /Users/alice/dev",
			"open /Users/[USER]/dev",
		},
		{
			"email in prose",
			"ping alice@example.com about it",
			"ping a***@example.com about it",
		},
		{
			"two emails",
			"alice@example.com and bob@corp.io",
			"a***@example.com and b***@corp.io",
		},
		{
			"nothing sensitive",
			"refactor the parser",
			"refactor the parser",
		},
		{
			"single-label git email host",
			"contact srujan@my-laptop for the fix",
			"contact s***@my-laptop for the fix",
		},
		{
			"single-label git email host, trailing comma",
			"my git email is srujan@buildbox, ...",
			"my git email is s***@buildbox, ...",
		},
		{
			"bare IP host",
			"reach the admin at root@10.0.0.5",
			"reach the admin at r***@10.0.0.5",
		},
		{
			"npm version pin untouched",
			"express@4.18.2",
			"express@4.18.2",
		},
		{
			"go module path with version pin untouched",
			"github.com/spf13/cobra@v1.10.2",
			"github.com/spf13/cobra@v1.10.2",
		},
		{
			"bare version pin untouched",
			"node@22",
			"node@22",
		},
		{
			"scoped package name, no local part before @",
			"@types/node",
			"@types/node",
		},
		{
			"bare IP host, trailing sentence period",
			"reach the admin at root@10.0.0.5.",
			"reach the admin at r***@10.0.0.5.",
		},
		{
			"email in prose, trailing sentence period",
			"contact alice@example.com.",
			"contact a***@example.com.",
		},
		{
			"single-label host, wrapped in parens",
			"(srujan@buildbox)",
			"(s***@buildbox)",
		},
		{
			"single-label host, wrapped in angle brackets",
			"<srujan@buildbox>",
			"<s***@buildbox>",
		},
		{
			"github actions branch pin untouched",
			"actions/checkout@main",
			"actions/checkout@main",
		},
		{
			"npm dist-tag untouched",
			"lodash@latest",
			"lodash@latest",
		},
		{
			"go module ref untouched",
			"go get github.com/foo/bar@main",
			"go get github.com/foo/bar@main",
		},
		{
			"npm dist-tag untouched, stable",
			"pkg@stable",
			"pkg@stable",
		},
		{
			"git ref untouched, uppercase HEAD",
			"user@HEAD",
			"user@HEAD",
		},
		{
			"single-label hostname with digit still masks",
			"srujan@box2",
			"s***@box2",
		},
		{
			"single-label hostname, uppercase, still masks",
			"srujan@MY-LAPTOP",
			"s***@MY-LAPTOP",
		},
		{
			"single-label hostname containing a ref word as prefix still masks",
			"sam@dev-box",
			"s***@dev-box",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MaskText(tt.in); got != tt.want {
				t.Errorf("MaskText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMaskedAppliesToPromptResponseCWDAndGitBranch(t *testing.T) {
	turn := Turn{
		Prompt:    "fix /home/srujan/app.go",
		Response:  "done, mailed alice@example.com",
		CWD:       "/home/srujan/app",
		GitBranch: "wip-/home/bob/personal-branch",
	}

	got := turn.Masked()

	if got.Prompt != "fix /home/[USER]/app.go" {
		t.Errorf("prompt = %q", got.Prompt)
	}
	if got.Response != "done, mailed a***@example.com" {
		t.Errorf("response = %q", got.Response)
	}
	if got.CWD != "/home/[USER]/app" {
		t.Errorf("cwd = %q", got.CWD)
	}
	if got.GitBranch != "wip-/home/[USER]/personal-branch" {
		t.Errorf("git branch = %q", got.GitBranch)
	}
}

func TestRecordItemWithResponse(t *testing.T) {
	when := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	turn := Turn{
		Agent:        "claude-code",
		SessionID:    "sess-1",
		TurnID:       "turn-1",
		Timestamp:    when,
		Prompt:       "add a test",
		Response:     "added",
		Model:        "claude-opus-5",
		CWD:          "/home/dev/repo",
		GitBranch:    "main",
		InputTokens:  120,
		OutputTokens: 45,
		CostUSD:      0.02,
		Tools:        []string{"Read", "Edit"},
	}

	item := turn.RecordItem()

	wantContent := "User: add a test\n\nAssistant: added"
	if item.Content != wantContent {
		t.Errorf("content = %q, want %q", item.Content, wantContent)
	}
	if item.Timestamp != "2026-08-06T12:00:00Z" {
		t.Errorf("timestamp = %q, want RFC3339 UTC", item.Timestamp)
	}
	if item.Context != "repo (main), claude-opus-5" {
		t.Errorf("context = %q", item.Context)
	}
	if item.Metadata["turn_id"] != "turn-1" {
		t.Errorf("turn_id = %v", item.Metadata["turn_id"])
	}
	if item.Metadata["session_id"] != "sess-1" {
		t.Errorf("session_id = %v", item.Metadata["session_id"])
	}
	if item.Metadata["agent_id"] != "claude-code" {
		t.Errorf("agent_id = %v", item.Metadata["agent_id"])
	}
	if item.Metadata["has_response"] != true {
		t.Errorf("has_response = %v, want true", item.Metadata["has_response"])
	}
	if item.Metadata["cost_usd"] != 0.02 {
		t.Errorf("cost_usd = %v", item.Metadata["cost_usd"])
	}
}

func TestRecordItemPromptOnly(t *testing.T) {
	turn := Turn{
		Agent:     "cursor",
		SessionID: "sess-2",
		TurnID:    "turn-2",
		Timestamp: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		Prompt:    "refactor this",
		CWD:       "/home/dev/repo",
	}

	item := turn.RecordItem()

	if item.Content != "User: refactor this" {
		t.Errorf("content = %q, want the prompt with no Assistant section", item.Content)
	}
	if item.Metadata["has_response"] != false {
		t.Errorf("has_response = %v, want false", item.Metadata["has_response"])
	}
}
