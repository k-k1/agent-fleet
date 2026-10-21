//go:build contract

// Drift detection for agy's lifecycle hooks, the sibling of agy_models_drift_test.go. The
// `contract` build tag keeps it out of a normal `go test ./...`: it needs the real agy binary
// and a real sign-in, because it runs one model turn that calls a tool.
//
// What it covers is the hooks.json entry the Agent installs (agy.EnsureHooks) and what the
// hook prints. Every other agy contract runs without our hooks, so none of them noticed that
// on 1.4.4 a PreToolUse hook printing "{}" made agy deny every tool call with an empty reason:
// the session started, the model answered, and no tool ever ran (#2470).
//
// The real entry is written into an isolated HOME that shares only the login, pointing at a
// thin wrapper around the real workspace-agent that records each event. The turn asks for one
// shell command; the contract is that the command ran and the PreToolUse event reached the
// wrapper.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
)

func TestDriftAgyToolHooks(t *testing.T) {
	needBin(t, "agy")
	if !agy.SignedIn() {
		if os.Getenv("E2E_REQUIRE") == "1" {
			t.Fatal("agy is not signed in (E2E_REQUIRE=1 requires the real credential; the probe runs a model turn)")
		}
		t.Skip("agy is not signed in — the probe needs a real token")
	}

	home := t.TempDir()
	tokenDir := filepath.Join(home, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(tokenDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(agy.TokenPath(), filepath.Join(tokenDir, "antigravity-oauth-token")); err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	events := filepath.Join(work, "events")
	// The real hook binary, so what agy reads back is what production prints.
	bin := filepath.Join(work, "workspace-agent")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	stub := filepath.Join(work, "stub-hook.sh")
	script := "#!/bin/sh\necho \"$2\" >> '" + events + "'\nexec '" + bin + "' \"$@\"\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", home) // EnsureHooks resolves ~/.gemini from HOME
	if err := agy.EnsureHooksFor(stub); err != nil {
		t.Fatalf("EnsureHooksFor: %v", err)
	}

	ran := filepath.Join(work, "tool-ran")
	cmd := exec.Command("agy", "--dangerously-skip-permissions", "--print",
		"Run exactly this shell command and nothing else: touch '"+ran+"'. Then reply with the word done.")
	cmd.Dir = work
	cmd.Env = agy.Env(os.Environ())
	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() { out, runErr = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(4 * time.Minute):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("agy did not finish within 4m: %s", out)
	}
	t.Logf("agy exit: %v\noutput: %s", runErr, out)

	if _, err := os.Stat(ran); err != nil {
		t.Errorf("the shell command never ran — agy refused the tool call (1.4.4 denied every one with an empty reason when the PreToolUse hook printed {}, #2470): %s", strings.TrimSpace(string(out)))
	}
	b, _ := os.ReadFile(events)
	if !strings.Contains(string(b), "PreToolUse") {
		t.Errorf("the PreToolUse hook never fired; events seen: %q", string(b))
	}
}
