package memory

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/anonalabs/agent-observability/cli/internal/cursorhook"
)

// emailPattern finds local@host candidates inside free text, including
// single-label hosts (e.g. a git user.email of "user@my-laptop") and bare
// IPs, not just multi-label domains with a letter TLD. cursorhook.MaskEmail
// expects a bare address, so prose needs this to locate candidates first;
// maybeMaskEmail then filters out version pins and module paths (e.g.
// "express@4.18.2", "cobra@v1.10.2") that share the local@host shape but
// aren't addresses.
var emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+`)

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
	return emailPattern.ReplaceAllStringFunc(masked, maybeMaskEmail)
}

// maybeMaskEmail masks a matched local@host candidate unless the host looks
// like a version pin or module path (a leading digit, or a leading "v"
// immediately followed by a digit) rather than a real address -- e.g.
// "express@4.18.2", "node@22", or the "v1.10.2" in
// "github.com/spf13/cobra@v1.10.2". A host that is purely digits and dots in
// IPv4 shape (e.g. "10.0.0.5") is exempted from that check, since it's an
// address despite the leading digit.
func maybeMaskEmail(match string) string {
	at := strings.IndexByte(match, '@')
	if at == -1 {
		return match
	}
	host := match[at+1:]
	if isIPv4Host(host) {
		return cursorhook.MaskEmail(match)
	}
	if looksLikeVersion(host) {
		return match
	}
	return cursorhook.MaskEmail(match)
}

// looksLikeVersion reports whether host has the shape of a version pin
// rather than a hostname: a leading digit (e.g. "4.18.2", "22"), or a
// leading "v" immediately followed by a digit (e.g. "v1.10.2").
func looksLikeVersion(host string) bool {
	if host == "" {
		return false
	}
	if host[0] >= '0' && host[0] <= '9' {
		return true
	}
	return len(host) > 1 && host[0] == 'v' && host[1] >= '0' && host[1] <= '9'
}

// isIPv4Host reports whether host is four dot-separated 1-3 digit octets,
// e.g. "10.0.0.5". This is checked ahead of looksLikeVersion so a bare IP
// address (which also starts with a digit) is still treated as an address.
func isIPv4Host(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 3 {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
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
