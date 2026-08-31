package copilot

// 実バイナリ契約テスト（opt-in）: AF_COPILOT_LIVE=1 のときだけ実 `copilot --acp`
// を子プロセスとして起動し、docs/log/36 の managed 契約が実 CLI で成立することを
// 検証する — spawn→initialize→session/new→prompt(completed)→（別プロセスで）
// session/load resume→文脈保持。認証は環境の GitHub 連携（gh 透過認証）前提。
// 実行例: AF_COPILOT_LIVE=1 AGENT_COPILOT_BIN=<path> go test -run TestLive -v ./internal/agents/copilot/
//
// 週次リリースの CLI なので、これがドリフト検知の一次防衛線（models.go の静的
// カタログ妥当性は TUI /model の実測に委ねる — Free プランは Auto のみで
// --model 検証が成立しないため、ここでは model 未指定で流す）。

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func liveGate(t *testing.T) {
	t.Helper()
	if os.Getenv("AF_COPILOT_LIVE") != "1" {
		t.Skip("AF_COPILOT_LIVE=1 で実 CLI 契約テストを有効化")
	}
}

func TestLiveSpawnPromptResume(t *testing.T) {
	liveGate(t)
	// HOME を隔離すると gh の保存済み資格情報も見えなくなる（ambient 認証が
	// 切れる）— 隔離前に実 HOME でトークンを取り、env で明示注入する。
	tok := Token()
	if tok == "" {
		t.Skip("gh auth token が取れない（GitHub 未連携）— live テストをスキップ")
	}
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", t.TempDir()) // status / sids ストアの隔離
	t.Setenv("COPILOT_HOME", home)
	t.Setenv("COPILOT_GITHUB_TOKEN", tok)

	h := &threadHandle{
		name: "live1", dir: work, slotSid: "live-slot-1",
		events: make(chan agents.Event, 64),
	}
	if err := h.spawn(agents.ThreadSettings{}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(func() { DropHandle("live1"); Shutdown() })
	handlesMu.Lock()
	handles["live1"] = h
	handlesMu.Unlock()

	if err := h.Send(agents.TurnInput{Prompt: "Reply with exactly: LIVE-OK", ClientMessageID: "lm1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitState(t, h, agents.TurnRunning)
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) && h.currentState() != agents.TurnCompleted {
		time.Sleep(500 * time.Millisecond)
	}
	if st := h.currentState(); st != agents.TurnCompleted {
		t.Fatalf("turn did not complete: %s", st)
	}
	sid := h.sid
	if sid == "" || sids.Read("live-slot-1") != sid {
		t.Fatalf("sid not captured: %q store=%q", sid, sids.Read("live-slot-1"))
	}
	// read 正本: events.jsonl が書かれ、応答本文が載っている。
	turns := parseEvents(EventsPath(sid))
	found := false
	for _, tn := range turns {
		if tn.Role == "assistant" && strings.Contains(tn.Text, "LIVE-OK") {
			found = true
		}
	}
	if !found {
		t.Fatalf("assistant turn missing from events.jsonl: %+v", turns)
	}
	if st := liveStateFromFile(EventsPath(sid)); st != "idle" {
		t.Fatalf("post-turn live state: %q", st)
	}

	// 別プロセスでの resume（session/load）: 子を落として spawn し直し、文脈が
	// 残っていることを実プロンプトで確認する。
	h.mu.Lock()
	oldCmd := h.cmd
	h.alive = false
	h.mu.Unlock()
	stopChild(oldCmd)
	time.Sleep(1 * time.Second)
	if err := h.spawn(agents.ThreadSettings{}); err != nil {
		t.Fatalf("respawn: %v", err)
	}
	if h.sid != sid {
		t.Fatalf("resume changed sid: %q → %q", sid, h.sid)
	}
	if err := h.Send(agents.TurnInput{Prompt: "What exact string did I ask you to reply with before? Answer with just that string.", ClientMessageID: "lm2"}); err != nil {
		t.Fatalf("send2: %v", err)
	}
	deadline = time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) && h.currentState() != agents.TurnCompleted {
		time.Sleep(500 * time.Millisecond)
	}
	turns = parseEvents(EventsPath(sid))
	last := turns[len(turns)-1]
	if last.Role != "assistant" || !strings.Contains(last.Text, "LIVE-OK") {
		t.Fatalf("resume lost context; last turn: %+v", last)
	}
}

// TestLiveModels: 実 TUI /model スクレイプの契約。Free プランのアカウントでは
// 空カタログ（Auto のみ）、有償プランでは 1 件以上の id が返る — どちらでも
// 「プローブが完走して解析できる」ことがこのテストの本体（描画ドリフト検知）。
func TestLiveModels(t *testing.T) {
	liveGate(t)
	if Token() == "" {
		t.Skip("gh auth token が取れない（GitHub 未連携）— live テストをスキップ")
	}
	list, err := probeModels()
	if err != nil {
		t.Fatalf("probeModels: %v", err)
	}
	t.Logf("catalog: %d models", len(list))
	for _, m := range list {
		if m.ID == "" || strings.EqualFold(m.ID, "auto") {
			t.Errorf("bad catalog entry: %+v", m)
		}
	}
}
