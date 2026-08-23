package main

import "testing"

// この表が docs/75 §75.5 の条件表そのもの。状態を足したら**必ずここに 1 行足す**
// （足し忘れると activityUnknown に落ちる＝畳まれず、起こし続けもしない）。
func TestSessionActivityClassification(t *testing.T) {
	cases := []struct {
		name string
		in   sessionWire
		want activity
	}{
		{"working は機械が動いている", sessionWire{Alive: true, State: stateWorking}, activityMachineBusy},
		{"idle は畳んでよい", sessionWire{Alive: true, State: stateIdle}, activityIdleWait},
		{"limited は時計待ちで idle と同じ", sessionWire{Alive: true, State: stateLimited}, activityIdleWait},
		{"question は人待ち", sessionWire{Alive: true, State: stateQuestion}, activityHumanWait},
		{"plan は人待ち", sessionWire{Alive: true, State: statePlan}, activityHumanWait},
		{"permission は人待ち", sessionWire{Alive: true, State: statePermission}, activityHumanWait},
		{"blocked は人待ち", sessionWire{Alive: true, State: stateBlocked}, activityHumanWait},
		{"auth は人待ち", sessionWire{Alive: true, State: stateAuth}, activityHumanWait},
		{"spend_limit は人待ち", sessionWire{Alive: true, State: stateSpendLimit}, activityHumanWait},
		// backgroundBusy は state と直交する（docs/75 D3）。
		{"idle でも背景作業中なら machineBusy", sessionWire{Alive: true, State: stateIdle, BackgroundBusy: true}, activityMachineBusy},
		{"question でも背景作業中なら machineBusy", sessionWire{Alive: true, State: stateQuestion, BackgroundBusy: true}, activityMachineBusy},
		// 分からないものはどちらにも倒さない。
		{"shell は state が空", sessionWire{Alive: true, Kind: "shell", State: ""}, activityUnknown},
		{"知らない状態は unknown", sessionWire{Alive: true, State: "teleported"}, activityUnknown},
		{"停止中の行は対象外", sessionWire{Alive: false, State: stateWorking}, activityUnknown},
		{"停止中は背景フラグが残っていても対象外", sessionWire{Alive: false, BackgroundBusy: true}, activityUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sessionActivity(c.in); got != c.want {
				t.Errorf("sessionActivity(%+v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// tier2 が止めてよいか。★P0 時点では question だけが過渡的な例外として
// 「起こし続ける」側に残る（持ち越し = P1 が入るまで畳めないため）。
func TestHoldsWorkspace(t *testing.T) {
	cases := []struct {
		state string
		bg    bool
		want  bool
	}{
		{stateWorking, false, true},
		{stateIdle, true, true},      // 背景作業中（D3 の修正）
		{stateQuestion, false, true}, // 過渡的例外（P2 で false になる）
		{stateIdle, false, false},
		{stateLimited, false, false},
		{statePlan, false, false},
		{statePermission, false, false},
		{stateBlocked, false, false},
		{stateAuth, false, false},
		{stateSpendLimit, false, false},
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

// tier1 の対象。★P0 では旧 reapableIdle と同一集合（idle / limited / spend_limit）。
func TestTier1ReapablePreservesLegacySet(t *testing.T) {
	reapable := map[string]bool{stateIdle: true, stateLimited: true, stateSpendLimit: true}
	for _, st := range []string{stateWorking, stateIdle, stateQuestion, statePlan, statePermission,
		stateBlocked, stateAuth, stateLimited, stateSpendLimit, ""} {
		s := sessionWire{Alive: true, State: st}
		if got := tier1Reapable(s); got != reapable[st] {
			t.Errorf("tier1Reapable(%q) = %v, want %v", st, got, reapable[st])
		}
	}
	// 背景作業を抱えた idle は畳まない（D3）。
	if tier1Reapable(sessionWire{Alive: true, State: stateIdle, BackgroundBusy: true}) {
		t.Error("tier1Reapable(idle+backgroundBusy) = true, want false: 走っている背景作業を殺す")
	}
	if tier1Reapable(sessionWire{State: stateIdle}) {
		t.Error("tier1Reapable(dead) = true, want false")
	}
}
