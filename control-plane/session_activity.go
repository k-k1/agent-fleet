package main

import "time"

// Classification of a single session row into "is this a reason to keep the container
// awake" and "may it be folded away" (docs/log/75 §75.5). The reaper's two tiers look
// only at this.
//
// It is one function because the decision used to be two inline boolean expressions in
// reaper.go (tier 2's busy test, and the re-test after taking the fence) with no test at
// all, and the two drifted apart twice — each time matched up again by hand. States keep
// being added (see agents.State*), so "the new state was forgotten in one of the two
// places" is closed structurally.
type activity int

const (
	// activityUnknown: what is happening cannot be known — shell / ssm (a running job is
	// invisible) and any state this CP does not know. Falls on NEITHER side: not a reason
	// to stay awake (otherwise every new state warms the workspace forever), and not
	// foldable either (do not kill what you do not understand).
	activityUnknown activity = iota
	// activityIdleWait: the turn is over and the next move belongs to a human or to the
	// clock. Foldable, and folding loses nothing.
	activityIdleWait
	// activityHumanWait: stopped waiting for a human decision. Foldable, but the pending
	// interaction must be carried over before folding (docs/log/75 §75.6). Not a reason to
	// keep the container awake — a human wait can last days.
	activityHumanWait
	// activityMachineBusy: a machine is working. Must not be touched.
	activityMachineBusy
)

// State names. The Agent side (internal/agents/notify.go, internal/status) is
// authoritative; these are copies of the strings that arrive over the wire.
const (
	stateWorking = "working"
	// stateCompacting is codex running a context compaction (WireLive in agents/codex).
	// Without a classification it falls to unknown and the whole workspace stops in the
	// middle of the compaction, because a working machine is then not counted as a reason
	// to stay awake. Per decision 9 what is not understood falls on neither side, but this
	// one is understood: it is machineBusy.
	stateCompacting = "compacting"
	stateIdle       = "idle"
	stateQuestion   = "question"
	statePlan       = "plan"
	statePermission = "permission"
	stateBlocked    = "blocked"
	stateAuth       = "auth"
	stateLimited    = "limited"
	stateSpendLimit = "spend_limit"
)

// busyState reports whether the state alone means "a machine is working".
//
// The list lives here only; idle_forecast.go's holdersOf reads it rather than keeping a
// copy. Written in two places, a newly added state gets forgotten in one of them:
// stateCompacting was added to sessionActivity alone while holdersOf still looked at
// working, so the reaper correctly refused to stop while the screen said "stopping soon"
// — a repeat of the docs/log/75 decision 11 violation (the screen publishes the reaper's
// decision as it is).
func busyState(state string) bool {
	switch state {
	case stateWorking, stateCompacting:
		return true
	}
	return false
}

// sessionActivity classifies one live session row.
//
// A row that is not alive is activityUnknown: neither foldable, nor a reason to stay awake.
func sessionActivity(s sessionWire) activity {
	if !s.Alive {
		return activityUnknown
	}
	// The user's "do not stop" pin (docs/log/75). It sits outermost because for shell /
	// ssm — the only escape hatch those have — state is empty, i.e. unknown, so none of
	// the branches below would ever catch it. It applies to live rows only (the !Alive
	// check above), so a pin on a dead session cannot hold the container.
	if keepAwake(s.KeepAwakeUntil, time.Now()) {
		return activityMachineBusy
	}
	// BackgroundBusy is orthogonal to state: with state idle there can still be a
	// run_in_background job, an in-process subagent / Workflow, or a background shell in
	// state S (the Agent's WireLive sets it). Checked first on the machineBusy side — the
	// reaper never looked at it and halted / stopped running background work with it.
	if s.BackgroundBusy {
		return activityMachineBusy
	}
	if busyState(s.State) {
		return activityMachineBusy
	}
	switch s.State {
	case stateIdle, stateLimited:
		// limited is a wait on the clock: CP's scheduled run wakes it at the reset time
		// (docs/log/47 §4-9), so folding loses nothing — the same treatment as idle.
		return activityIdleWait
	case stateQuestion, statePlan, statePermission, stateBlocked, stateAuth, stateSpendLimit:
		// blocked (the limit menu), auth (re-authentication) and spend_limit (a quota
		// raise) are all "a human acts now", a human wait like question / plan /
		// permission. Waiting does not resolve them by itself.
		return activityHumanWait
	}
	return activityUnknown
}

// holdsWorkspace reports whether tier 2 must leave the workspace running while this row
// exists.
//
// Only while a machine is working. A human wait (question / plan / permission / blocked /
// auth / spend_limit) is not a reason — it can last days, and keeping the container awake
// for it bills the user straight through (docs/log/75 §75.1; question surviving as the one
// exception was exactly the cause of "an open AUQ means the workspace never stops").
//
// That folding loses nothing is guaranteed by carry-over (docs/log/75 §75.6): a pending
// question/plan/permission is moved to carried just before the halt, and answering it from
// the Console resumes the session and delivers it.
func holdsWorkspace(s sessionWire) bool {
	return sessionActivity(s) == activityMachineBusy
}

// tier1Reapable reports whether tier 1 (session halt) may take this row: foldable, i.e.
// neither machineBusy nor unknown. Which timeout applies is decided by the caller from the
// classification (idleWait: session_idle_timeout, humanWait: interaction_idle_timeout).
func tier1Reapable(s sessionWire) bool {
	a := sessionActivity(s)
	return a == activityIdleWait || a == activityHumanWait
}

// keepAwake reports whether a user pin is still in force.
//
// An unreadable value means "not pinned": the string is written by af's own Agent, so a
// broken one is a bug, and the worst way for that bug to surface is "the workspace never
// stops" — billing silently forever. Erring the other way (failing to protect) costs one
// job, which the user can start again.
func keepAwake(until string, now time.Time) bool {
	if until == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, until)
	if err != nil {
		return false
	}
	return now.Before(t)
}

// tier1Foldable splits kinds by "is halting this safe" (docs/log/75 P5).
//
// A halt is a resumable stop (claude via --resume, managed by reconnecting the runtime
// handle), so an agent session of any kind may be folded. shell / ssm are the exception:
// halting one means killing the job it is running, and af cannot see what that job is (a
// foreground command name does not tell an abandoned less from a build, and ssm always
// shows aws). Anything worth protecting is declared with the "do not auto-stop" pin.
//
// An empty string is an old session carrying no kind, i.e. claude (the same default as
// normalizeKind).
func tier1Foldable(kind string) bool {
	switch kind {
	case "shell", "ssm":
		return false
	}
	return true
}
