package sessionx

// Equivalence proof for agents.LiveStateTraits: DriveState's per-kind lines and
// recordPolledTurnEnd's kind switch must select the same kinds and call the same functions
// through the capability. drive_state_equiv_test.go is the behavioural half.

import (
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// liveStateProbeKinds is every registered kind plus spellings that are no kind, which must get
// no trait (the old conditions compared exactly, or through NormalizeKind for codex).
func liveStateProbeKinds() []string {
	return append(RegisteredKinds(), "", "Claude", " codex", "Muse", "pi")
}

func ptrOf(f any) uintptr { return reflect.ValueOf(f).Pointer() }

// TestLiveStateTraitsMatchOldConditions holds each trait to the kinds the old condition selected
// (copied verbatim) and to the very function the old line called.
func TestLiveStateTraitsMatchOldConditions(t *testing.T) {
	// DriveState's State calls. muse's line called musePendingState(m.Name), which was
	// muse.PendingState; muse.LiveState is that call for m.
	oldState := map[string]any{
		session.KindOpencode: opencode.LiveState,
		session.KindAgy:      agy.LiveState,
		session.KindCopilot:  copilot.LiveState,
		session.KindCursor:   cursor.LiveState,
		session.KindKiro:     kiro.LiveState,
		session.KindMuse:     muse.LiveState,
	}
	// DriveState's notifyPolledTurnEnd lines and recordPolledTurnEnd's
	// `case session.KindAgy, session.KindCopilot, session.KindCursor, session.KindKiro:`.
	oldPolled := func(k string) bool {
		return k == session.KindAgy || k == session.KindCopilot || k == session.KindCursor || k == session.KindKiro
	}
	oldStale := func(k string) bool { return k == session.KindAgy }
	// `m.DriverKind() == session.DriverManaged && NormalizeKind(m.Kind) == session.KindCodex && ... codex.IsRateLimited`
	// and `m.Kind == session.KindMuse && ... museRateLimited`.
	oldLimited := func(k string) (fn any, managedOnly bool) {
		if NormalizeKind(k) == session.KindCodex {
			return codex.IsRateLimited, true
		}
		if k == session.KindMuse {
			return muse.IsRateLimited, false
		}
		return nil, false
	}
	n := 0
	for _, k := range liveStateProbeKinds() {
		tr := liveStateTraits(k)
		if want, ok := oldState[k]; ok {
			n++
			if tr.State == nil || ptrOf(tr.State) != ptrOf(want) {
				t.Errorf("%q: State is not the function its old line called", k)
			}
		} else if tr.State != nil {
			t.Errorf("%q: has State, the old lines selected no such kind", k)
		}
		if tr.PolledTurnEnd != oldPolled(k) {
			t.Errorf("%q: PolledTurnEnd = %v, old conditions %v", k, tr.PolledTurnEnd, oldPolled(k))
		}
		if tr.StaleWorkingIdle != oldStale(k) {
			t.Errorf("%q: StaleWorkingIdle = %v, old condition %v", k, tr.StaleWorkingIdle, oldStale(k))
		}
		fn, managedOnly := oldLimited(k)
		switch {
		case fn == nil && tr.RateLimited != nil:
			t.Errorf("%q: has RateLimited, the old lines selected no such kind", k)
		case fn != nil && (tr.RateLimited == nil || ptrOf(tr.RateLimited) != ptrOf(fn)):
			t.Errorf("%q: RateLimited is not the function its old line called", k)
		case fn != nil && tr.RateLimitedManagedOnly != managedOnly:
			t.Errorf("%q: RateLimitedManagedOnly = %v, old line %v", k, tr.RateLimitedManagedOnly, managedOnly)
		}
	}
	if n != len(oldState) {
		t.Fatalf("checked State on %d kinds, want %d: a probe kind is missing", n, len(oldState))
	}
}
