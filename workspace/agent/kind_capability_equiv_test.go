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
