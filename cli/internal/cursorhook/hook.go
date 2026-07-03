package cursorhook

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func getString(m map[string]interface{}, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func stringOr(m map[string]interface{}, key, def string) string {
	if s, ok := getString(m, key); ok {
		return s
	}
	return def
}

func newTraceID() [16]byte {
	var id [16]byte
	_, _ = rand.Read(id[:])
	return id
}

var toolCompletionOrStartOperation = map[string]string{
	"beforeSubmitPrompt": "chat", "preToolUse": "tool", "postToolUse": "tool",
	"postToolUseFailure": "tool", "beforeShellExecution": "tool", "afterShellExecution": "tool",
	"beforeMCPExecution": "tool", "afterMCPExecution": "tool", "beforeReadFile": "tool",
	"afterFileEdit": "tool", "subagentStart": "chain", "subagentStop": "chain",
	"sessionStart": "session", "sessionEnd": "session",
	"permissionRequest": "tool", "errorOccurred": "chain",
}

var eventSpanKind = map[string]string{
	"beforeSubmitPrompt": "llm", "preToolUse": "tool", "postToolUse": "tool",
	"postToolUseFailure": "tool", "beforeShellExecution": "tool", "afterShellExecution": "tool",
	"beforeMCPExecution": "tool", "afterMCPExecution": "tool", "beforeReadFile": "tool",
	"afterFileEdit": "tool", "subagentStart": "chain", "subagentStop": "chain",
	"permissionRequest": "tool", "errorOccurred": "chain",
}

// canonicalEvent maps each source agent's own hook event vocabulary onto ours
// (Cursor's own camelCase names, since that's what event_attributes.go's
// switch and the maps above are keyed on). Cursor's names pass through
// unchanged; Codex's PascalCase names and any event name we don't recognize
// pass through as-is too, so an unmapped event still gets a "chain"-kind,
// "unknown"-operation span instead of being dropped.
var canonicalEvent = map[string]string{
	// Copilot coding agent
	"userPromptSubmitted": "beforeSubmitPrompt",
	// Codex, and shared with OpenCode's plugin (also emits PascalCase names)
	"SessionStart":       "sessionStart",
	"SessionEnd":         "sessionEnd",
	"PreToolUse":         "preToolUse",
	"PostToolUse":        "postToolUse",
	"PostToolUseFailure": "postToolUseFailure",
	"UserPromptSubmit":   "beforeSubmitPrompt",
	"PermissionRequest":  "permissionRequest",
	"AfterFileEdit":      "afterFileEdit",
	"Stop":               "stop",
}

func normalizeEvent(raw string) string {
	if canon, ok := canonicalEvent[raw]; ok {
		return canon
	}
	return raw
}

// spanNamePrefix derives a short prefix like "cursor" or "copilot" from a
// Config.ServiceName like "cursor-agent"/"copilot-agent" -- each agent's
// wrapper config sets its own ServiceName, so this needs no separate field.
func spanNamePrefix(serviceName string) string {
	return strings.TrimSuffix(serviceName, "-agent")
}

func mapEventToOperation(event string) string {
	if op, ok := toolCompletionOrStartOperation[event]; ok {
		return op
	}
	return "unknown"
}

func mapEventToSpanKind(event string) string {
	if k, ok := eventSpanKind[event]; ok {
		return k
	}
	return "chain"
}

// generateResponse returns the response the calling hook runner expects on
// stdout. Only verified against Cursor's actual hook protocol, where
// pre-approval events (beforeShellExecution etc.) must explicitly return
// {"permission":"allow"} or Cursor blocks the action. For every other agent
// (Copilot, Codex, or anything else reusing this binary) we don't have the
// real tool to test against, so this deliberately does NOT invent an ack
// schema for their permission-shaped events (e.g. Codex's PermissionRequest,
// canonicalized to "permissionRequest") -- returning the wrong shape could
// silently block/confuse a runner we can't verify. Those get the same empty
// ack as any other non-blocking event; if a given runner turns out to need
// something else, that's a one-line addition here once it's actually tested.
func generateResponse(agent, event string) map[string]interface{} {
	if agent != "cursor" {
		return map[string]interface{}{}
	}
	permissionEvents := map[string]bool{
		"beforeShellExecution": true, "beforeMCPExecution": true,
		"beforeReadFile": true, "beforeSubmitPrompt": true,
	}
	if permissionEvents[event] {
		return map[string]interface{}{"permission": "allow"}
	}
	return map[string]interface{}{}
}

// ProcessHook builds and exports one span for a single hook event from any
// supported agent's hook runner (Cursor, and any other hook-based agent
// reusing this same binary via a different wrapper script/config -- e.g.
// Copilot or Codex), returning the JSON response that runner expects on
// stdout.
func ProcessHook(cfg Config, data map[string]interface{}) (map[string]interface{}, error) {
	rawEvent := stringOr(data, "hook_event_name", stringOr(data, "hook_event_type", "unknown"))
	hookEvent := normalizeEvent(rawEvent)
	// conversation_id is Cursor's name for the session key; session_id is
	// Copilot's (and generically what most other hook-based runners use).
	conversationID := stringOr(data, "conversation_id", stringOr(data, "session_id", "unknown"))
	generationID := stringOr(data, "generation_id", "unknown")

	ctxMgr, err := NewContextManager()
	if err != nil {
		return nil, err
	}

	var parent *ParentRef
	if generationID != "unknown" {
		parent, err = ctxMgr.GetParentContext(generationID, hookEvent)
		if err != nil {
			return nil, err
		}
	}

	traceID, spanID, parentSpanID, err := resolveSpanIDs(ctxMgr, parent, hookEvent, conversationID, generationID)
	if err != nil {
		return nil, err
	}

	startTime := time.Now()
	agent := spanNamePrefix(cfg.ServiceName)

	span := Span{
		TraceID:      traceID,
		SpanID:       spanID,
		ParentSpanID: parentSpanID,
		Name:         agent + "." + hookEvent,
		Kind:         tracepb.Span_SPAN_KIND_INTERNAL,
		StartTime:    startTime,
		Attributes:   map[string]interface{}{},
		StatusCode:   tracepb.Status_STATUS_CODE_OK,
	}

	addCommonAttributes(&span, data, hookEvent, conversationID, generationID, parentSpanID)
	addEventSpecificAttributes(&span, cfg, hookEvent, data)

	response := generateResponse(agent, hookEvent)
	if perm, ok := response["permission"]; ok {
		span.Attributes["langsmith.metadata.permission"] = perm
	}

	span.EndTime = time.Now()
	span.Attributes["langsmith.metadata.duration_ms"] = float64(span.EndTime.Sub(startTime).Microseconds()) / 1000.0

	if err := Export(cfg, span); err != nil {
		return nil, fmt.Errorf("exporting span: %w", err)
	}

	if hookEvent == "sessionStart" && conversationID != "unknown" {
		if err := ctxMgr.SaveConversationTraceID(conversationID, traceID); err != nil {
			return nil, err
		}
	}
	if generationID != "unknown" {
		if err := ctxMgr.SaveSpanContext(generationID, hookEvent, traceID, spanID); err != nil {
			return nil, err
		}
	}
	if hookEvent == "stop" && generationID != "unknown" {
		ctxMgr.CleanupContext(generationID)
	}

	return response, nil
}

// resolveSpanIDs works out this span's trace_id/span_id/parent_span_id,
// preferring (in order): an existing cross-process parent, this generation's
// stored session trace_id, the conversation's stored trace_id, a fresh
// deterministic trace_id for sessionStart, or finally a brand-new root trace.
func resolveSpanIDs(ctxMgr *ContextManager, parent *ParentRef, hookEvent, conversationID, generationID string) (traceID [16]byte, spanID [8]byte, parentSpanID *[8]byte, err error) {
	spanID = NewSpanID()

	if parent == nil {
		if hookEvent == "sessionStart" && conversationID != "unknown" {
			return GenerateSessionTraceID(conversationID), spanID, nil, nil
		}
		if conversationID != "unknown" {
			if tid, err := ctxMgr.GetConversationTraceID(conversationID); err != nil {
				return traceID, spanID, nil, err
			} else if tid != nil {
				return *tid, spanID, nil, nil
			}
		}
		return newTraceID(), spanID, nil, nil
	}

	var sessionTraceID *[16]byte
	if generationID != "unknown" {
		if sessionTraceID, err = ctxMgr.GetSessionTraceID(generationID); err != nil {
			return traceID, spanID, nil, err
		}
	}
	if sessionTraceID == nil && conversationID != "unknown" {
		if sessionTraceID, err = ctxMgr.GetConversationTraceID(conversationID); err != nil {
			return traceID, spanID, nil, err
		}
	}

	psid := parent.SpanID
	if sessionTraceID != nil {
		return *sessionTraceID, spanID, &psid, nil
	}
	return parent.TraceID, spanID, &psid, nil
}

func addCommonAttributes(span *Span, data map[string]interface{}, hookEvent, conversationID, generationID string, parentSpanID *[8]byte) {
	span.Attributes["langsmith.trace.id"] = hex(span.TraceID[:])
	span.Attributes["langsmith.span.id"] = hex(span.SpanID[:])
	if parentSpanID != nil {
		span.Attributes["langsmith.span.parent_id"] = hex(parentSpanID[:])
	}
	if conversationID != "unknown" {
		span.Attributes["langsmith.trace.session_id"] = conversationID
	}
	if generationID != "unknown" {
		span.Attributes["langsmith.metadata.generation_id"] = generationID
	}

	if model, ok := getString(data, "model"); ok {
		span.Attributes["gen_ai.request.model"] = model
		span.Attributes["gen_ai.response.model"] = model
		lower := strings.ToLower(model)
		switch {
		case strings.Contains(lower, "claude"):
			span.Attributes["gen_ai.system"] = "anthropic"
		case strings.Contains(lower, "gpt"), strings.Contains(lower, "o1"):
			span.Attributes["gen_ai.system"] = "openai"
		default:
			span.Attributes["gen_ai.system"] = "cursor"
		}
	}

	for _, key := range []string{"cursor_version", "user_email", "transcript_path"} {
		if v, ok := getString(data, key); ok {
			span.Attributes["langsmith.metadata."+key] = v
		}
	}

	if roots, ok := data["workspace_roots"].([]interface{}); ok && len(roots) > 0 {
		b, _ := json.Marshal(roots)
		span.Attributes["langsmith.metadata.workspace_roots"] = string(b)
	}

	span.Attributes["langsmith.metadata.hook_event"] = hookEvent
}

func hex(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = digits[c>>4]
		out[i*2+1] = digits[c&0x0f]
	}
	return string(out)
}
