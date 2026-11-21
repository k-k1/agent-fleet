//go:build contract

// Contract for the Console device sign-in start: the real `codex login --device-auth` must
// still print a verification URL and one-time code that HandleDeviceStart's scrape accepts.
// Needs no credential: both print before anything is approved, and the test closes the flow
// without approving. CODEX_HOME is an empty staging directory, so no real login is touched.
//
//	go test -tags contract -run TestContractCodexDeviceSignIn ./internal/agents/codex/
package codex

import (
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/contractenv"
)

func TestContractCodexDeviceSignIn(t *testing.T) {
	// A missing CLI is a failure, never a skip: a skipped contract reads as green.
	if _, err := exec.LookPath("codex"); err != nil {
		t.Fatalf("codex not on PATH: %v", err)
	}
	contractenv.Isolate(t)
	// The code request needs the network, so a CLI that exited without output is retried. An
	// attempt succeeds only with the production scrape's first URL match being the verification
	// page AND a code printed: a failed request prints an API URL on the same host, which
	// matches urlRe but is neither.
	f, got := contractenv.Start(t,
		func() (*agents.Flow, error) { return agents.StartFlow(deviceLoginCmd(t.TempDir())) },
		func(f *agents.Flow) string {
			got := f.WaitFor(urlRe, 20*time.Second)
			if !isVerificationURL(got) || f.WaitFor(codeRe, 8*time.Second) == "" {
				return ""
			}
			return got
		})
	if got == "" {
		t.Fatalf("no verification URL and one-time code within 28s; CLI output:\n%s", f.Clean())
	}
}

// isVerificationURL reports whether u is the device verification page (measured:
// https://auth.openai.com/codex/device).
func isVerificationURL(u string) bool {
	p, err := url.Parse(u)
	return err == nil && p.Scheme == "https" && p.Host == "auth.openai.com" && strings.HasPrefix(p.Path, "/codex/device")
}
