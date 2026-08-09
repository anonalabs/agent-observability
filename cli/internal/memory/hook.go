package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// maxHookLogBytes caps the hook's log. The hook runs unattended on every
// session end, so an uncapped log would grow without anyone noticing.
const maxHookLogBytes = 1 << 20 // 1 MiB

// HookPayload is the subset of Claude Code's Stop-hook stdin this needs.
type HookPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
}

func ParseHookPayload(r io.Reader) (HookPayload, error) {
	var p HookPayload
	data, err := io.ReadAll(r)
	if err != nil {
		return p, err
	}
	if len(data) == 0 {
		return p, fmt.Errorf("empty hook payload")
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("parsing hook payload: %w", err)
	}
	return p, nil
}

// configDir is where the hook keeps its log and lock files -- alongside the
// config, so AGENTOBS_MEMORY_CONFIG relocates all of it together in tests.
func configDir() (string, error) {
	path, err := CredentialsPath()
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}

func HookLogPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sync.log"), nil
}

// LogHook appends a timestamped line to the hook log. It never writes to
// stdout or stderr: Claude Code reads a Stop hook's output, and a broken
// connector must stay invisible to the agent. Every failure here is
// swallowed for the same reason.
func LogHook(format string, args ...interface{}) {
	path, err := HookLogPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}

	if info, err := os.Stat(path); err == nil && info.Size() > maxHookLogBytes {
		truncateLogFront(path)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// truncateLogFront keeps the newest half of an oversized log, so recent
// entries survive while the file stops growing.
func truncateLogFront(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	keep := data[len(data)/2:]
	if i := indexByte(keep, '\n'); i >= 0 {
		keep = keep[i+1:]
	}
	_ = os.WriteFile(path, keep, 0o600)
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// ProjectLock takes an exclusive, non-blocking lock for one project.
// It returns (nil, nil) when another process already holds it -- two
// concurrent syncs of the same project would double-push, since the batch
// endpoint has no idempotency key. Waiting is wrong here: the next session
// end will sync anyway, so the right move is to skip this run.
func ProjectLock(p *Project) (*flock.Flock, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	name := fmt.Sprintf("sync-%s.lock", sha256Hex(p.Path))
	lock := flock.New(filepath.Join(dir, name))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return lock, nil
}

// sha256Hex hashes a project path for use in a lock filename -- paths
// contain separators and arbitrary characters that a filename cannot.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
