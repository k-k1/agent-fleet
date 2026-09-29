package muse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// A CLI that dies during the handshake used to leave only "initialize failed": its stderr went
// to /dev/null. The start error must now carry the redacted end of it, and Error() — which
// callers log — must not.
func TestSpawnFailureCarriesStderrTail(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // the watch goroutine records the exit under HOME
	fake := filepath.Join(t.TempDir(), "fake-cli")
	// Assembled at run time: a literal assignment trips the repository's full-history secret
	// scan (gitleaks' generic-api-key).
	fakeValue := "abcdef" + "0123456789xyz"
	script := "#!/bin/sh\necho 'Error: You are not logged in' >&2\necho 'debug: API_TOKEN=" + fakeValue + "' >&2\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_MUSE_BIN", fake)

	h := &threadHandle{name: "t1", dir: t.TempDir()}
	err := h.spawn(agents.ThreadSettings{})
	if err == nil {
		t.Fatal("spawn succeeded against a CLI that exits at once")
	}
	tail := agents.StartErrStderr(err)
	if !strings.Contains(tail, "Error: You are not logged in") {
		t.Fatalf("stderr tail = %q, want the CLI's own reason (err: %v)", tail, err)
	}
	if strings.Contains(tail, fakeValue) {
		t.Fatalf("stderr tail leaks the secret: %q", tail)
	}
	if strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("Error() carries the tail, so it would reach the Agent log: %q", err.Error())
	}
}
