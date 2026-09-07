//go:build clicontract

// A hand-driven agy OAuth login: the one step of the Connections flow that cannot be
// automated, because completing it needs a human with a browser.
//
// It drives the SAME steps HandleStart/HandleComplete do, through the same helpers and the
// same environment (fips.go's mask included), so a pass is evidence for the product's own
// flow — which is how the RDRAND workaround was first shown to carry a whole login on a host
// where ADR 0008 had declared agy unrunnable.
//
//	AF_AGY_LOGIN=1 go test -tags clicontract -run TestAgyLoginByHand -timeout 20m ./internal/agents/agy/
//
// It needs a human: the URL is written to <dir>/url.txt, and the flow waits for the
// authorization code to appear in <dir>/code.txt.
package agy

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"os/exec"
)

const agyLoginDir = "/tmp/agy-login"

func TestAgyLoginByHand(t *testing.T) {
	if os.Getenv("AF_AGY_LOGIN") != "1" {
		t.Skip("AF_AGY_LOGIN!=1 — this one waits on a human")
	}
	if SignedIn() {
		t.Skip("already signed in")
	}
	if err := os.MkdirAll(agyLoginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(agyLoginDir, "url.txt"))
	_ = os.Remove(filepath.Join(agyLoginDir, "code.txt"))

	loginDir := filepath.Join(stateDir(), "login-flow")
	if err := os.MkdirAll(loginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	EnsureWorkspaceTrusted(loginDir)

	cmd := exec.Command("agy")
	cmd.Dir = loginDir
	cmd.Env = Env(append(os.Environ(), "TERM=xterm-256color"))
	f, err := agents.StartFlow(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if f.WaitFor(selectorRe, 30*time.Second) == "" {
		t.Fatal("agy did not show the sign-in method selector (the mask did not take?)")
	}
	if _, err := f.Ptmx.Write([]byte(keyEnter)); err != nil {
		t.Fatal(err)
	}
	url := sanitizeAuthURL(f.WaitFor(urlRe, 30*time.Second))
	if url == "" {
		t.Fatal("agy printed no authorization URL")
	}
	if err := os.WriteFile(filepath.Join(agyLoginDir, "url.txt"), []byte(url+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("AUTHORIZE URL WRITTEN: %s/url.txt", agyLoginDir)

	code := waitForCodeFile(t, filepath.Join(agyLoginDir, "code.txt"), 15*time.Minute)
	if _, err := f.Ptmx.Write([]byte(code)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := f.Ptmx.Write([]byte(keyEnter)); err != nil {
		t.Fatal(err)
	}
	if err := driveOnboarding(f, 120*time.Second); err != nil {
		t.Fatalf("login did not complete: %v", err)
	}
	enforceTelemetryOff()
	if !SignedIn() {
		t.Fatal("onboarding finished but no token landed")
	}
	t.Logf("SIGNED IN: token at %s", tokenPath())
}

func waitForCodeFile(t *testing.T, path string, budget time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if s := string(trimSpaceBytes(b)); s != "" {
				return s
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("no code appeared at %s within %s", path, budget)
	return ""
}

func trimSpaceBytes(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	for len(b) > 0 && (b[0] == '\n' || b[0] == '\r' || b[0] == ' ') {
		b = b[1:]
	}
	return b
}
