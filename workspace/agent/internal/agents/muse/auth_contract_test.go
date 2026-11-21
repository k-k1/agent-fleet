//go:build contract

// Contract for the Console sign-in start: the real `muse login` must still print a device
// URL that HandleStart's scrape accepts. Needs no credential: the URL prints before anything
// is approved, and the test closes the flow. HOME is an empty staging directory, so no real
// login is touched.
//
//	go test -tags contract -run TestContractMuseSignInURL ./internal/agents/muse/
package muse

import (
	"net/url"
	"os/exec"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func TestContractMuseSignInURL(t *testing.T) {
	// A missing CLI is a failure, never a skip: a skipped contract reads as green.
	if _, err := exec.LookPath(Bin()); err != nil {
		t.Fatalf("%s not on PATH: %v", Bin(), err)
	}
	f, err := agents.StartPipeFlow(loginCmd(t.TempDir()))
	if err != nil {
		t.Fatalf("StartPipeFlow: %v", err)
	}
	defer f.Close()

	got := f.WaitFor(loginURLRe, 20*time.Second)
	if got == "" {
		t.Fatalf("no device URL matched loginURLRe within 20s; CLI output:\n%s", f.Clean())
	}
	if u, err := url.Parse(got); err != nil || u.Scheme != "https" || u.Host == "" {
		t.Fatalf("unexpected device URL %q (err %v)", got, err)
	}
}
