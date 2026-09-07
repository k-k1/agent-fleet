package chatx

// The consuming side of the "stop after this turn" arm (docs/log/85).
//
// The arm itself is a session-side fact (session.Meta.StopAfterTurnAt, armed by the MCP tool
// or the Console). What is here is only the moment it comes due, and it is here because this
// package already answers "has the turn ended" for session reports: end-of-turn marker with
// the TurnEnd bit, pending question / plan / permission, subagent and transcript freshness,
// the pane spinner, an interruption auto-resume has taken on. A stop decided from anything
// less folds a session away mid-question or mid-subagent, and a second copy of the same
// evidence table drifts from this one the first time either is touched (docs/log/75).
//
// So the arm reuses evalReportEvidence unchanged, with the arm's instant as the lower bound
// the evidence is cut by — exactly the role an instruction row's cursor plays for a report.

import (
	"log"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// stopArmSweepSessions lists the sessions currently armed. Reading the metas is the same
// readdir the ledger sweep already does per tick, and an unarmed workspace costs one listing.
func stopArmSweepSessions(now time.Time) []string {
	var out []string
	for _, m := range session.ListMetas() {
		if !session.ValidName(m.Name) || m.Archived || m.StoppedAt != "" {
			continue
		}
		if _, live := session.StopArmedAt(m, now); live {
			out = append(out, m.Name)
		}
	}
	return out
}

func (rc *reportReconciler) sweepStopArms(now time.Time) {
	armed := stopArmSweepSessions(now)
	for _, name := range armed {
		rc.evaluateStopArm(name, now)
	}
	rc.pruneStopArms(armed)
}

// evaluateStopArm is the whole decision for one armed session: is it still armed, does it owe
// a report first, has the turn ended, and has that been true for two consecutive ticks.
func (rc *reportReconciler) evaluateStopArm(name string, now time.Time) {
	m, ok := session.ReadMeta(name)
	if !ok {
		return
	}
	armedAt, live := session.StopArmedAt(m, now)
	if !live {
		rc.forgetStopArm(name)
		return
	}
	// Report first. The rows are the same ones the settle path above walked in this very
	// sweep, so a completion reported a moment ago has already left them closed and the stop
	// goes through on this tick rather than the next.
	if len(openInstrRows(name)) > 0 {
		rc.resetStopSettle(name)
		return
	}
	sig := collectReportSignals(m, armedAt.Format(time.RFC3339), "", "")
	v := evalReportEvidence(sig)
	// The pane check costs a tmux call, so it is only made once the cheap evidence is quiet —
	// the same order (and the same reason) as the report path.
	if v.Quiet && !v.Terminal && reportPaneBusy(m) {
		sig.PaneBusy = true
		v = evalReportEvidence(sig)
	}
	if !v.Quiet {
		rc.resetStopSettle(name)
		return
	}
	if !v.Terminal && !rc.stopDebounce(name, now) {
		return // not yet two consecutive quiet ticks
	}
	if err := stopArmedSession(name); err != nil {
		// Keep the arm and the debounce: a halt that failed (tmux refused, the runtime is
		// wedged) has to be retried, and dropping the arm here would silently turn "stop when
		// you are done" into "nothing happened".
		log.Printf("stop-after-turn: %s: halt failed, retrying next tick: %v", name, err)
		return
	}
	rc.forgetStopArm(name)
	log.Printf("stop-after-turn: halted %s (%s)", name, v.Why)
}

// stopDebounce is the arm's own two-tick debounce, with the same time condition as the
// report's: hint wakeups can run several sweeps back to back, and without it "observed twice"
// would be reachable within a second of the turn ending.
func (rc *reportReconciler) stopDebounce(name string, now time.Time) bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	st := rc.stops[name]
	if st.quiet == 0 {
		st.quietSince = now
	}
	st.quiet++
	rc.stops[name] = st
	return st.quiet >= reportSettleTicks && !now.Before(st.quietSince.Add(rc.interval))
}

func (rc *reportReconciler) resetStopSettle(name string) {
	rc.mu.Lock()
	delete(rc.stops, name)
	rc.mu.Unlock()
}

func (rc *reportReconciler) forgetStopArm(name string) { rc.resetStopSettle(name) }

// pruneStopArms drops the bookkeeping for sessions that are no longer armed (released,
// expired, stopped, deleted).
func (rc *reportReconciler) pruneStopArms(armed []string) {
	live := make(map[string]bool, len(armed))
	for _, n := range armed {
		live[n] = true
	}
	rc.mu.Lock()
	for name := range rc.stops {
		if !live[name] {
			delete(rc.stops, name)
		}
	}
	rc.mu.Unlock()
}
