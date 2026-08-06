package memory

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/anonalabs/agent-observability/cli/internal/cursorhook"
)

// emailPattern finds addresses inside free text. cursorhook.MaskEmail
// expects a bare address, so prose needs this to locate them first.
var emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// Turn is one prompt/response exchange. Response is empty for agents whose
// telemetry carries prompts but no assistant text.
type Turn struct {
	Agent     string
	SessionID string
	TurnID    string
	Timestamp time.Time

	Prompt    string
	Response  string
	Model     string
	CWD       string
	GitBranch string

	InputTokens  int
	OutputTokens int
	CostUSD      float64
	Tools        []string
}

func (t Turn) HasResponse() bool { return t.Response != "" }

// MaskText redacts home-directory usernames and email addresses in free
// text, reusing cursorhook's path patterns so both paths behave the same.
func MaskText(s string) string {
	masked := cursorhook.MaskPath(s)
	return emailPattern.ReplaceAllStringFunc(masked, cursorhook.MaskEmail)
}

// Masked returns a copy with every user-supplied text field redacted.
func (t Turn) Masked() Turn {
	t.Prompt = MaskText(t.Prompt)
	t.Response = MaskText(t.Response)
	t.CWD = MaskText(t.CWD)
	return t
}

// RecordItem converts a turn into a /v1/record/batch item. The batch
// endpoint takes only content/context/timestamp/metadata, so session_id,
// agent_id, and tool names all travel inside metadata.
func (t Turn) RecordItem() RecordItem {
	content := "User: " + t.Prompt
	if t.HasResponse() {
		content += "\n\nAssistant: " + t.Response
	}

	var contextParts []string
	if t.CWD != "" {
		repo := filepath.Base(t.CWD)
		if t.GitBranch != "" {
			repo = fmt.Sprintf("%s (%s)", repo, t.GitBranch)
		}
		contextParts = append(contextParts, repo)
	}
	if t.Model != "" {
		contextParts = append(contextParts, t.Model)
	}

	metadata := map[string]interface{}{
		"turn_id":       t.TurnID,
		"session_id":    t.SessionID,
		"agent_id":      t.Agent,
		"has_response":  t.HasResponse(),
		"input_tokens":  t.InputTokens,
		"output_tokens": t.OutputTokens,
		"cost_usd":      t.CostUSD,
		"cwd":           t.CWD,
		"git_branch":    t.GitBranch,
	}
	if len(t.Tools) > 0 {
		metadata["tools"] = t.Tools
	}

	return RecordItem{
		Content:   content,
		Context:   strings.Join(contextParts, ", "),
		Timestamp: t.Timestamp.UTC().Format(time.RFC3339),
		Metadata:  metadata,
	}
}
