package main

import (
	"testing"
	"time"
)

// This table is the condition table of docs/log/75 §75.5 itself. Add a state, add a row
// here: a state missing from it falls to activityUnknown, which is neither folded nor a
// reason to stay awake.
func TestSessionActivityClassification(t *testing.T) {
	cases := []struct {
		name string
		in   sessionWire
		want activity
	}{
		{"working: a machine is running", sessionWire{Alive: true, State: stateWorking}, activityMachineBusy},
		// codex context compaction. Missing from the table it falls to unknown, and the
		// whole workspace can then stop in the middle of the compaction.
		{"compacting: a machine is running too", sessionWire{Alive: true, State: stateCompacting}, activityMachineBusy},
		{"idle: foldable", sessionWire{Alive: true, State: stateIdle}, activityIdleWait},
		{"limited: waiting on a clock, same as idle", sessionWire{Alive: true, State: stateLimited}, activityIdleWait},
		{"question: waiting on a human", sessionWire{Alive: true, State: stateQuestion}, activityHumanWait},
		{"plan: waiting on a human", sessionWire{Alive: true, State: statePlan}, activityHumanWait},
		{"permission: waiting on a human", sessionWire{Alive: true, State: statePermission}, activityHumanWait},
		{"blocked: waiting on a human", sessionWire{Alive: true, State: stateBlocked}, activityHumanWait},
		{"auth: waiting on a human", sessionWire{Alive: true, State: stateAuth}, activityHumanWait},
		{"spend_limit: waiting on a human", sessionWire{Alive: true, State: stateSpendLimit}, activityHumanWait},
		// backgroundBusy is orthogonal to state (docs/log/75 D3).
		{"idle with background work is machineBusy", sessionWire{Alive: true, State: stateIdle, BackgroundBusy: true}, activityMachineBusy},
		{"question with background work is machineBusy", sessionWire{Alive: true, State: stateQuestion, BackgroundBusy: true}, activityMachineBusy},
		// What cannot be known falls on neither side.
		{"shell has an empty state", sessionWire{Alive: true, Kind: "shell", State: ""}, activityUnknown},
		{"an unknown state is unknown", sessionWire{Alive: true, State: "teleported"}, activityUnknown},
		{"a stopped row is out of scope", sessionWire{Alive: false, State: stateWorking}, activityUnknown},
		{"a stopped row is out of scope even with the background flag still set", sessionWire{Alive: false, BackgroundBusy: true}, activityUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sessionActivity(c.in); got != c.want {
				t.Errorf("sessionActivity(%+v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// Tier 2 refuses to stop only while a machine is working. That every human wait —
// question included — is false here is the whole point: while question was true, a
// workspace with an open AUQ never stopped and billed straight through
// (docs/log/75 §75.1).
func TestHoldsWorkspace(t *testing.T) {
	cases := []struct {
		state string
		bg    bool
		want  bool
	}{
		{stateWorking, false, true},
		{stateIdle, true, true}, // background work (D3)
		{stateQuestion, true, true},
		{stateQuestion, false, false}, // the case this exists for
		{stateIdle, false, false},
		{stateLimited, false, false},
		{statePlan, false, false},
		{statePermission, false, false},
		{stateBlocked, false, false},
		{stateAuth, false, false},
		{stateSpendLimit, false, false},
		{stateCompacting, false, true}, // never stop mid-compaction
		{"", false, false},
	}
	for _, c := range cases {
		s := sessionWire{Alive: true, State: c.state, BackgroundBusy: c.bg}
		if got := holdsWorkspace(s); got != c.want {
			t.Errorf("holdsWorkspace(state=%q bg=%v) = %v, want %v", c.state, c.bg, got, c.want)
		}
		if dead := (sessionWire{State: c.state, BackgroundBusy: c.bg}); holdsWorkspace(dead) {
			t.Errorf("holdsWorkspace(dead state=%q) = true, want false", c.state)
		}
	}
}

// Tier 1 takes everything foldable (the idle side plus human waits). A folded interaction
// is moved to carried, so nothing is lost (docs/log/75 §75.6). Only unknown (shell / ssm,
// unrecognised states) is out of scope.
func TestTier1Reapable(t *testing.T) {
	reapable := map[string]bool{
		stateIdle: true, stateLimited: true, stateSpendLimit: true,
		stateQuestion: true, statePlan: true, statePermission: true,
		stateBlocked: true, stateAuth: true,
	}
	for _, st := range []string{stateWorking, stateCompacting, stateIdle, stateQuestion, statePlan,
		statePermission, stateBlocked, stateAuth, stateLimited, stateSpendLimit, ""} {
		s := sessionWire{Alive: true, State: st}
		if got := tier1Reapable(s); got != reapable[st] {
			t.Errorf("tier1Reapable(%q) = %v, want %v", st, got, reapable[st])
		}
	}
	// An idle row holding background work is not folded (D3).
	if tier1Reapable(sessionWire{Alive: true, State: stateIdle, BackgroundBusy: true}) {
		t.Error("tier1Reapable(idle+backgroundBusy) = true, want false: it kills background work that is running")
	}
	if tier1Reapable(sessionWire{State: stateIdle}) {
		t.Error("tier1Reapable(dead) = true, want false")
	}
}

// Which clock applies follows from the classification (idle side: session, human wait:
// interaction). Swap them and a "wait 4 hours on a question" setting applies to idle too.
func TestTierClocksTier1For(t *testing.T) {
	cl := tierClocks{session: 1, sessionOn: true, interaction: 2, interactionOn: true}
	for _, c := range []struct {
		state string
		want  time.Duration
	}{
		{stateIdle, 1}, {stateLimited, 1},
		{stateQuestion, 2}, {statePlan, 2}, {statePermission, 2}, {stateBlocked, 2}, {stateAuth, 2}, {stateSpendLimit, 2},
	} {
		got, on := cl.tier1For(sessionWire{Alive: true, State: c.state})
		if !on || got != c.want {
			t.Errorf("tier1For(%q) = (%v,%v), want (%v,true)", c.state, got, on, c.want)
		}
	}
	// Either side can be disabled alone: human waits not folded while idle still is.
	off := tierClocks{session: 1, sessionOn: true}
	if _, on := off.tier1For(sessionWire{Alive: true, State: stateQuestion}); on {
		t.Error("interaction_idle_timeout=0, yet a human wait was about to be folded")
	}
	if _, on := off.tier1For(sessionWire{Alive: true, State: stateIdle}); !on {
		t.Error("idle stopped being folded as well")
	}
	if _, on := cl.tier1For(sessionWire{Alive: true, State: stateWorking}); on {
		t.Error("working was about to be folded")
	}
}

// A tenant that sets only session gets that value for human waits too, so the number on
// screen is not a lie. Explicit value > the tenant's session > deployment default.
func TestInteractionTimeoutFallbackChain(t *testing.T) {
	def := 9 * time.Minute
	cases := []struct {
		name    string
		lim     tenantLimits
		wantDur time.Duration
		wantOn  bool
	}{
		{"unset falls back to the deployment default", tenantLimits{}, def, true},
		{"with only session set, follow that", tenantLimits{SessionIdleTimeout: "5m"}, 5 * time.Minute, true},
		{"an explicit value wins", tenantLimits{SessionIdleTimeout: "5m", InteractionIdleTimeout: "4h"}, 4 * time.Hour, true},
		{"an explicit 0 disables human waits alone", tenantLimits{SessionIdleTimeout: "5m", InteractionIdleTimeout: "0"}, 0, false},
		{"session = 0 makes human waits 0 too", tenantLimits{SessionIdleTimeout: "0"}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sessTO, sessOn := idleTimeout(c.lim.SessionIdleTimeout, time.Hour)
			got, on := interactionTimeout(c.lim, sessTO, sessOn, def)
			if on != c.wantOn || (on && got != c.wantDur) {
				t.Errorf("interactionTimeout = (%v,%v), want (%v,%v)", got, on, c.wantDur, c.wantOn)
			}
		})
	}
}

// The do-not-stop pin (docs/log/75). shell / ssm carry no state, so none of the branches
// further in ever catch them: the pin only means anything if it applies at the outermost
// point of the classification.
func TestKeepAwakePin(t *testing.T) {
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	past := time.Now().Add(-time.Minute).Format(time.RFC3339)

	// A shell (empty state) is protected too — the reason the pin exists.
	shell := sessionWire{Alive: true, Kind: "shell", KeepAwakeUntil: future}
	if got := sessionActivity(shell); got != activityMachineBusy {
		t.Errorf("pinned shell = %v, want machineBusy", got)
	}
	if !holdsWorkspace(shell) {
		t.Error("a pinned shell is not holding the workspace")
	}
	if tier1Reapable(shell) {
		t.Error("tier 1 was about to fold a pinned row")
	}
	// An idle claude is protected the same way (a long unattended run one wants left alone).
	if !holdsWorkspace(sessionWire{Alive: true, Kind: "claude", State: stateIdle, KeepAwakeUntil: future}) {
		t.Error("a pinned idle session is not protected")
	}
	// An expired pin does not hold — this is what keeps a forgotten pin from billing forever.
	if holdsWorkspace(sessionWire{Alive: true, Kind: "shell", KeepAwakeUntil: past}) {
		t.Error("an expired pin is still in force")
	}
	// A pin on a dead session does not hold the container.
	if holdsWorkspace(sessionWire{Kind: "shell", KeepAwakeUntil: future}) {
		t.Error("a pin on a stopped session held the workspace")
	}
	// An unreadable value falls to "not pinned" rather than to billing on in silence.
	if holdsWorkspace(sessionWire{Alive: true, Kind: "shell", KeepAwakeUntil: "いつまでも"}) {
		t.Error("an unreadable deadline took effect as a pin")
	}
	if keepAwake("", time.Now()) {
		t.Error("an empty value is not a pin")
	}
}

// The tier 1 kind gate (docs/log/75 P5). shell / ssm are the only exception; get it wrong
// and the halt kills a running job af cannot even see.
func TestTier1Foldable(t *testing.T) {
	for _, k := range []string{"claude", "codex", "opencode", "copilot", "cursor", "kiro", "agy", ""} {
		if !tier1Foldable(k) {
			t.Errorf("tier1Foldable(%q) = false, want true (halt is resumable)", k)
		}
	}
	for _, k := range []string{"shell", "ssm"} {
		if tier1Foldable(k) {
			t.Errorf("tier1Foldable(%q) = true, want false (it kills a running job)", k)
		}
	}
}
