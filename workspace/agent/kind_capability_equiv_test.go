package main

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TestKeepsLocalTranscriptMatchesOldBranch pins the trash's transcript gate to the old
// `m.Kind == session.KindClaude` over every registered kind plus an empty and an unknown one.
func TestKeepsLocalTranscriptMatchesOldBranch(t *testing.T) {
	kinds := append(agentreg.Kinds(), "", "no-such-kind")
	for _, k := range kinds {
		m := session.Meta{Kind: k}
		if got, want := keepsLocalTranscript(m), k == session.KindClaude; got != want {
			t.Errorf("kind %q: keepsLocalTranscript = %v, old branch = %v", k, got, want)
		}
	}
}

// TestStopBackgroundBusyerKinds pins which kinds answer the stop-after-turn background check;
// the old switch had exactly claude, codex and muse.
func TestStopBackgroundBusyerKinds(t *testing.T) {
	want := map[string]bool{session.KindClaude: true, session.KindCodex: true, session.KindMuse: true}
	for _, k := range agentreg.Kinds() {
		a, _ := agentreg.Lookup(k)
		_, got := a.(agents.StopBackgroundBusyer)
		if got != want[k] {
			t.Errorf("kind %q: implements StopBackgroundBusyer = %v, want %v", k, got, want[k])
		}
	}
}

// TestPart2CapabilityKinds pins which kinds carry the capabilities that replaced the
// session_status.go and PaneKind kind branches: launch preparation was claude only, managed
// abort signals codex and opencode, and the pane sniff knew opencode, codex and claude.
func TestPart2CapabilityKinds(t *testing.T) {
	launch := map[string]bool{session.KindClaude: true}
	abort := map[string]bool{session.KindCodex: true, session.KindOpencode: true}
	pane := map[string]string{session.KindClaude: "claude", session.KindCodex: "codex", session.KindOpencode: "opencode"}
	for _, k := range agentreg.Kinds() {
		a, _ := agentreg.Lookup(k)
		_, gotLaunch := a.(agents.LaunchPreparer)
		if gotLaunch != launch[k] {
			t.Errorf("kind %q: implements LaunchPreparer = %v, want %v", k, gotLaunch, launch[k])
		}
		gotAbort := false
		if c, ok := a.(agents.ManagedAbortSignaler); ok {
			gotAbort = c.ManagedAbortSignals()
		}
		if gotAbort != abort[k] {
			t.Errorf("kind %q: ManagedAbortSignals = %v, want %v", k, gotAbort, abort[k])
		}
		gotPane := ""
		if c, ok := a.(agents.PaneStartMarker); ok {
			gotPane = c.PaneStartMarker()
		}
		if gotPane != pane[k] {
			t.Errorf("kind %q: PaneStartMarker = %q, want %q", k, gotPane, pane[k])
		}
	}
}
