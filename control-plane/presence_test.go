package main

import (
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestConnRegistryWatched pins presence to "a human is touching it" rather than "a
// socket exists" (docs/log/75 P3). If this table breaks, one forgotten Console tab
// holding a terminal pane open warms the workspace forever — a main cause of "it never
// stops", and nothing on screen tells the user they are still being billed.
func TestConnRegistryWatched(t *testing.T) {
	const ws = "ws1"
	grace := 30 * time.Minute
	now := time.Now()

	r := newConnRegistry()
	if r.watched(ws, grace, now) {
		t.Error("接続が無いのに在席と答えた")
	}

	// Terminal (attention-bearing): present the moment it opens, absent once the
	// keystrokes stop.
	r.addConn(ws, "s1", true)
	if !r.watched(ws, grace, now) {
		t.Error("開いた直後の端末が在席でない")
	}
	if !r.watched(ws, grace, now.Add(29*time.Minute)) {
		t.Error("猶予内なのに不在になった")
	}
	if r.watched(ws, grace, now.Add(31*time.Minute)) {
		t.Error("★打鍵の無い端末が在席のまま＝タブ 1 枚で永久に温まる")
	}
	// Typing again brings presence back.
	r.noteInput(ws)
	if !r.watched(ws, grace, time.Now().Add(time.Minute)) {
		t.Error("打鍵後も不在のまま")
	}

	// A non-terminal long-lived connection (a scheduled-run wake-up) counts as presence
	// unconditionally: it has no notion of a keystroke, and reading it as absence would
	// stop the workspace mid-delivery.
	r2 := newConnRegistry()
	r2.addConn(ws, "", false)
	if !r2.watched(ws, grace, now.Add(10*time.Hour)) {
		t.Error("定時実行の presence が猶予で切れた")
	}

	// 0 disables the grace: any open socket counts as presence.
	r3 := newConnRegistry()
	r3.addConn(ws, "s1", true)
	if !r3.watched(ws, 0, now.Add(10*time.Hour)) {
		t.Error("AF_PRESENCE_IDLE_TIMEOUT=0 で従来挙動に戻らない")
	}

	// Disconnecting ends presence.
	r.doneConn(ws, "s1", true)
	if r.watched(ws, grace, time.Now()) {
		t.Error("切断後も在席と答えた")
	}
}

// TestAttentionFreshGatesTheLease covers the decision the heartbeat makes about whether
// the presence lease may keep being renewed. It is a pure function precisely so the
// decision can be pinned without waiting on the 5s goroutine.
func TestAttentionFreshGatesTheLease(t *testing.T) {
	const ws = "ws1"
	r := newConnRegistry()
	r.addConn(ws, "s1", true)
	now := time.Now()
	if !r.attentionFresh(ws, time.Minute, now) {
		t.Error("開いた直後に lease を止めた")
	}
	if r.attentionFresh(ws, time.Minute, now.Add(2*time.Minute)) {
		t.Error("★打鍵が途絶えても lease を更新し続けている（DB の connected_until が生き続ける）")
	}
	if !r.attentionFresh(ws, 0, now.Add(10*time.Hour)) {
		t.Error("猶予 0（無効）で lease が止まった")
	}
	if !r.attentionFresh("unknown-ws", 0, now) {
		t.Error("無効時は未知の workspace でも true であるべき")
	}
	if r.attentionFresh("unknown-ws", time.Minute, now) {
		t.Error("記録の無い workspace を在席と答えた")
	}
}

// TestIsTerminalInputCountsOnlyKeystrokes guards that ping and resize are not counted as
// keystrokes. The Console pings every open socket periodically, so treating "a frame
// arrived" as presence restores the forgotten-tab-warms-forever behaviour.
func TestIsTerminalInputCountsOnlyKeystrokes(t *testing.T) {
	cases := []struct {
		name string
		mt   int
		data string
		want bool
	}{
		{"打鍵", websocket.TextMessage, `{"type":"input","data":"ls\n"}`, true},
		{"ping は在席ではない", websocket.TextMessage, `{"type":"ping"}`, false},
		{"resize は在席ではない", websocket.TextMessage, `{"type":"resize","cols":80,"rows":24}`, false},
		{"PTY 出力（binary）は在席ではない", websocket.BinaryMessage, `{"type":"input"}`, false},
		{"壊れた JSON", websocket.TextMessage, `{`, false},
		{"空", websocket.TextMessage, ``, false},
	}
	for _, c := range cases {
		if got := isTerminalInput(c.mt, []byte(c.data)); got != c.want {
			t.Errorf("%s: isTerminalInput = %v, want %v", c.name, got, c.want)
		}
	}
}
