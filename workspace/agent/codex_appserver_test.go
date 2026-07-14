package main

import (
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
)

func TestConnectCodexAppServerIntegration(t *testing.T) {
	addr := os.Getenv("AF_TEST_CODEX_APP_SERVER_ADDR")
	if addr == "" {
		t.Skip("set AF_TEST_CODEX_APP_SERVER_ADDR to run against a real Codex app-server")
	}
	conn, err := connectCodexAppServer(addr)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestStartCodexAppServerDisabledClearsRemoteAddress(t *testing.T) {
	t.Setenv("AF_CODEX_APP_SERVER_DISABLE", "1")
	t.Setenv(codexAppServerEnv, "ws://127.0.0.1:1")
	startCodexAppServer()
	if got := os.Getenv(codexAppServerEnv); got != "" {
		t.Fatalf("app-server address = %q; want empty in disabled mode", got)
	}
}

func TestHandleCodexAppServerCompactionLifecycle(t *testing.T) {
	codex.ClearCompacting()
	t.Cleanup(codex.ClearCompacting)

	handleCodexAppServerEvent([]byte(`{
      "method":"item/started",
      "params":{"threadId":"thr-1","turnId":"turn-1","item":{"type":"contextCompaction","id":"item-1"}}
    }`))
	if !codex.IsCompactingThread("thr-1") {
		t.Fatal("contextCompaction item/started did not set compacting")
	}

	handleCodexAppServerEvent([]byte(`{
      "method":"item/completed",
      "params":{"threadId":"thr-1","turnId":"turn-1","item":{"type":"contextCompaction","id":"item-1"}}
    }`))
	if codex.IsCompactingThread("thr-1") {
		t.Fatal("contextCompaction item/completed did not clear compacting")
	}
}

func TestHandleCodexAppServerTurnCompletedClearsStuckCompaction(t *testing.T) {
	codex.ClearCompacting()
	t.Cleanup(codex.ClearCompacting)
	codex.SetCompacting("thr-1", true)

	handleCodexAppServerEvent([]byte(`{
      "method":"turn/completed",
      "params":{"threadId":"thr-1","turn":{"id":"turn-1","status":"failed"}}
    }`))
	if codex.IsCompactingThread("thr-1") {
		t.Fatal("turn/completed did not clear compacting")
	}
}

func TestHandleCodexAppServerIgnoresOtherItems(t *testing.T) {
	codex.ClearCompacting()
	t.Cleanup(codex.ClearCompacting)
	handleCodexAppServerEvent([]byte(`{
      "method":"item/started",
      "params":{"threadId":"thr-1","item":{"type":"commandExecution","id":"item-1"}}
    }`))
	if codex.IsCompactingThread("thr-1") {
		t.Fatal("non-compaction item changed compacting state")
	}
}
