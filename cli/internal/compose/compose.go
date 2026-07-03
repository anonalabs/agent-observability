// Package compose wraps `docker compose` against this repo's docker-compose.yml.
//
// Unlike the Python CLI (which located the compose file relative to the
// installed package's source path), a compiled Go binary carries no such
// path. Instead this searches upward from the current working directory
// (same convention as `git`/`docker compose` itself), with an env var/flag
// override for anything else.
package compose

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

var Services = []string{"otel-collector", "prometheus", "clickhouse", "grafana"}

func DefaultComposeFile() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		candidate := filepath.Join(dir, "docker-compose.yml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf(
		"could not find docker-compose.yml in the current directory or any parent; " +
			"run agentobs from within the repo, or pass --compose-file explicitly",
	)
}

func ResolveComposeFile(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env := os.Getenv("AGENTOBS_COMPOSE_FILE"); env != "" {
		return env, nil
	}
	return DefaultComposeFile()
}

// Run invokes `docker compose` against one or more compose files (later
// files layer as overlays, e.g. a base file plus a security-hardening
// override), same semantics as `docker compose -f a.yml -f b.yml ...`.
func Run(composeFiles []string, args ...string) error {
	fullArgs := []string{"compose"}
	for _, f := range composeFiles {
		fullArgs = append(fullArgs, "-f", f)
	}
	fullArgs = append(fullArgs, args...)
	cmd := exec.Command("docker", fullArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func Up(composeFiles []string, detach bool) error {
	args := []string{"up"}
	if detach {
		args = append(args, "-d")
	}
	return Run(composeFiles, args...)
}

// SecureOverlayFile locates docker-compose.secure.yml next to the resolved
// base compose file.
func SecureOverlayFile(baseComposeFile string) (string, error) {
	overlay := filepath.Join(filepath.Dir(baseComposeFile), "docker-compose.secure.yml")
	if _, err := os.Stat(overlay); err != nil {
		return "", fmt.Errorf("docker-compose.secure.yml not found next to %s: %w", baseComposeFile, err)
	}
	return overlay, nil
}

func DockerAvailable() bool {
	cmd := exec.Command("docker", "info")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}
