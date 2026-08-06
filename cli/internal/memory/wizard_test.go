package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAbsolutizeProjectsMakesRelativePathsAbsolute is finding M5's
// regression test. Credentials.AllowsPath compares cleaned, absolute paths
// component-wise; a relative entry here would never match anything a real
// Turn.CWD (always absolute) could produce, silently pushing nothing while
// looking fully configured.
func TestAbsolutizeProjectsMakesRelativePathsAbsolute(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := absolutizeProjects("relative/dir")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %v, want one entry", got)
	}
	if !filepath.IsAbs(got[0]) {
		t.Errorf("path = %q, want it absolute", got[0])
	}
	want := filepath.Join(wd, "relative/dir")
	if got[0] != want {
		t.Errorf("path = %q, want %q", got[0], want)
	}
}

func TestAbsolutizeProjectsExpandsBareTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := absolutizeProjects("~")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != home {
		t.Errorf("got %v, want [%q]", got, home)
	}
}

func TestAbsolutizeProjectsExpandsHomeSlashPrefix(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := absolutizeProjects("~/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(home, "repo")
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %v, want [%q]", got, want)
	}
}

func TestAbsolutizeProjectsLeavesAlreadyAbsolutePaths(t *testing.T) {
	got, err := absolutizeProjects("/home/dev/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "/home/dev/repo" {
		t.Errorf("got %v, want [/home/dev/repo]", got)
	}
}

func TestAbsolutizeProjectsHandlesMultipleCommaSeparated(t *testing.T) {
	got, err := absolutizeProjects("/home/dev/repo, relative/dir")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 entries", got)
	}
	for _, p := range got {
		if !filepath.IsAbs(p) {
			t.Errorf("path = %q, want it absolute", p)
		}
	}
}

func TestAbsolutizeProjectsRejectsEmptyInput(t *testing.T) {
	if _, err := absolutizeProjects(""); err == nil {
		t.Error("expected an error for an empty allowlist")
	}
}
