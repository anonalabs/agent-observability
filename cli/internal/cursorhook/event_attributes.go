package cursorhook

import (
	"encoding/json"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func jsonStr(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// addEventSpecificAttributes mirrors Python's _add_event_specific_attributes:
// per-event-type GenAI/LangSmith attributes, with prompt/tool content masked
// when cfg.MaskPrompts is set.
func addEventSpecificAttributes(span *Span, cfg Config, event string, raw map[string]interface{}) {
	data := raw
	if cfg.MaskPrompts {
		data = MaskSensitiveData(raw)
	}

	span.Attributes["gen_ai.operation.name"] = mapEventToOperation(event)
	span.Attributes["langsmith.span.kind"] = mapEventToSpanKind(event)

	switch event {
	case "sessionStart", "sessionEnd":
		for _, attr := range []string{"session_id", "is_background_agent", "composer_mode"} {
			if v, ok := data[attr]; ok {
				span.Attributes["langsmith.metadata."+attr] = jsonStr(v)
			}
		}

	case "preToolUse", "postToolUse", "postToolUseFailure":
		if event == "postToolUseFailure" {
			// Without this, a failed tool call span stays STATUS_CODE_OK
			// (the zero value set in hook.go) and the tool-failure alert
			// rule -- which filters on StatusCode='STATUS_CODE_ERROR' -- would
			// never match a single real failure.
			span.StatusCode = tracepb.Status_STATUS_CODE_ERROR
			if v, ok := getString(data, "error"); ok {
				span.StatusMessage = v
			}
		}
		if toolName, ok := getString(data, "tool_name"); ok {
			span.Attributes["gen_ai.tool.name"] = toolName
		}
		if input, ok := data["tool_input"]; ok {
			span.Attributes["langsmith.metadata.tool_input"] = jsonStr(input)
			if _, isMap := input.(map[string]interface{}); isMap {
				span.Attributes["gen_ai.tool.arguments"] = jsonStr(input)
			}
		}
		if output, ok := data["tool_output"]; ok {
			s := jsonStr(output)
			if outStr, isStr := output.(string); isStr {
				s = outStr
			}
			if len(s) > 10000 {
				s = s[:10000] + "... (truncated)"
			}
			span.Attributes["langsmith.metadata.tool_output"] = s
		}

	case "beforeShellExecution", "afterShellExecution":
		span.Attributes["gen_ai.tool.name"] = "bash"
		if cmd, ok := getString(data, "command"); ok {
			span.Attributes["gen_ai.tool.arguments"] = jsonStr(map[string]interface{}{"command": cmd})
			span.Attributes["langsmith.metadata.shell_command"] = cmd
		}
		if cwd, ok := getString(data, "cwd"); ok {
			span.Attributes["langsmith.metadata.shell_cwd"] = cwd
		}
		if v, ok := data["timeout"]; ok {
			span.Attributes["langsmith.metadata.shell_timeout"] = v
		}
		if v, ok := data["exit_code"]; ok {
			span.Attributes["langsmith.metadata.shell_exit_code"] = v
		}

	case "beforeMCPExecution", "afterMCPExecution":
		if toolName, ok := getString(data, "mcp_tool"); ok {
			if server, ok := getString(data, "mcp_server"); ok {
				toolName = server + "." + toolName
			}
			span.Attributes["gen_ai.tool.name"] = toolName
		}
		if input, ok := data["mcp_input"]; ok {
			span.Attributes["gen_ai.tool.arguments"] = jsonStr(input)
		}
		if server, ok := getString(data, "mcp_server"); ok {
			span.Attributes["langsmith.metadata.mcp_server"] = server
		}

	case "beforeReadFile", "afterFileEdit":
		toolName := "read_file"
		if event == "afterFileEdit" {
			toolName = "edit_file"
		}
		span.Attributes["gen_ai.tool.name"] = toolName
		if path, ok := getString(data, "file_path"); ok {
			span.Attributes["gen_ai.tool.arguments"] = jsonStr(map[string]interface{}{"file_path": path})
			span.Attributes["langsmith.metadata.file_path"] = path
		}
		if edits, ok := data["edits"].([]interface{}); ok {
			span.Attributes["langsmith.metadata.edit_count"] = len(edits)
		}

	case "beforeSubmitPrompt":
		if prompt, ok := raw["prompt"].(string); ok {
			span.Attributes["gen_ai.prompt.0.role"] = "user"
			if cfg.MaskPrompts {
				span.Attributes["gen_ai.prompt.0.content"] = "[MASKED]"
			} else {
				if len(prompt) > 5000 {
					prompt = prompt[:5000] + "... (truncated)"
				}
				span.Attributes["gen_ai.prompt.0.content"] = prompt
			}
		}

	case "preCompact":
		if v, ok := data["context_size"]; ok {
			span.Attributes["langsmith.metadata.context_size"] = v
		}
		if v, ok := data["context_limit"]; ok {
			span.Attributes["langsmith.metadata.context_limit"] = v
		}

	case "stop":
		if v, ok := data["status"]; ok {
			span.Attributes["langsmith.metadata.completion_status"] = v
		}
		if v, ok := data["loop_count"]; ok {
			span.Attributes["langsmith.metadata.loop_count"] = v
		}

	case "subagentStart", "subagentStop":
		if v, ok := data["subagent_type"]; ok {
			span.Attributes["langsmith.metadata.subagent_type"] = v
		}
		if v, ok := data["subagent_task"]; ok {
			span.Attributes["langsmith.metadata.subagent_task"] = v
		}

	case "errorOccurred":
		span.StatusCode = tracepb.Status_STATUS_CODE_ERROR
		if v, ok := getString(data, "error_message"); ok {
			span.StatusMessage = v
			span.Attributes["langsmith.metadata.error_message"] = v
		} else if v, ok := getString(data, "message"); ok {
			span.StatusMessage = v
			span.Attributes["langsmith.metadata.error_message"] = v
		}
		if v, ok := getString(data, "error_type"); ok {
			span.Attributes["langsmith.metadata.error_type"] = v
		}

	case "permissionRequest":
		if v, ok := getString(data, "tool_name"); ok {
			span.Attributes["gen_ai.tool.name"] = v
		}
		if v, ok := data["tool_input"]; ok {
			span.Attributes["langsmith.metadata.tool_input"] = jsonStr(v)
		}
	}

	span.Attributes["langsmith.metadata.raw_event"] = jsonStr(data)
}
