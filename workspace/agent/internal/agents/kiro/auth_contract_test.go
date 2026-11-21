//go:build contract

// Contract for the Console sign-in start: the real `kiro-cli login --use-device-flow` must
// still print a device URL that HandleStart's scrape accepts. Needs no credential: the URL
// prints before anything is approved, and the test closes the flow. HOME is an empty staging
// directory, so no real login is touched.
//
//	go test -tags contract -run TestContractKiroSignInURL ./internal/agents/kiro/
package kiro

import (
	"net/url"
	"os/exec"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func TestContractKiroSignInURL(t *testing.T) {
	// A missing CLI is a failure, never a skip: a skipped contract reads as green.
	if _, err := exec.LookPath(bin()); err != nil {
		t.Fatalf("%s not on PATH: %v", bin(), err)
	}
	f, err := agents.StartFlow(loginCmd(t.TempDir()))
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	defer f.Close()

	got := loginURLRe.FindString(f.WaitFor(loginURLRe, 20*time.Second))
	if got == "" {
		t.Fatalf("no device URL matched loginURLRe within 20s; CLI output:\n%s", f.Clean())
	}
	if u, err := url.Parse(got); err != nil || u.Scheme != "https" || u.Host == "" {
		t.Fatalf("unexpected device URL %q (err %v)", got, err)
	}
}
