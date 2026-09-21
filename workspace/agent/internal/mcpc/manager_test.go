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

// TestManager_SyncReconnectsOnDefChange guards the fix for a review finding: editing a
// registered server's definition in place (same Name, different Command/Env/whatever)
// must tear down the old connection rather than leave it serving stale configuration.
func TestManager_SyncReconnectsOnDefChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ctx)
	defer func() { _ = m.Close() }()

	defA := fakeStdioDef(t, "a", "modern")
	if errs := m.Sync(context.Background(), []mcpreg.ServerDef{defA}); len(errs) != 0 {
		t.Fatalf("Sync errors: %+v", errs)
	}
	m.mu.Lock()
	first := m.servers["a"]
	m.mu.Unlock()

	defA2 := defA
	defA2.Label = "changed" // any field differing is enough to trigger a reconnect

	if errs := m.Sync(context.Background(), []mcpreg.ServerDef{defA2}); len(errs) != 0 {
		t.Fatalf("Sync errors: %+v", errs)
	}
	m.mu.Lock()
	second := m.servers["a"]
	m.mu.Unlock()

	if first == second {
		t.Fatal("Sync should have replaced the *Server on a definition change, not left the old one")
	}
	if len(m.ToolDefs()) != 3 {
		t.Fatalf("the reconnected server should still serve its tools: %+v", m.ToolDefs())
	}
	// Sync must have closed the OLD connection synchronously, not merely orphaned it —
	// a call against it should now fail client-side (stdio.go's call() refuses once closed).
	if _, _, err := first.CallTool(context.Background(), "echo", []byte(`{"msg":"x"}`)); err == nil {
		t.Fatal("the replaced server's old connection should already be closed after Sync returned")
	}
}

// TestManager_SyncIsANoOpWhenDefsAreUnchanged is the negative control
// TestManager_SyncReconnectsOnDefChange was missing: a repeated Sync call with the SAME defs
// (the exact shape a caller like lcpp's own runTurn makes every single turn — mcpreg.ForSession
// returns a stable value across turns for an unchanged registry) must not tear down and
// reconnect anything. Without this, "Sync compares defs" and "Sync always reconnects" are
// indistinguishable from outside this package, and a caller that calls Sync every turn would
// look like it re-dials (execs) every server every turn when it does not.
func TestManager_SyncIsANoOpWhenDefsAreUnchanged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ctx)
	defer func() { _ = m.Close() }()

	defA := fakeStdioDef(t, "a", "modern")
	if errs := m.Sync(context.Background(), []mcpreg.ServerDef{defA}); len(errs) != 0 {
		t.Fatalf("Sync errors: %+v", errs)
	}
	m.mu.Lock()
	first := m.servers["a"]
	m.mu.Unlock()

	// Same def, a fresh slice (the caller's own repeated ForSession call would not hand back
	// the identical slice value either) — repeated 3 times, matching several turns in a row.
	for i := 0; i < 3; i++ {
		if errs := m.Sync(context.Background(), []mcpreg.ServerDef{defA}); len(errs) != 0 {
			t.Fatalf("Sync errors (call %d): %+v", i, errs)
		}
	}

	m.mu.Lock()
	second := m.servers["a"]
	m.mu.Unlock()
	if first != second {
		t.Fatal("Sync reconnected a server whose definition never changed — it should have been a pure no-op")
	}
	// A stronger check than pointer identity alone: if Sync had actually closed and
	// re-dialed, the OLD connection (now closed) would refuse a call the same way
	// TestManager_SyncReconnectsOnDefChange's own final assertion checks.
	if _, _, err := first.CallTool(context.Background(), "echo", []byte(`{"msg":"still-alive"}`)); err != nil {
		t.Fatalf("the untouched connection should still be usable: %v", err)
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
