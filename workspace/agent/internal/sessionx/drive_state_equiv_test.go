package sessionx

// Behavioural half of the LiveStateTraits equivalence proof: DriveState against a verbatim copy
// of itself from before the traits, over kind x driver x stored state x the kind's own verdict x
// rate limit x heal.

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// Stubs both DriveState copies call in place of the kinds' own verdicts.
var (
	stubLiveState string
	stubLimited   bool
)

func stubState(session.Meta) string { return stubLiveState }
func stubRateLimited(string) bool   { return stubLimited }

// editedLiveAgent keeps a registered agent's terminal traits and passes its live-state traits
// through edit.
type editedLiveAgent struct {
	agents.Agent
	edit func(*agents.LiveStateTraits)
}

func (e editedLiveAgent) TerminalTraits() agents.TerminalTraits {
	if t, ok := e.Agent.(agents.TerminalTraiter); ok {
		return t.TerminalTraits()
	}
	return agents.TerminalTraits{}
}

// editedLiveLimitAgent is editedLiveAgent for a kind that also has usage-limit traits, which it
// passes through: the capability must stay absent for a kind that lacks it, so it cannot be a
// method of editedLiveAgent itself.
type editedLiveLimitAgent struct{ editedLiveAgent }

func (e editedLiveLimitAgent) UsageLimitTraits() agents.UsageLimitTraits {
	return e.Agent.(agents.UsageLimiter).UsageLimitTraits()
}

func (e editedLiveAgent) LiveStateTraits() agents.LiveStateTraits {
	var tr agents.LiveStateTraits
	if l, ok := e.Agent.(agents.LiveStater); ok {
		tr = l.LiveStateTraits()
	}
	e.edit(&tr)
	return tr
}

// editLiveStateTraits registers kind's agent with its live-state traits passed through edit,
// until the test ends: how a test stands in for a kind's store or live handle.
func editLiveStateTraits(t *testing.T, kind string, edit func(*agents.LiveStateTraits)) {
	t.Helper()
	e := editedLiveAgent{agentreg.Of(kind), edit}
	var a agents.Agent = e
	if _, ok := e.Agent.(agents.UsageLimiter); ok {
		a = editedLiveLimitAgent{e}
	}
	t.Cleanup(agentreg.SwapForTest(kind, a))
}

// stubVerdicts replaces a kind's own verdicts, where it has them, with the stubs.
func stubVerdicts(tr *agents.LiveStateTraits) {
	if tr.State != nil {
		tr.State = stubState
	}
	if tr.RateLimited != nil {
		tr.RateLimited = stubRateLimited
	}
}

