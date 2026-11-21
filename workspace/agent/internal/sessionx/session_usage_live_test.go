package sessionx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// liveStub is a kind with a live handle: the real agent with LiveContext answered by the test.
type liveStub struct {
	agents.Agent
	lc agents.LiveContext
	ok bool
}

func (s liveStub) LiveContext(string) (agents.LiveContext, bool) { return s.lc, s.ok }

func TestOverlayLiveUsage(t *testing.T) {
	zero, five := 0.0, 5.0
	base, _ := agentreg.Lookup(session.KindKiro)
	cases := []struct {
		name        string
		lc          agents.LiveContext
		ok          bool
		wantCtx     bool
		wantCredits float64
	}{
		{"no live handle leaves usage unchanged", agents.LiveContext{}, false, false, 7},
		{"metered credits replace, even a live 0", agents.LiveContext{Tokens: 10, Window: 100, Pct: 10, Credits: &zero}, true, true, 0},
		{"a reported figure replaces", agents.LiveContext{Tokens: 10, Window: 100, Pct: 10, Credits: &five}, true, true, 5},
		{"an unmetered kind keeps the existing credits", agents.LiveContext{Tokens: 10, Window: 100, Pct: 10}, true, true, 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer agentreg.SwapForTest(session.KindKiro, liveStub{Agent: base, lc: c.lc, ok: c.ok})()
			var u sessionUsage
			u.Cumulative.Credits = 7
			overlayLiveUsage(session.Meta{Kind: session.KindKiro, Name: "n"}, &u)
			if (u.Context != nil) != c.wantCtx {
				t.Fatalf("context = %v, want present=%v", u.Context, c.wantCtx)
			}
			if u.Cumulative.Credits != c.wantCredits {
				t.Errorf("credits = %v, want %v", u.Cumulative.Credits, c.wantCredits)
			}
			if c.wantCtx && (u.Context.Tokens != c.lc.Tokens || u.Context.Fresh != c.lc.Tokens ||
				u.Context.Window != c.lc.Window || u.Context.Pct != c.lc.Pct) {
				t.Errorf("context = %+v, want the live figures %+v", u.Context, c.lc)
			}
		})
	}
}
