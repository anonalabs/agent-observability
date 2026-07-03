package cursorhook

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// GenerateSessionTraceID derives a deterministic 128-bit trace ID from a
// Cursor conversation_id, so every span in one conversation shares a trace
// even before any cross-process context file exists (used for the first,
// root span of a session).
func GenerateSessionTraceID(conversationID string) [16]byte {
	sum := sha256.Sum256([]byte(conversationID))
	var id [16]byte
	copy(id[:], sum[:16])
	return id
}

type spanRef struct {
	TraceID   [16]byte `json:"trace_id"`
	SpanID    [8]byte  `json:"span_id"`
	HookEvent string   `json:"hook_event"`
	Timestamp float64  `json:"timestamp"`
}

type generationContext struct {
	CurrentSessionSpan  *spanRef  `json:"current_session_span,omitempty"`
	CurrentSubagentSpan *spanRef  `json:"current_subagent_span,omitempty"`
	CurrentToolSpan     *spanRef  `json:"current_tool_span,omitempty"`
	SessionTraceID      *[16]byte `json:"session_trace_id,omitempty"`
}

type conversationContext struct {
	SessionTraceID [16]byte `json:"session_trace_id"`
	Timestamp      float64  `json:"timestamp"`
}

// ContextManager persists span state across process invocations (each hook
// event is a fresh process, so this file is the only way child spans learn
// their parent's trace_id/span_id).
type ContextManager struct {
	StorageDir string
}

func NewContextManager() (*ContextManager, error) {
	dir := filepath.Join(os.TempDir(), "agentobs_cursor_context")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &ContextManager{StorageDir: dir}, nil
}

// safeFilenameComponent hashes an externally-supplied ID (generation_id /
// conversation_id come straight from Cursor's hook JSON on stdin) before
// it's used in a file path. filepath.Join does not stop ".." segments from
// escaping StorageDir, so using the raw ID directly would let a crafted
// generation_id like "../../../../etc/cron.d/x" write outside the intended
// temp directory. Hashing makes the result a fixed-format, traversal-proof
// filename regardless of what the ID contains.
func safeFilenameComponent(id string) string {
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("%x", sum)
}

func (c *ContextManager) contextFile(generationID string) string {
	return filepath.Join(c.StorageDir, safeFilenameComponent(generationID)+"_context.json")
}

func (c *ContextManager) conversationFile(conversationID string) string {
	return filepath.Join(c.StorageDir, "conversation_"+safeFilenameComponent(conversationID)+".json")
}

func readLocked(path string, out interface{}) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		return false, nil
	}
	lock := flock.New(path)
	if err := lock.RLock(); err != nil {
		return false, err
	}
	defer lock.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if len(data) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, err
	}
	return true, nil
}

func writeLocked(path string, v interface{}) error {
	lock := flock.New(path)
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ParentRef is the {trace_id, span_id} pair a new span should be a child of.
type ParentRef struct {
	TraceID [16]byte
	SpanID  [8]byte
}

// GetParentContext determines the parent span for hookEvent, per the span
// hierarchy: sessionStart is always root; tool-completion events parent to
// the matching tool-start span; subagent/tool-start events parent to the
// active subagent, else the session.
func (c *ContextManager) GetParentContext(generationID, hookEvent string) (*ParentRef, error) {
	if hookEvent == "sessionStart" {
		return nil, nil
	}

	var ctx generationContext
	found, err := readLocked(c.contextFile(generationID), &ctx)
	if err != nil || !found {
		return nil, err
	}

	toolCompletionEvents := map[string]bool{
		"postToolUse": true, "postToolUseFailure": true,
		"afterShellExecution": true, "afterMCPExecution": true, "afterFileEdit": true,
	}
	toolStartEvents := map[string]bool{
		"preToolUse": true, "beforeShellExecution": true,
		"beforeMCPExecution": true, "beforeReadFile": true,
	}

	if toolCompletionEvents[hookEvent] && ctx.CurrentToolSpan != nil {
		return &ParentRef{ctx.CurrentToolSpan.TraceID, ctx.CurrentToolSpan.SpanID}, nil
	}

	if hookEvent == "subagentStart" && ctx.CurrentSessionSpan != nil {
		return &ParentRef{ctx.CurrentSessionSpan.TraceID, ctx.CurrentSessionSpan.SpanID}, nil
	}

	if toolStartEvents[hookEvent] {
		if ctx.CurrentSubagentSpan != nil {
			return &ParentRef{ctx.CurrentSubagentSpan.TraceID, ctx.CurrentSubagentSpan.SpanID}, nil
		}
		if ctx.CurrentSessionSpan != nil {
			return &ParentRef{ctx.CurrentSessionSpan.TraceID, ctx.CurrentSessionSpan.SpanID}, nil
		}
	}

	if ctx.CurrentSubagentSpan != nil {
		return &ParentRef{ctx.CurrentSubagentSpan.TraceID, ctx.CurrentSubagentSpan.SpanID}, nil
	}
	if ctx.CurrentSessionSpan != nil {
		return &ParentRef{ctx.CurrentSessionSpan.TraceID, ctx.CurrentSessionSpan.SpanID}, nil
	}

	return nil, nil
}

func (c *ContextManager) GetSessionTraceID(generationID string) (*[16]byte, error) {
	var ctx generationContext
	found, err := readLocked(c.contextFile(generationID), &ctx)
	if err != nil || !found {
		return nil, err
	}
	return ctx.SessionTraceID, nil
}

func (c *ContextManager) SaveConversationTraceID(conversationID string, traceID [16]byte) error {
	return writeLocked(c.conversationFile(conversationID), conversationContext{
		SessionTraceID: traceID,
		Timestamp:      float64(time.Now().UnixNano()) / 1e9,
	})
}

func (c *ContextManager) GetConversationTraceID(conversationID string) (*[16]byte, error) {
	var ctx conversationContext
	found, err := readLocked(c.conversationFile(conversationID), &ctx)
	if err != nil || !found {
		return nil, err
	}
	return &ctx.SessionTraceID, nil
}

// SaveSpanContext records the just-created span as the new "current" span of
// its type, so subsequent hook events (in other processes) can find it as
// their parent.
func (c *ContextManager) SaveSpanContext(generationID, hookEvent string, traceID [16]byte, spanID [8]byte) error {
	path := c.contextFile(generationID)

	var ctx generationContext
	if _, err := readLocked(path, &ctx); err != nil {
		return err
	}

	info := &spanRef{TraceID: traceID, SpanID: spanID, HookEvent: hookEvent, Timestamp: float64(time.Now().UnixNano()) / 1e9}

	switch hookEvent {
	case "sessionStart":
		ctx = generationContext{CurrentSessionSpan: info, SessionTraceID: &traceID}
	case "sessionEnd":
		ctx = generationContext{}
	case "subagentStart":
		ctx.CurrentSubagentSpan = info
		ctx.CurrentToolSpan = nil
	case "subagentStop":
		ctx.CurrentSubagentSpan = nil
		ctx.CurrentToolSpan = nil
	case "preToolUse", "beforeShellExecution", "beforeMCPExecution", "beforeReadFile":
		ctx.CurrentToolSpan = info
	case "postToolUse", "postToolUseFailure", "afterShellExecution", "afterMCPExecution", "afterFileEdit":
		ctx.CurrentToolSpan = nil
	}

	return writeLocked(path, ctx)
}

func (c *ContextManager) CleanupContext(generationID string) {
	_ = os.Remove(c.contextFile(generationID))
}
