package mcpreg

import (
	"context"
	"testing"
	"time"
)

// The stdio probe reports the server's stderr tail as Detail. os/exec copies a non-file
// Stderr in a goroutine of its own until Wait returns, while the probe reads it on its
// failure paths with the server possibly still running; run these under -race.

// Golden for the text of an exit failure: the user-visible Error and the stderr tail.
func TestProbeStdioExitReportsStderrTail(t *testing.T) {
	d := stdioDef("exits-loudly")
	d.Command = "sh"
	// The sleep orders the stderr line before the stdout EOF that triggers the failure.
	d.Args = []string{"-c", "echo boom >&2; read line; sleep 0.2; exit 0"}
	res := Probe(context.Background(), d)
	if res.Code != codeProcessExited || res.Error != "no response (the process exited)" || res.Detail != "boom" {
		t.Fatalf("got %+v", res)
	}
}

// A rejection is reported while the server is alive and still writing stderr.
func TestProbeStdioRejectWhileWritingStderr(t *testing.T) {
	d := stdioDef("noisy")
	d.Command = "sh"
	d.Args = []string{"-c", `read line; echo '{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"nope"}}'; ` +
		`i=0; while [ $i -lt 20000 ]; do echo noise >&2; i=$((i+1)); done`}
	for i := 0; i < 5; i++ {
		res := Probe(context.Background(), d)
		if res.Code != codeRPCRejected || res.Error != "server/discover was rejected: nope" {
			t.Fatalf("got %+v", res)
		}
	}
}

// A grandchild that inherits stderr outlives the killed server (npx -> node is the usual
// shape). Wait copies stderr until EOF, so without a bound the probe never returns.
func TestProbeStdioGrandchildHoldingStderrDoesNotHang(t *testing.T) {
	d := stdioDef("forks")
	d.Command = "sh"
	d.Args = []string{"-c", "(sleep 20 >/dev/null) & read line; exit 0"}
	got := make(chan ProbeResult, 1)
	go func() { got <- Probe(context.Background(), d) }()
	select {
	case res := <-got:
		if res.Code != codeProcessExited {
			t.Fatalf("got %+v", res)
		}
	case <-time.After(probeWaitDelay + 5*time.Second):
		t.Fatal("probe still waiting on the grandchild's stderr")
	}
}
