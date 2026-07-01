package cursorhook

import "regexp"

var sensitiveFields = []string{
	"prompt", "user_message", "agent_message", "tool_input", "tool_output",
	"mcp_input", "command", "file_path", "edits", "transcript_path",
}

var pathPatterns = []*regexp.Regexp{
	regexp.MustCompile(`/home/[^/]+`),
	regexp.MustCompile(`/Users/[^/]+`),
	regexp.MustCompile(`C:\\Users\\[^\\]+`),
	regexp.MustCompile(`^/root`),
}

// MaskSensitiveData returns a copy of data with prompt/tool I/O redacted,
// email addresses partially masked, and workspace paths' username components
// masked -- mirrors the Python privacy.py behavior field-for-field.
func MaskSensitiveData(data map[string]interface{}) map[string]interface{} {
	masked := make(map[string]interface{}, len(data))
	for k, v := range data {
		masked[k] = v
	}

	for _, field := range sensitiveFields {
		if _, ok := masked[field]; ok {
			masked[field] = "[MASKED]"
		}
	}

	if email, ok := masked["user_email"].(string); ok {
		masked["user_email"] = MaskEmail(email)
	}

	if roots, ok := masked["workspace_roots"].([]interface{}); ok {
		maskedRoots := make([]interface{}, len(roots))
		for i, r := range roots {
			if s, ok := r.(string); ok {
				maskedRoots[i] = MaskPath(s)
			} else {
				maskedRoots[i] = r
			}
		}
		masked["workspace_roots"] = maskedRoots
	}

	return masked
}

// MaskEmail preserves the domain: user@example.com -> u***@example.com.
func MaskEmail(email string) string {
	at := -1
	for i, c := range email {
		if c == '@' {
			at = i
			break
		}
	}
	if at == -1 {
		return "[MASKED]"
	}
	local, domain := email[:at], email[at+1:]
	if len(local) <= 1 {
		return "*@" + domain
	}
	return string(local[0]) + "***@" + domain
}

// MaskPath masks username-like path components while preserving structure.
func MaskPath(path string) string {
	masked := path
	masked = pathPatterns[0].ReplaceAllString(masked, "/home/[USER]")
	masked = pathPatterns[1].ReplaceAllString(masked, "/Users/[USER]")
	masked = pathPatterns[2].ReplaceAllString(masked, `C:\Users\[USER]`)
	masked = pathPatterns[3].ReplaceAllString(masked, "/[USER]")
	return masked
}
