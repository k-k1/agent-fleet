// Contract test for the "would a stop/start run different code" verdict. A false
// positive leaves a "restart needed" badge on the WS bar that nothing can clear, and a
// miss hides an update that never took effect. When in doubt, answer not stale.
package main

import (
	"context"
	"testing"
)

// staleStubRuntime is a Runtime whose Stale answer is fixed by the test, for exercising
// workspacePayload.
type staleStubRuntime struct {
	stubRuntime
	state string
	stale bool
}

func (s staleStubRuntime) State(context.Context) string { return s.state }
func (s staleStubRuntime) Stale(context.Context) bool   { return s.stale }

func TestWorkspacePayloadStale(t *testing.T) {
	a := workspaceAPI{}
	ctx := context.Background()

	m := a.workspacePayload(ctx, &resolved{rt: staleStubRuntime{state: "running", stale: true}}, "running")
	if m["stale"] != true {
		t.Fatalf("running+stale: stale = %v, want true", m["stale"])
	}

	// A stopped workspace comes up fresh anyway, so don't show a badge nobody can act on.
	m = a.workspacePayload(ctx, &resolved{rt: staleStubRuntime{state: "none", stale: true}}, "none")
	if _, ok := m["stale"]; ok {
		t.Fatalf("stopped: stale present (%v), want absent", m["stale"])
	}

	// With no drift the key is absent entirely, keeping the existing payload shape.
	m = a.workspacePayload(ctx, &resolved{rt: staleStubRuntime{state: "running", stale: false}}, "running")
	if _, ok := m["stale"]; ok {
		t.Fatalf("fresh: stale present (%v), want absent", m["stale"])
	}
}
