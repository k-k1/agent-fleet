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
	f, got := contractenv.Start(t,
		func() (*agents.Flow, error) { return agents.StartFlow(loginCmd(t.TempDir())) },
		func(f *agents.Flow) string { return loginURLRe.FindString(f.WaitFor(loginURLRe, 20*time.Second)) })
	if got == "" {
		t.Fatalf("no device URL matched loginURLRe within 20s; CLI output:\n%s", f.Clean())
	}
	u, err := url.Parse(got)
	if err != nil || u.Scheme != "https" {
		t.Fatalf("unexpected device URL %q (err %v)", got, err)
	}
	// The IAM Identity Center start page (measured) or the OIDC device endpoint; the one-time
	// code rides in the fragment or the query. Any other host is a diagnostic link, not sign-in.
	okHost := u.Host == "view.awsapps.com" ||
		(strings.HasPrefix(u.Host, "device.sso.") && strings.HasSuffix(u.Host, ".amazonaws.com"))
	if !okHost || !strings.Contains(u.Path+u.Fragment, "device") ||
		!strings.Contains(u.RawQuery+u.Fragment, "user_code=") {
		t.Fatalf("not a device sign-in URL: host %q path %q", u.Host, u.Path)
	}
}
