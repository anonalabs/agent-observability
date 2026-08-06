package memory

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TranscriptSource yields conversation turns for one agent. The second
// return value counts files skipped as unreadable or malformed -- reported
// to the user rather than aborting a sync over one bad file.
type TranscriptSource interface {
	Name() string
	Turns(since time.Time) ([]Turn, int, error)
}

// ClaudeCodeSource reads ~/.claude/projects/<slug>/<session-id>.jsonl.
// Each line is one JSON object; user and assistant lines interleave.
type ClaudeCodeSource struct {
	Root string
}

func NewClaudeCodeSource() (ClaudeCodeSource, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return ClaudeCodeSource{}, err
	}
	return ClaudeCodeSource{Root: filepath.Join(home, ".claude", "projects")}, nil
}

func (ClaudeCodeSource) Name() string { return "claude-code" }

// transcriptLine is the subset of each JSONL record this needs.
type transcriptLine struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	IsSidechain bool            `json:"isSidechain"`
	Timestamp   string          `json:"timestamp"`
	SessionID   string          `json:"sessionId"`
	CWD         string          `json:"cwd"`
	GitBranch   string          `json:"gitBranch"`
	Message     json.RawMessage `json:"message"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
}

type transcriptMessage struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// blocks normalizes content, which is a plain string on user lines and an
// array of typed blocks on assistant lines.
func (m transcriptMessage) blocks() []contentBlock {
	var asString string
	if err := json.Unmarshal(m.Content, &asString); err == nil {
		return []contentBlock{{Type: "text", Text: asString}}
	}
	var asBlocks []contentBlock
	if err := json.Unmarshal(m.Content, &asBlocks); err == nil {
		return asBlocks
	}
	return nil
}

func (s ClaudeCodeSource) Turns(since time.Time) ([]Turn, int, error) {
	pattern := filepath.Join(s.Root, "*", "*.jsonl")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, 0, err
	}

	var all []Turn
	skipped := 0

	for _, file := range files {
		turns, err := s.turnsFromFile(file, since)
		if err != nil {
			skipped++
			continue
		}
		all = append(all, turns...)
	}

	return all, skipped, nil
}

// turnsFromFile pairs each user prompt with every assistant message that
// follows it, up to the next real user prompt (or end of file). Agentic
// sessions routinely emit a short text preamble, run one or more tools, and
// then emit the substantive answer as separate assistant messages in
// between rounds of tool use -- closing the turn at the first text block
// (the earlier behavior) captured the preamble and silently discarded the
// real answer. Tool-result lines come back with role "user" but carry no
// prompt text, so they're recognized and skipped without closing the turn
// that's still accumulating around them.
func (s ClaudeCodeSource) turnsFromFile(path string, since time.Time) ([]Turn, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var turns []Turn

	// Turn-in-progress state. pending holds the fields known as soon as the
	// prompt line arrives (Prompt/CWD/GitBranch/SessionID); everything
	// assistant-message-derived accumulates alongside it until closeTurn.
	var pending *Turn
	var responseParts []string
	var tools []string
	// lastTurnID/lastTimestamp/lastModel come from the most recent
	// text-bearing assistant message in the turn, not just any assistant
	// message -- a tool_use-only message never carried a turn identity
	// even under the old close-at-first-text behavior, and multiple text
	// blocks in one turn should resolve to its final, most complete
	// answer's identity. TurnID is therefore the dedup key of the *last*
	// text-bearing assistant message, deliberately: it's sourced from the
	// same message as Timestamp (see below), so a record's turn_id and
	// timestamp always trace back to one line in the transcript rather
	// than two different ones, and re-parsing the same file always
	// produces the same id for the same accumulated turn.
	//
	// Consequence: an exchange still being written when a sync runs gets a
	// turn_id keyed to whatever its last text message is *at that moment*.
	// If the assistant later adds more text to the same exchange (the file
	// is still open, e.g. a long tool-using response spanning several
	// sync intervals), the next sync sees a *different* last text-bearing
	// message and therefore a new turn_id -- so that exchange is pushed
	// again, as a new record with a fuller Response, rather than updating
	// the earlier one in place. This is intentional, not a bug: a turn_id
	// fixed at the exchange's first message would dedup away every later,
	// more-complete version, and the finished answer would never be sent.
	// The tradeoff is near-duplicate partial records for anything still in
	// progress when a sync fires -- routine with hourly cron and a long
	// session, not an edge case. See docs/anonamemory.md.
	var lastTurnID string
	var lastTimestamp time.Time
	var lastModel string
	// inputTokens/outputTokens sum every assistant message's usage in the
	// turn, text-bearing or not -- each one is a real API call made while
	// producing this turn's overall response, so summing (rather than
	// keeping only the closing message's count) is the accurate cost.
	var inputTokens, outputTokens int

	sawValidLine := false

	// closeTurn finalizes whatever turn has been accumulating, if it ever
	// received actual response text, and appends it (subject to the since
	// filter) before resetting all per-turn state. Called both when the
	// next real user prompt arrives and once more after the scan loop ends,
	// since the last exchange in a file has no following prompt to trigger
	// on and must still be emitted rather than silently dropped.
	closeTurn := func() {
		if pending != nil && len(responseParts) > 0 {
			turn := *pending
			turn.TurnID = lastTurnID
			turn.Timestamp = lastTimestamp
			turn.Response = strings.Join(responseParts, "\n")
			turn.Model = lastModel
			turn.InputTokens = inputTokens
			turn.OutputTokens = outputTokens
			turn.Tools = tools

			if since.IsZero() || turn.Timestamp.After(since) {
				turns = append(turns, turn)
			}
		}
		pending = nil
		responseParts = nil
		tools = nil
		lastTurnID = ""
		lastTimestamp = time.Time{}
		lastModel = ""
		inputTokens = 0
		outputTokens = 0
	}

	scanner := bufio.NewScanner(f)
	// Transcript lines routinely exceed bufio's 64KB default.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}

		var line transcriptLine
		if err := json.Unmarshal([]byte(text), &line); err != nil {
			continue
		}
		if line.Type != "user" && line.Type != "assistant" {
			continue
		}

		var msg transcriptMessage
		if err := json.Unmarshal(line.Message, &msg); err != nil {
			continue
		}

		timestamp, err := time.Parse(time.RFC3339, line.Timestamp)
		if err != nil {
			continue
		}

		// The line fully parsed -- type recognized, message body unmarshalled,
		// timestamp valid -- so this file has at least one genuine candidate
		// turn line, even if it's a sidechain or later excluded by since.
		// Lines that fail earlier (bad JSON, unrecognized type, bad message,
		// bad timestamp) never reach here, so a file of nothing but such
		// lines is genuinely unusable and gets counted as skipped below.
		sawValidLine = true

		// Sidechains are subagent traffic, not the user's own conversation.
		if line.IsSidechain {
			continue
		}

		if line.Type == "user" {
			var promptParts []string
			for _, b := range msg.blocks() {
				if b.Type == "text" && b.Text != "" {
					promptParts = append(promptParts, b.Text)
				}
			}
			if len(promptParts) == 0 {
				// Tool results come back as user-role lines; they aren't
				// prompts, and must not close a turn that's still
				// accumulating assistant text around them.
				continue
			}

			// A real prompt closes whatever turn was accumulating before
			// starting the new one.
			closeTurn()
			pending = &Turn{
				Agent:     "claude-code",
				SessionID: line.SessionID,
				Prompt:    strings.Join(promptParts, "\n"),
				CWD:       line.CWD,
				GitBranch: line.GitBranch,
			}
			continue
		}

		// Assistant line. Nothing to accumulate into without an open turn
		// (e.g. a stray assistant line before any prompt in the file).
		if pending == nil {
			continue
		}

		var sawText bool
		for _, b := range msg.blocks() {
			switch b.Type {
			case "text":
				if b.Text != "" {
					responseParts = append(responseParts, b.Text)
					sawText = true
				}
			case "tool_use":
				if b.Name != "" {
					tools = append(tools, b.Name)
				}
			}
		}
		if sawText {
			lastTurnID = line.UUID
			lastTimestamp = timestamp
			lastModel = msg.Model
		}
		inputTokens += msg.Usage.InputTokens
		outputTokens += msg.Usage.OutputTokens
	}

	// The last exchange in the file has no following prompt to close it.
	closeTurn()

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !sawValidLine {
		return nil, errNoValidLines
	}
	return turns, nil
}

// errNoValidLines marks a file that parsed to nothing usable, so the caller
// counts it as skipped instead of silently reporting zero turns.
var errNoValidLines = errors.New("no valid transcript lines")
