package agents

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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

// noStrayTempFiles fails the test if any of our temp files (the
// ".settings.json.tmp-*" pattern used by registerStopHookAt) are left
// behind in dir. A leftover temp file would mean a cleanup path was missed.
func noStrayTempFiles(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".settings.json.tmp-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("temp files left behind: %v", matches)
	}
}

func TestRegisterStopHookAtBacksUpBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := []byte(`{"model":"opus"}`)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := registerStopHookAt(path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("reading backup: %v", err)
	}
	if !bytes.Equal(backup, original) {
		t.Errorf("backup = %s, want the original bytes %s", backup, original)
	}
	noStrayTempFiles(t, dir)
}

func TestRegisterStopHookAtAbortsOnMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := []byte(`{not valid json`)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := registerStopHookAt(path); err == nil {
		t.Fatal("expected an error for malformed JSON, got nil")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading settings after abort: %v", err)
	}
	if !bytes.Equal(after, original) {
		t.Errorf("settings.json changed after an abort: got %s, want unchanged %s", after, original)
	}
	noStrayTempFiles(t, dir)
}

func TestRegisterStopHookAtAbortsOnEmptyExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := registerStopHookAt(path); err == nil {
		t.Fatal("expected an error for an empty existing file, got nil")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading settings after abort: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("settings.json changed after an abort: got %q, want empty", after)
	}
	noStrayTempFiles(t, dir)
}

func TestRegisterStopHookAtLeavesNoTempFileOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	if _, err := registerStopHookAt(path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	noStrayTempFiles(t, dir)
}
