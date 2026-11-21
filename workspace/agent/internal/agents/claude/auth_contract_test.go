//go:build contract

// Contract for the Console sign-in start: the real `claude auth login --claudeai` must still
// print an authorize URL that HandleStart's scrape accepts.
//
// Why it exists: 2.1.330 moved the printed URL from claude.com/cai/oauth/authorize to
// claude.ai/oauth/authorize, the scraper's urlRe stopped matching, and Console
// re-authentication broke while every other contract stayed green.
//
// Needs no credential and no network login: the CLI prints the URL before anything is
// entered, and the test closes the flow without submitting a code. HOME and
// CLAUDE_CONFIG_DIR point at an empty directory so no real login is read or touched.
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
)

func TestContractClaudeSignInURL(t *testing.T) {
	// A missing CLI is a failure, never a skip: a skipped contract reads as green.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Fatalf("claude not on PATH: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", home+"/.claude")
	// Credentials in the environment would make the CLI skip or alter the sign-in.
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

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
