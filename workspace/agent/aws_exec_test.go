package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// /dev/null is a character device; a redirected run must not count as interactive.
func TestIsTerminalRejectsDevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Fatal("/dev/null was treated as a terminal")
	}
}

func TestParseAWSExecArgs(t *testing.T) {
	o, list := parseAWSExecArgs([]string{"--profile=prod", "--account", "123456789012", "--keep-aws-config",
		"--region", "eu-west-1", "-q", "--no-login", "--", "cdk", "deploy", "--profile", "other"})
	if list || o.Profile != "prod" || o.Account != "123456789012" || !o.KeepConfig || o.Region != "eu-west-1" ||
		!o.Quiet || o.Login != "never" || strings.Join(o.Argv, " ") != "cdk deploy --profile other" {
		t.Fatalf("parsed %+v list=%v", o, list)
	}
	if o, list := parseAWSExecArgs([]string{"--list"}); !list || o.Login != "auto" {
		t.Fatalf("--list: %+v %v", o, list)
	}
}

// ADR 0102 decision 5: a measured kind gets its own wait; an unmeasured kind or an unknown
// caller gets the short wait that no tool's timeout plausibly cuts.
func TestConsoleLoginWaitByKind(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	session.WriteMeta(session.Meta{Name: "c1", Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "x1", Kind: session.KindCodex})
	session.WriteMeta(session.Meta{Name: "s1", Kind: session.KindShell})
	for name, want := range map[string]time.Duration{
		"c1": 90 * time.Second, "x1": 90 * time.Second,
		"s1":     consoleLoginUnmeasuredWait,
		"nosuch": consoleLoginUnmeasuredWait, "": consoleLoginUnmeasuredWait,
	} {
		if got := consoleLoginWait(name); got != want {
			t.Errorf("%q: wait = %s, want %s", name, got, want)
		}
	}
	// docs/log/120 §1.1: every measured kind either outlasts 90 s or keeps the output.
	for _, k := range []string{session.KindClaude, session.KindCodex, session.KindOpencode,
		session.KindCopilot, session.KindCursor, session.KindKiro, session.KindMuse, session.KindAgy, session.KindLcpp} {
		if consoleLoginWaits[k] != 90*time.Second {
			t.Errorf("%s: measured wait = %s, changed without a new measurement", k, consoleLoginWaits[k])
		}
	}
}
