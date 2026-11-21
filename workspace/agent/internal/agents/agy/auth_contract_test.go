//go:build contract

// Contract for the Console sign-in start: the real `agy` TUI must still show the sign-in
// method selector and, once Enter picks Google OAuth, print an authorize URL that
// HandleStart's scrape accepts. Needs no credential: the URL prints before anything is
// approved, and the test closes the flow. HOME is an empty staging directory, so no real
// login is touched.
//
//	go test -tags contract -run TestContractAgySignInURL ./internal/agents/agy/
package agy

import (
	"net/url"
	"os/exec"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func TestContractAgySignInURL(t *testing.T) {
	// A missing CLI is a failure, never a skip: a skipped contract reads as green.
	if _, err := exec.LookPath("agy"); err != nil {
		t.Fatalf("agy not on PATH: %v", err)
	}
	stage, loginDir := t.TempDir(), t.TempDir()
	if err := seedStagingSettings(stage, loginDir); err != nil {
		t.Fatalf("seedStagingSettings: %v", err)
	}
	f, err := agents.StartFlow(loginCmd(loginDir, stage))
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	defer f.Close()

	if f.WaitFor(selectorRe, 20*time.Second) == "" {
		t.Fatalf("no sign-in method selector within 20s; CLI output:\n%s", f.Clean())
	}
	if _, err := f.Ptmx.Write([]byte(keyEnter)); err != nil {
		t.Fatalf("select Google OAuth: %v", err)
	}
	got := sanitizeAuthURL(f.WaitForURL(urlRe, 20*time.Second))
	if got == "" {
		t.Fatalf("no authorize URL matched urlRe within 20s; CLI output:\n%s", f.Clean())
	}
	if u, err := url.Parse(got); err != nil || u.Scheme != "https" || u.Host != "accounts.google.com" {
		t.Fatalf("unexpected authorize URL %q (err %v)", got, err)
	}
}
