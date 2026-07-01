package config

import (
	"errors"
	"testing"
)

func strPtr(s string) *string { return &s }

func TestFlagWinsOverYAMLAndPrompt(t *testing.T) {
	promptCalled := false
	prompt := func() (string, error) {
		promptCalled = true
		return "prompt-value", nil
	}

	yamlConfig := map[string]interface{}{
		"connect": map[string]interface{}{"endpoint": "yaml-value"},
	}

	got, err := Resolve("connect.endpoint", strPtr("flag-value"), yamlConfig, prompt, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "flag-value" {
		t.Errorf("got %q, want %q", got, "flag-value")
	}
	if promptCalled {
		t.Error("prompt should not have been called")
	}
}

func TestYAMLWinsOverPrompt(t *testing.T) {
	promptCalled := false
	prompt := func() (string, error) {
		promptCalled = true
		return "prompt-value", nil
	}

	yamlConfig := map[string]interface{}{
		"connect": map[string]interface{}{"endpoint": "yaml-value"},
	}

	got, err := Resolve[string]("connect.endpoint", nil, yamlConfig, prompt, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "yaml-value" {
		t.Errorf("got %q, want %q", got, "yaml-value")
	}
	if promptCalled {
		t.Error("prompt should not have been called")
	}
}

func TestPromptUsedWhenNothingElseSet(t *testing.T) {
	prompt := func() (string, error) { return "prompt-value", nil }

	got, err := Resolve[string]("connect.endpoint", nil, map[string]interface{}{}, prompt, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "prompt-value" {
		t.Errorf("got %q, want %q", got, "prompt-value")
	}
}

func TestNonInteractiveUsesDefaultWithoutPrompting(t *testing.T) {
	promptCalled := false
	prompt := func() (string, error) {
		promptCalled = true
		return "prompt-value", nil
	}

	got, err := Resolve("connect.endpoint", (*string)(nil), map[string]interface{}{}, prompt, true, strPtr("default-value"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "default-value" {
		t.Errorf("got %q, want %q", got, "default-value")
	}
	if promptCalled {
		t.Error("prompt should not have been called")
	}
}

func TestNonInteractiveRaisesWhenNoDefault(t *testing.T) {
	prompt := func() (string, error) { return "prompt-value", nil }

	_, err := Resolve[string]("connect.endpoint", nil, map[string]interface{}{}, prompt, true, nil)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var missing *MissingConfigError
	if !errors.As(err, &missing) {
		t.Fatalf("expected MissingConfigError, got %T: %v", err, err)
	}
}
