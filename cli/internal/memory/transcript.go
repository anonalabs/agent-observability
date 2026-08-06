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

// turnsFromFile pairs each user prompt with the next assistant message that
// actually contains text. Tool-only assistant messages in between contribute
// their tool names to the turn rather than ending it.
func (s ClaudeCodeSource) turnsFromFile(path string, since time.Time) ([]Turn, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var turns []Turn
	var pending *Turn
	var tools []string
	sawValidLine := false

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
		sawValidLine = true
		// Sidechains are subagent traffic, not the user's own conversation.
		if line.IsSidechain {
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

		if line.Type == "user" {
			var promptParts []string
			for _, b := range msg.blocks() {
				if b.Type == "text" && b.Text != "" {
					promptParts = append(promptParts, b.Text)
				}
			}
			if len(promptParts) == 0 {
				// Tool results come back as user-role lines; they aren't prompts.
				continue
			}
			pending = &Turn{
				Agent:     "claude-code",
				SessionID: line.SessionID,
				Prompt:    strings.Join(promptParts, "\n"),
				CWD:       line.CWD,
				GitBranch: line.GitBranch,
			}
			tools = nil
			continue
		}

		var responseParts []string
		for _, b := range msg.blocks() {
			switch b.Type {
			case "text":
				if b.Text != "" {
					responseParts = append(responseParts, b.Text)
				}
			case "tool_use":
				if b.Name != "" {
					tools = append(tools, b.Name)
				}
			}
		}
		if len(responseParts) == 0 || pending == nil {
			continue
		}

		turn := *pending
		turn.TurnID = line.UUID
		turn.Timestamp = timestamp
		turn.Response = strings.Join(responseParts, "\n")
		turn.Model = msg.Model
		turn.InputTokens = msg.Usage.InputTokens
		turn.OutputTokens = msg.Usage.OutputTokens
		turn.Tools = tools
		pending = nil
		tools = nil

		if !since.IsZero() && !turn.Timestamp.After(since) {
			continue
		}
		turns = append(turns, turn)
	}

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
