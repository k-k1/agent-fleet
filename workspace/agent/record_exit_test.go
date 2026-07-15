package main

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

func TestExitReason(t *testing.T) {
	cases := []struct {
		name string
		code int
		oom  bool
		want string
	}{
		{"normal quit", 0, false, "exited"},
		{"oom kill", 137, true, "oom"},           // SIGKILL + oom_kill advanced this session
		{"sigkill no oom", 137, false, "killed"}, // SIGKILL but the counter didn't move → not an OOM
		{"segfault", 139, false, "crashed"},      // SIGSEGV = application fault
		{"abort", 134, false, "crashed"},         // SIGABRT
		{"nonzero exit", 1, false, "crashed"},    // CLI exited non-zero (no signal)
		{"sigterm", 143, false, "stopped"},       // graceful termination
		{"sigint", 130, false, "stopped"},        // Ctrl-C
		{"sighup", 129, false, "stopped"},        // hangup
	}
	for _, c := range cases {
		sig := 0
		if c.code >= 128 {
			sig = c.code - 128
		}
		if got := status.ExitReasonFor(c.code, sig, c.oom); got != c.want {
			t.Errorf("%s: status.ExitReasonFor(%d, %d, %v) = %q, want %q", c.name, c.code, sig, c.oom, got, c.want)
		}
	}
}

// An OOM claim requires the SIGKILL signal, not just the oom flag — a process that
// exits some other way while the container happens to have OOMed elsewhere is not "oom".
func TestExitReasonOOMNeedsSigkill(t *testing.T) {
	if got := status.ExitReasonFor(1, 0, true); got != "crashed" {
		t.Errorf("nonzero exit with oom flag = %q, want crashed", got)
	}
	if got := status.ExitReasonFor(139, 11, true); got != "crashed" {
		t.Errorf("segfault with oom flag = %q, want crashed", got)
	}
}
