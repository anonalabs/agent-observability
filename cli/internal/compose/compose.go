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

func Run(composeFile string, args ...string) error {
	fullArgs := append([]string{"compose", "-f", composeFile}, args...)
	cmd := exec.Command("docker", fullArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func Up(composeFile string, detach bool) error {
	args := []string{"up"}
	if detach {
		args = append(args, "-d")
	}
	return Run(composeFile, args...)
}

func DockerAvailable() bool {
	cmd := exec.Command("docker", "info")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}
