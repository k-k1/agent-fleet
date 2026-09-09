package session

import "testing"

// InUnattendedChain is the two-term predicate the recursion limit refuses on (ADR 0073
// decision 5). Its whole reason to exist as a named function is that the one-term spelling
// it replaced — `OriginSession != ""` — still reads as right, so the shapes that separate the
// two are pinned here.
func TestInUnattendedChain(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    Meta
		want bool
	}{
		{"a session a person opened", Meta{Origin: OriginUser}, false},
		{"a child", Meta{Origin: OriginSession, OriginSession: "parent1"}, true},
		{"a fork of a child keeps the chain", Meta{Origin: OriginHandoff, OriginSession: "parent1"}, true},
		// The shape the amendment adds, and the one the old spelling gets wrong: a person
		// launched it, so it is a re-entry however the work was suggested.
		{"a launch from a handoff proposal", Meta{Origin: OriginUser, OriginSession: "proposer"}, false},
		{"a fork of one, which inherits no lineage", Meta{Origin: OriginHandoff}, false},
		{"a plain fork", Meta{Origin: OriginHandoff}, false},
		{"an operator's session", Meta{Origin: OriginOperator, OriginConv: "c1"}, false},
		// A meta older than the feature cannot say what opened it. With a lineage present,
		// answer "unattended": when in doubt about a chain nobody is watching, refuse.
		{"a meta too old to say", Meta{OriginSession: "parent1"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := InUnattendedChain(tc.m); got != tc.want {
				t.Fatalf("InUnattendedChain(%+v) = %v, want %v", tc.m, got, tc.want)
			}
		})
	}
}
