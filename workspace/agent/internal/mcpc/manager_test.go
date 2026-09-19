package mcpc

import (
	"context"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

func TestManager_MergesAndRoutesAcrossServers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ctx)
	defer func() { _ = m.Close() }()

	defA := fakeStdioDef(t, "a", "modern")
	defB := fakeStdioDef(t, "b", "legacy")
	if errs := m.Sync(context.Background(), []mcpreg.ServerDef{defA, defB}); len(errs) != 0 {
		t.Fatalf("Sync errors: %+v", errs)
	}

	defs := m.ToolDefs()
	if len(defs) != 6 { // 3 tools x 2 servers
		t.Fatalf("ToolDefs = %d entries, want 6: %+v", len(defs), defs)
	}

	text, isErr, err := m.CallTool(context.Background(), PrefixToolName("a", "echo"), rawArgs(`{"msg":"from-a"}`))
	if err != nil || isErr || text != "from-a" {
		t.Fatalf("CallTool routed to a: (%q, %v, %v)", text, isErr, err)
	}
	text, isErr, err = m.CallTool(context.Background(), PrefixToolName("b", "echo"), rawArgs(`{"msg":"from-b"}`))
	if err != nil || isErr || text != "from-b" {
		t.Fatalf("CallTool routed to b: (%q, %v, %v)", text, isErr, err)
	}
}

func TestManager_SyncRemovesAndClosesDroppedServers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ctx)
	defer func() { _ = m.Close() }()

	defA := fakeStdioDef(t, "a", "modern")
	defB := fakeStdioDef(t, "b", "modern")
	if errs := m.Sync(context.Background(), []mcpreg.ServerDef{defA, defB}); len(errs) != 0 {
		t.Fatalf("Sync errors: %+v", errs)
	}
	if len(m.ToolDefs()) != 6 {
		t.Fatalf("expected both servers attached")
	}

	if errs := m.Sync(context.Background(), []mcpreg.ServerDef{defB}); len(errs) != 0 {
		t.Fatalf("Sync errors: %+v", errs)
	}
	for _, d := range m.ToolDefs() {
		if d.Name == PrefixToolName("a", "echo") {
			t.Fatalf("server a should have been dropped and closed: %+v", m.ToolDefs())
		}
	}
	if len(m.ToolDefs()) != 3 {
		t.Fatalf("ToolDefs = %d, want 3 (b only): %+v", len(m.ToolDefs()), m.ToolDefs())
	}
}

// TestManager_ContextCancelClosesEverything is the ADR 0093 decision 6 guarantee this
// package is built around: a session context dying must not leave a stdio child behind
// even if the caller never calls Close explicitly.
func TestManager_ContextCancelClosesEverything(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := NewManager(ctx)
	if errs := m.Sync(context.Background(), []mcpreg.ServerDef{fakeStdioDef(t, "a", "modern")}); len(errs) != 0 {
		t.Fatalf("Sync errors: %+v", errs)
	}
	if len(m.ToolDefs()) == 0 {
		t.Fatal("setup: server never connected")
	}

	cancel()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if len(m.ToolDefs()) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("context cancellation never tore the manager's servers down")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestManager_CallToolUnknownName(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ctx)
	defer func() { _ = m.Close() }()
	if _, _, err := m.CallTool(context.Background(), "mcp__nope__nope", nil); err == nil {
		t.Fatal("CallTool for a name with no matching server should fail")
	}
}

func rawArgs(s string) []byte { return []byte(s) }
