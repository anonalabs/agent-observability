package agents

import (
	"encoding/json"
	"testing"
)

func TestMergeStopHookPreservesUnrelatedHooks(t *testing.T) {
	existing := map[string]interface{}{}
	raw := `{
	  "model": "opus",
	  "hooks": {
	    "PreToolUse": [{"matcher":"Bash","hooks":[{"type":"command","command":"other-tool hook"}]}]
	  }
	}`
	if err := json.Unmarshal([]byte(raw), &existing); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	merged := MergeStopHook(existing)

	if merged["model"] != "opus" {
		t.Errorf("unrelated top-level keys must survive: %+v", merged)
	}
	hooks, ok := merged["hooks"].(map[string]interface{})
	if !ok {
		t.Fatalf("hooks = %T, want a map", merged["hooks"])
	}
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Error("an unrelated hook event must not be dropped")
	}
	stop, ok := hooks["Stop"].([]interface{})
	if !ok || len(stop) != 1 {
		t.Fatalf("Stop = %#v, want one entry", hooks["Stop"])
	}
}

func TestMergeStopHookIsIdempotent(t *testing.T) {
	merged := MergeStopHook(map[string]interface{}{})
	again := MergeStopHook(merged)

	hooks := again["hooks"].(map[string]interface{})
	stop := hooks["Stop"].([]interface{})
	if len(stop) != 1 {
		t.Errorf("Stop has %d entries after a second merge, want 1", len(stop))
	}
}

func TestMergeStopHookKeepsForeignStopEntries(t *testing.T) {
	existing := map[string]interface{}{}
	raw := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"someone-elses-tool"}]}]}}`
	if err := json.Unmarshal([]byte(raw), &existing); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	merged := MergeStopHook(existing)

	hooks := merged["hooks"].(map[string]interface{})
	stop := hooks["Stop"].([]interface{})
	if len(stop) != 2 {
		t.Fatalf("Stop has %d entries, want the foreign one plus ours", len(stop))
	}
	found := false
	for _, entry := range stop {
		m := entry.(map[string]interface{})
		inner := m["hooks"].([]interface{})
		for _, h := range inner {
			if h.(map[string]interface{})["command"] == "someone-elses-tool" {
				found = true
			}
		}
	}
	if !found {
		t.Error("another tool's Stop hook must not be replaced")
	}
}
