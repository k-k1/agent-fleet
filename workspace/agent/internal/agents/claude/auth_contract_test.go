//go:build contract

// Contract for the Console sign-in start: the real `claude auth login --claudeai` must still
// print an authorize URL that HandleStart's scrape accepts. A URL the scraper cannot read
// leaves the Console unable to re-authenticate while every other contract stays green.
//
// Needs no credential: the CLI prints the URL before anything is entered, and the test closes
// the flow without submitting a code. The environment is reduced to an allowlist and HOME is
// empty, so no real login is read or touched.
//
//	go test -tags contract -run TestContractClaudeSignInURL ./internal/agents/claude/
package claude

import (
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/contractenv"
)

func TestContractClaudeSignInURL(t *testing.T) {
	// A missing CLI is a failure, never a skip: a skipped contract reads as green.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Fatalf("claude not on PATH: %v", err)
	}
	// Only allowlisted variables reach the CLI; CLAUDE_CONFIG_DIR is not among them, so the
	// config lands under the empty HOME.
	contractenv.Isolate(t)

	f, err := agents.StartFlow(loginCmd())
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	defer f.Close() // no code is ever submitted, so nothing is signed in

	got := f.WaitForURL(urlRe, 20*time.Second)
	if got == "" {
		t.Fatalf("no authorize URL matched urlRe within 20s; CLI output:\n%s", f.Clean())
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", got, err)
	}
	if u.Scheme != "https" || (u.Host != "claude.ai" && u.Host != "claude.com") ||
		!strings.HasSuffix(u.Path, "/oauth/authorize") || u.RawQuery == "" {
		t.Fatalf("unexpected authorize URL shape: %s", got)
	}
}
