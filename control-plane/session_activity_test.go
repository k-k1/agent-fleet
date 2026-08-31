package main

import (
	"testing"
	"time"
)

// この表が docs/log/75 §75.5 の条件表そのもの。状態を足したら**必ずここに 1 行足す**
// （足し忘れると activityUnknown に落ちる＝畳まれず、起こし続けもしない）。
func TestSessionActivityClassification(t *testing.T) {
	cases := []struct {
		name string
		in   sessionWire
		want activity
	}{
		{"working は機械が動いている", sessionWire{Alive: true, State: stateWorking}, activityMachineBusy},
		// codex の文脈圧縮。表から漏れていて unknown に落ちており、圧縮の最中に
		// Workspace ごと止まりうる状態だった（docs/log/75 P5 で追加）。
		{"compacting も機械が動いている", sessionWire{Alive: true, State: stateCompacting}, activityMachineBusy},
		{"idle は畳んでよい", sessionWire{Alive: true, State: stateIdle}, activityIdleWait},
		{"limited は時計待ちで idle と同じ", sessionWire{Alive: true, State: stateLimited}, activityIdleWait},
		{"question は人待ち", sessionWire{Alive: true, State: stateQuestion}, activityHumanWait},
		{"plan は人待ち", sessionWire{Alive: true, State: statePlan}, activityHumanWait},
		{"permission は人待ち", sessionWire{Alive: true, State: statePermission}, activityHumanWait},
		{"blocked は人待ち", sessionWire{Alive: true, State: stateBlocked}, activityHumanWait},
		{"auth は人待ち", sessionWire{Alive: true, State: stateAuth}, activityHumanWait},
		{"spend_limit は人待ち", sessionWire{Alive: true, State: stateSpendLimit}, activityHumanWait},
		// backgroundBusy は state と直交する（docs/log/75 D3）。
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

// tier2 が止めてよいか＝**機械が動いているときだけ止めない**。
// question を含む人待ちが false であることがこの機能の核心（docs/log/75 §75.1: これが true
// だった間、AUQ の出ているワークスペースは永久に停止せず課金され続けた）。
func TestHoldsWorkspace(t *testing.T) {
	cases := []struct {
		state string
		bg    bool
		want  bool
	}{
		{stateWorking, false, true},
		{stateIdle, true, true}, // 背景作業中（D3 の修正）
		{stateQuestion, true, true},
		{stateQuestion, false, false}, // ★本件
		{stateIdle, false, false},
		{stateLimited, false, false},
		{statePlan, false, false},
		{statePermission, false, false},
		{stateBlocked, false, false},
		{stateAuth, false, false},
		{stateSpendLimit, false, false},
		{stateCompacting, false, true}, // 圧縮中は止めない
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

// tier1 の対象＝畳んでよいもの全部（idle 系＋人待ち）。畳んだ対話は持ち越しへ退避される
// ので失われない（docs/log/75 §75.6）。unknown（shell/ssm・未知の状態）だけが対象外。
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
	// 背景作業を抱えた idle は畳まない（D3）。
	if tier1Reapable(sessionWire{Alive: true, State: stateIdle, BackgroundBusy: true}) {
		t.Error("tier1Reapable(idle+backgroundBusy) = true, want false: 走っている背景作業を殺す")
	}
	if tier1Reapable(sessionWire{State: stateIdle}) {
		t.Error("tier1Reapable(dead) = true, want false")
	}
}

// どの時計が当たるかは分類で決まる（idle 系 = session、人待ち = interaction）。
// 取り違えると「質問だけ 4 時間待つ」設定が idle にも効いてしまう。
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
	// 片方だけ無効にできる: 人待ちは畳まないが idle は畳む、という設定が成立すること。
	off := tierClocks{session: 1, sessionOn: true}
	if _, on := off.tier1For(sessionWire{Alive: true, State: stateQuestion}); on {
		t.Error("interaction_idle_timeout=0 なのに人待ちを畳もうとした")
	}
	if _, on := off.tier1For(sessionWire{Alive: true, State: stateIdle}); !on {
		t.Error("idle まで畳まなくなった")
	}
	if _, on := cl.tier1For(sessionWire{Alive: true, State: stateWorking}); on {
		t.Error("working を畳もうとした")
	}
}

// テナントが session だけを設定したら、人待ちもその値に従う（画面に出ている数字が
// 嘘にならないように）。明示値 > テナントの session > デプロイ既定。
func TestInteractionTimeoutFallbackChain(t *testing.T) {
	def := 9 * time.Minute
	cases := []struct {
		name    string
		lim     tenantLimits
		wantDur time.Duration
		wantOn  bool
	}{
		{"未設定はデプロイ既定", tenantLimits{}, def, true},
		{"session だけ設定したらそれに従う", tenantLimits{SessionIdleTimeout: "5m"}, 5 * time.Minute, true},
		{"明示値が最優先", tenantLimits{SessionIdleTimeout: "5m", InteractionIdleTimeout: "4h"}, 4 * time.Hour, true},
		{"明示 0 で人待ちだけ無効", tenantLimits{SessionIdleTimeout: "5m", InteractionIdleTimeout: "0"}, 0, false},
		{"session が 0 なら人待ちも 0", tenantLimits{SessionIdleTimeout: "0"}, 0, false},
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

// 停止しないピン（docs/log/75）。shell / ssm には state が無い＝分類の先の分岐に一切
// 引っかからないので、ピンは**分類の一番外**で効かなければ意味を持たない。
func TestKeepAwakePin(t *testing.T) {
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	past := time.Now().Add(-time.Minute).Format(time.RFC3339)

	// shell（state 空）でも守られる — これがこの機能の存在理由。
	shell := sessionWire{Alive: true, Kind: "shell", KeepAwakeUntil: future}
	if got := sessionActivity(shell); got != activityMachineBusy {
		t.Errorf("ピンされた shell = %v, want machineBusy", got)
	}
	if !holdsWorkspace(shell) {
		t.Error("ピンされた shell が Workspace を守っていない")
	}
	if tier1Reapable(shell) {
		t.Error("ピンされた行を tier1 が畳もうとした")
	}
	// idle な claude も同じく守られる（長い自動走行を止めたくないとき）。
	if !holdsWorkspace(sessionWire{Alive: true, Kind: "claude", State: stateIdle, KeepAwakeUntil: future}) {
		t.Error("ピンされた idle セッションが守られていない")
	}
	// 期限切れは効かない — 消し忘れたピンが永久に課金しないための本体。
	if holdsWorkspace(sessionWire{Alive: true, Kind: "shell", KeepAwakeUntil: past}) {
		t.Error("期限切れのピンがまだ効いている")
	}
	// 死んだセッションのピンはコンテナを抱え込まない。
	if holdsWorkspace(sessionWire{Kind: "shell", KeepAwakeUntil: future}) {
		t.Error("停止中のセッションのピンが Workspace を守った")
	}
	// 壊れた値は「ピンされていない」に倒す（黙って課金し続ける側に倒さない）。
	if holdsWorkspace(sessionWire{Alive: true, Kind: "shell", KeepAwakeUntil: "いつまでも"}) {
		t.Error("読めない期限がピンとして効いた")
	}
	if keepAwake("", time.Now()) {
		t.Error("空はピンではない")
	}
}

// tier1 の kind の門（docs/log/75 P5）。shell / ssm だけが例外で、そこを間違えると
// 走行中のジョブを halt で殺す（しかも af からは何が走っているか見えない）。
func TestTier1Foldable(t *testing.T) {
	for _, k := range []string{"claude", "codex", "opencode", "copilot", "cursor", "kiro", "agy", ""} {
		if !tier1Foldable(k) {
			t.Errorf("tier1Foldable(%q) = false, want true（halt は resumable）", k)
		}
	}
	for _, k := range []string{"shell", "ssm"} {
		if tier1Foldable(k) {
			t.Errorf("tier1Foldable(%q) = true, want false（走っているジョブを殺す）", k)
		}
	}
}
