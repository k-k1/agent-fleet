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
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/contractenv"
)

func TestContractKiroSignInURL(t *testing.T) {
	// A missing CLI is a failure, never a skip: a skipped contract reads as green.
	if _, err := exec.LookPath(bin()); err != nil {
		t.Fatalf("%s not on PATH: %v", bin(), err)
	}
	contractenv.Isolate(t)
	// The device-code request needs the network, so a CLI that exited without output is retried.
	// An attempt succeeds only when the first URL match is a device sign-in URL: a failed request
	// can print a diagnostic URL that matches loginURLRe.
	f, got := contractenv.Start(t,
		func() (*agents.Flow, error) { return agents.StartFlow(loginCmd(t.TempDir())) },
		func(f *agents.Flow) string {
			got := loginURLRe.FindString(f.WaitFor(loginURLRe, 20*time.Second))
			if !isDeviceURL(got) {
				return ""
			}
			return got
		})
	if got == "" {
		t.Fatalf("no device sign-in URL within 20s; CLI output:\n%s", f.Clean())
	}
}

// isDeviceURL reports whether s is a device sign-in URL: the IAM Identity Center start page
// (measured) or the OIDC device endpoint, with the one-time code in the fragment or query.
func isDeviceURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" {
		return false
	}
	okHost := u.Host == "view.awsapps.com" ||
		(strings.HasPrefix(u.Host, "device.sso.") && strings.HasSuffix(u.Host, ".amazonaws.com"))
	return okHost && strings.Contains(u.Path+u.Fragment, "device") &&
		strings.Contains(u.RawQuery+u.Fragment, "user_code=")
}
