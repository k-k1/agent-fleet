// Package contractenv isolates the sign-in contract tests (`-tags contract`) from every
// credential and profile the host environment carries. Only test files import it.
package contractenv

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// keep is the allowlist of variables a sign-in CLI may inherit: what it needs to start and to
// reach the network, nothing that names a profile, token or key. A denylist would miss the
// next vendor variable (KIRO_HOME, KIRO_API_KEY, OPENAI_API_KEY, ANTHROPIC_AUTH_TOKEN, …).
var keep = map[string]bool{
	"PATH": true, "LANG": true, "TERM": true, "TMPDIR": true, "TZ": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true,
	// The harness's own switches for which binary runs and that it must not self-update; the
	// test LookPaths the same name, so dropping them would run a different binary.
	"AGENT_MUSE_BIN": true, "AGENT_CURSOR_BIN": true, "AGENT_KIRO_BIN": true, "DISABLE_AUTOUPDATER": true,
}

// Isolate drops every variable outside the allowlist for the rest of the test, points HOME at
// an empty directory and returns it. The original environment is restored on cleanup.
func Isolate(t *testing.T) (home string) {
	t.Helper()
	saved := os.Environ()
	for _, kv := range saved {
		k, _, _ := strings.Cut(kv, "=")
		if !keep[k] && !strings.HasPrefix(k, "LC_") {
			_ = os.Unsetenv(k)
		}
	}
	t.Cleanup(func() {
		os.Clearenv()
		for _, kv := range saved {
			k, v, _ := strings.Cut(kv, "=")
			_ = os.Setenv(k, v)
		}
	})
	home = t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// Start launches a flow and waits for the scrape, retrying only when the CLI exited without
// printing one: that is a failed code request (DNS, API outage), which does not say the output
// format changed. A CLI that keeps running without the expected output is never retried, so a
// changed URL stays red.
func Start(t *testing.T, launch func() (*agents.Flow, error), wait func(*agents.Flow) string) (*agents.Flow, string) {
	t.Helper()
	const attempts = 3
	for i := 1; ; i++ {
		f, err := launch()
		if err != nil {
			t.Fatalf("launch: %v", err)
		}
		if got := wait(f); got != "" {
			t.Cleanup(f.Close)
			return f, got
		}
		if i == attempts || !f.Ended() {
			t.Cleanup(f.Close)
			return f, ""
		}
		t.Logf("attempt %d: CLI exited without the expected output, retrying:\n%s", i, f.Clean())
		f.Close()
		time.Sleep(5 * time.Second)
	}
}
