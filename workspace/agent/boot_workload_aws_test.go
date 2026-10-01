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
	listen := strings.Index(body, "net.Listen(")
	if iso < 0 || listen < 0 || iso > listen {
		t.Fatalf("serve() must call awsx.IsolateWorkloadChain() before anything else (isolate at %d, listen at %d)", iso, listen)
	}
}
