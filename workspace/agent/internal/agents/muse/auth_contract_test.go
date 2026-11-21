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
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/contractenv"
)

func TestContractMuseSignInURL(t *testing.T) {
	// A missing CLI is a failure, never a skip: a skipped contract reads as green.
	if _, err := exec.LookPath(Bin()); err != nil {
		t.Fatalf("%s not on PATH: %v", Bin(), err)
	}
	contractenv.Isolate(t)
	// The device-code request needs the network, so a CLI that exited without output is retried.
	// An attempt succeeds only when the first URL match is a device sign-in URL: a failed request
	// can print a diagnostic URL that matches loginURLRe.
	f, got := contractenv.Start(t,
		func() (*agents.Flow, error) { return agents.StartPipeFlow(loginCmd(t.TempDir())) },
		func(f *agents.Flow) string {
			got := f.WaitFor(loginURLRe, 20*time.Second)
			if !isDeviceURL(got) {
				return ""
			}
			return got
		})
	if got == "" {
		t.Fatalf("no device sign-in URL within 20s; CLI output:\n%s", f.Clean())
	}
}

// isDeviceURL reports whether s is a device sign-in URL (measured:
// https://auth.meta.com/oauth/device/?code=XXXX-XXXX).
func isDeviceURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host == "auth.meta.com" &&
		strings.HasPrefix(u.Path, "/oauth/device") && u.Query().Get("code") != ""
}
