package main

import (
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// 在席は「ソケットがある」ではなく「人が触っている」で数える（docs/75 P3）。
// この表が壊れると、端末ペインを開いた Console のタブ 1 枚で Workspace が永久に
// 温まる状態へ戻る — question と並ぶ「止まらない」の主因で、しかも利用者からは
// 自分が課金し続けていることが見えない。
func TestConnRegistryWatched(t *testing.T) {
	const ws = "ws1"
	grace := 30 * time.Minute
	now := time.Now()

	r := newConnRegistry()
	if r.watched(ws, grace, now) {
		t.Error("接続が無いのに在席と答えた")
	}

	// 端末（attention 付き）: 開いた瞬間は在席、打鍵が途絶えれば不在。
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
	// 打ち始めれば戻る。
	r.noteInput(ws)
	if !r.watched(ws, grace, time.Now().Add(time.Minute)) {
		t.Error("打鍵後も不在のまま")
	}

	// 端末以外の長命接続（定時実行の起床）は無条件に在席 — 打鍵という概念が無く、
	// 不在と読むと配達中に Workspace を止めてしまう。
	r2 := newConnRegistry()
	r2.addConn(ws, "", false)
	if !r2.watched(ws, grace, now.Add(10*time.Hour)) {
		t.Error("定時実行の presence が猶予で切れた")
	}

	// 0 = 機能オフ（従来どおりソケットがある限り在席）。
	r3 := newConnRegistry()
	r3.addConn(ws, "s1", true)
	if !r3.watched(ws, 0, now.Add(10*time.Hour)) {
		t.Error("AF_PRESENCE_IDLE_TIMEOUT=0 で従来挙動に戻らない")
	}

	// 切断で在席は消える。
	r.doneConn(ws, "s1", true)
	if r.watched(ws, grace, time.Now()) {
		t.Error("切断後も在席と答えた")
	}
}

// heartbeat が lease を更新し続けてよいかの判定。5 秒周期の goroutine を待たずに
// 固定できるよう純関数に切ってある。
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

// ★ping と resize を打鍵と数えないこと。Console は開いているソケットへ定期的に ping を
// 送るので、「フレームが来た＝在席」にすると閉じ忘れたタブが永久に温める挙動へ戻る。
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
