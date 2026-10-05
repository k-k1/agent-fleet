package main

import (
	"os"
	"strings"
	"testing"
)

// serve() must drop the workload identity before it starts anything, because every
// session, terminal and helper inherits the Agent's environment from the moment it exists.
// A call moved below the first spawn still passes the awsx tests, so pin the order here.
func TestServeIsolatesWorkloadChainFirst(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	body := src[strings.Index(src, "func serve() {"):]
	iso := strings.Index(body, "awsx.IsolateWorkloadChain()")
	active := strings.Index(body, "if awsx.IsolationActive() {")
	listen := strings.Index(body, "net.Listen(")
	if iso < 0 || listen < 0 || iso > listen {
		t.Fatalf("serve() must call awsx.IsolateWorkloadChain() before anything else (isolate at %d, listen at %d)", iso, listen)
	}
	// A pre-existing tmux server does not take the Agent's environment; the launch patch
	// has to be armed in the same place.
	if tm := strings.Index(body, "tmuxx.SetLaunchEnv(awsx.WorkloadChainVars(), []string{awsx.MetadataDisabled})"); active < iso || tm < active || tm > listen {
		t.Fatalf("serve() must arm tmuxx.SetLaunchEnv right after isolating (at %d)", tm)
	}
}
