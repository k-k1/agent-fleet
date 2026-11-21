//go:build contract

// Contract for the Console sign-in start: the real `cursor-agent login` must still print a
// login URL that HandleStart's scrape accepts. Needs no credential: the URL prints before
// anything is approved, and the test closes the flow. HOME is an empty staging directory, so
// no real login is touched.
//
//	go test -tags contract -run TestContractCursorSignInURL ./internal/agents/cursor/
package cursor

import (
	"net/url"
	"os/exec"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func TestContractCursorSignInURL(t *testing.T) {
	// A missing CLI is a failure, never a skip: a skipped contract reads as green.
	if _, err := exec.LookPath(bin()); err != nil {
		t.Fatalf("%s not on PATH: %v", bin(), err)
	}
	f, err := agents.StartFlow(loginCmd(t.TempDir()))
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	defer f.Close()

	got := sanitizeURL(f.WaitFor(loginURLRe, 20*time.Second))
	if got == "" {
		t.Fatalf("no login URL matched loginURLRe within 20s; CLI output:\n%s", f.Clean())
	}
	if u, err := url.Parse(got); err != nil || u.Scheme != "https" || u.Host != "cursor.com" {
		t.Fatalf("unexpected login URL %q (err %v)", got, err)
	}
}