// driveStateBefore is DriveState as it was before LiveStateTraits, verbatim but for the kinds'
// own verdicts, which call the stubs. Comments are dropped.
func driveStateBefore(m session.Meta, alive, heal bool) string {
	if !alive {
		return "stopped"
	}
	if m.Kind == session.KindOpencode {
		if st := stubState(m); st != "" {
			return st
		}
	}
	if m.Kind == session.KindAgy {
		if st := stubState(m); st != "" {
			notifyPolledTurnEnd(m, st)
			return st
		}
		sid := session.UUID(m.Dir, m.Name)
		if status.LiveState(sid) == "working" && !status.RecentlyWorking(sid) {
			return "idle"
		}
	}
	if m.Kind == session.KindCopilot {
		if st := stubState(m); st != "" {
			notifyPolledTurnEnd(m, st)
			return st
		}
	}
	if m.Kind == session.KindCursor {
		if st := stubState(m); st != "" {
			notifyPolledTurnEnd(m, st)
			return st
		}
	}
	if m.Kind == session.KindKiro {
		if st := stubState(m); st != "" {
			notifyPolledTurnEnd(m, st)
			return st
		}
	}
	if m.Kind == session.KindMuse {
		if st := stubState(m); st != "" {
			return st
		}
	}
	sid := session.UUID(m.Dir, m.Name)
	state := status.EffectiveModal(sid, status.LiveState(sid))
	exact := terminalTraits(m.Kind)
	if exact.MissedTurnEnd != nil && state == "working" && exact.MissedTurnEnd(m) {
		state = "idle"
	}
	if exact.TerminalQuestion != nil && state == "working" && exact.TerminalQuestion(m) != "" {
		return "question"
	}
	t := normalizedTerminalTraits(m.Kind)
	usageLimits := t.UsageLimitMenu != nil
	var pane tmuxx.PaneRead
	if heal || usageLimits {
		pane = readPane(m.Name)
	}
	if callTrue0(t.AuthExpired) {
		if heal && state != "idle" {
			healIdle(t, sid)
		}
		return agents.StateAuth
	}
	if usageLimits && pane.RateLimitMenu {
		if heal && state != "idle" {
			healIdle(t, sid)
		}
		return agents.StateBlocked
	}
	if m.DriverKind() == session.DriverManaged && NormalizeKind(m.Kind) == session.KindCodex && state == "idle" && stubRateLimited(m.Name) {
		return agents.StateLimited
	}
	if m.Kind == session.KindMuse && state == "idle" && stubRateLimited(m.Name) {
		return agents.StateLimited
	}
	if heal && state != "idle" && pane.IdleSettled {
		state = "idle"
		healIdle(t, sid)
	} else if heal && state == "idle" && pane.Busy && claude.ReopenFromPane(sid) {
		state = "working"
	}
	if usageLimits && alive && state == "idle" {
		if limited, _, waiting := rateLimitWaiting(m, time.Now()); waiting {
			return limited
		}
	}
	return state
}

// TestDriveStateMatchesBeforeLiveStateTraits runs both copies on the same fixtures and compares
// the answer and the status store left behind. The stored state "working-old" is a "working"
// past the warm-up, which agy's stale rule turns idle.
func TestDriveStateMatchesBeforeLiveStateTraits(t *testing.T) {
	isolateAgentState(t)
	prevPane := readPane
	readPane = func(string) tmuxx.PaneRead { return tmuxx.PaneRead{} }
	t.Cleanup(func() { readPane = prevPane })
	for _, k := range RegisteredKinds() {
		editLiveStateTraits(t, k, stubVerdicts)
	}
	dir := t.TempDir()
	prime := func(sid, stored string) {
		status.Remove(sid)
		switch stored {
		case "":
		case "working-old":
			status.Persist(sid, "working")
			old := time.Now().Add(-time.Hour)
			if err := os.Chtimes(statusPath(sid), old, old); err != nil {
				t.Fatal(err)
			}
		default:
			status.Persist(sid, stored)
		}
	}
	answers := map[string]bool{}
	cases := 0
	for _, k := range append(RegisteredKinds(), "") {
		for _, driver := range []string{session.DriverTUI, session.DriverManaged} {
			for _, stored := range []string{"", "working", "working-old", "idle"} {
				for _, own := range []string{"", "working", "idle", "question"} {
					for _, limited := range []bool{false, true} {
						for _, heal := range []bool{true, false} {
							cases++
							m := session.Meta{Name: fmt.Sprintf("lst%d", cases), Dir: dir, Kind: k, Driver: driver}
							sid := session.UUID(m.Dir, m.Name)
							stubLiveState, stubLimited = own, limited
							prime(sid, stored)
							want := driveStateBefore(m, true, heal)
							wantAfter := status.LiveState(sid)
							prime(sid, stored)
							got := DriveState(m, true, heal)
							gotAfter := status.LiveState(sid)
							if got != want || gotAfter != wantAfter {
								t.Errorf("kind %q driver %s stored %q own %q limited %v heal %v: DriveState = %q (status %q), before = %q (status %q)",
									k, driver, stored, own, limited, heal, got, gotAfter, want, wantAfter)
							}
							answers[want] = true
						}
					}
				}
			}
		}
	}
	// The fixtures prove something only if they reach the branches in question.
	for _, a := range []string{"question", "idle", "working", agents.StateLimited} {
		if !answers[a] {
			t.Errorf("no fixture answered %q", a)
		}
	}
	t.Logf("%d cases", cases)
}
