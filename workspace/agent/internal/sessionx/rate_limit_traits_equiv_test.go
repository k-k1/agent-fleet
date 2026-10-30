package sessionx

// Equivalence proof for agents.UsageLimiter: the sweep's per-kind answers against a verbatim copy
// of the kind switches they replaced, over every registered kind x driver, plus the legacy
// spellings NormalizeKind folds into claude.

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// oldRateLimitWatched is rateLimitWatched as it was before the capability.
func oldRateLimitWatched(m session.Meta) bool {
	switch NormalizeKind(m.Kind) {
	case session.KindClaude, session.KindMuse:
		return true
	case session.KindCodex:
		return m.DriverKind() == session.DriverManaged
	}
	return false
}

func TestUsageLimitTraitsMatchOldSwitch(t *testing.T) {
	kinds := append(RegisteredKinds(), "", "no-such-kind")
	checked := 0
	for _, k := range kinds {
		for _, drv := range []string{"", session.DriverTUI, session.DriverManaged} {
			m := session.Meta{Name: "ultraits-" + k, Dir: t.TempDir(), Kind: k, Driver: drv}
			if got, want := rateLimitWatched(m), oldRateLimitWatched(m); got != want {
				t.Errorf("rateLimitWatched(kind=%q driver=%q) = %v, old switch %v", k, drv, got, want)
			}
			tr, ok := usageLimitTraits(m)
			nk := NormalizeKind(k)
			// The old claude-only menu read.
			if hasMenu := ok && tr.PaneMenu != nil; hasMenu != (nk == session.KindClaude) {
				t.Errorf("kind=%q: PaneMenu present = %v, old read was claude-only", k, hasMenu)
			}
			// A kind the old switch never watched has no question to answer.
			if !ok && (tr.AtLimit != nil || tr.ResetAt != nil) {
				t.Errorf("kind=%q: answers without the capability", k)
			}
			if oldRateLimitWatched(session.Meta{Kind: k, Driver: session.DriverManaged}) != (ok && tr.AtLimit != nil && tr.ResetAt != nil) {
				t.Errorf("kind=%q: capability presence differs from the old watched population", k)
			}
			if !ok {
				if kind, at := atUsageLimit(m); kind != "" || at {
					t.Errorf("kind=%q: atUsageLimit = %q/%v without the capability", k, kind, at)
				}
				if _, _, rok := usageLimitResetAt(m, rateLimitState{}, time.Now()); rok {
					t.Errorf("kind=%q: usageLimitResetAt answered without the capability", k)
				}
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no kind was checked")
	}
}

// TestUsageLimitTraitsCallTheOldFunctions: codex and muse answer through the same functions the
// old branches called, and always report a window (never a spend cap).
func TestUsageLimitTraitsCallTheOldFunctions(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		kind    string
		limited func(string) bool
		reset   func(name string, now time.Time) (time.Time, string, bool)
	}{
		{session.KindCodex, codex.IsRateLimited, func(_ string, n time.Time) (time.Time, string, bool) { return codex.ResetAt(n) }},
		{session.KindMuse, muse.IsRateLimited, muse.ResetAt},
	} {
		m := session.Meta{Name: "ultraits-fn", Dir: t.TempDir(), Kind: tc.kind, Driver: session.DriverManaged}
		kind, at := atUsageLimit(m)
		if kind != agents.LimitWindow || at != tc.limited(m.Name) {
			t.Errorf("%s: atUsageLimit = %q/%v, old %q/%v", tc.kind, kind, at, agents.LimitWindow, tc.limited(m.Name))
		}
		gAt, gSrc, gOK := usageLimitResetAt(m, rateLimitState{Menu: true}, now)
		wAt, wSrc, wOK := tc.reset(m.Name, now)
		if !gAt.Equal(wAt) || gSrc != wSrc || gOK != wOK {
			t.Errorf("%s: usageLimitResetAt = %v/%q/%v, old %v/%q/%v", tc.kind, gAt, gSrc, gOK, wAt, wSrc, wOK)
		}
	}
}
