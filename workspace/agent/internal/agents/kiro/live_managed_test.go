package kiro

// managed（ACP）ルートの実バイナリ契約テスト（opt-in・KIRO_LIVE=1）。実 `kiro-cli acp` を
// 子として起動し、docs/log/43 Track A2 の契約が実 CLI で成立することを検証する:
// spawn→initialize→session/new→prompt(completed)→転写がメモリ構築される→（別プロセスで）
// session/load resume→文脈保持＋転写がリプレイから再構築される。read 層の liveGate
// （live_test.go・KIRO_LIVE＋PATH）を共有する。認証は環境の kiro ログイン（ambient・
// ~/.local/share/kiro-cli）前提。
//
// 週次更新の CLI なので、これが managed 契約のドリフト検知線（sessionUpdate 判別子・
// session/load リプレイ形状・.lock の解放・stopReason が変わればここで落ちる）。
//   KIRO_LIVE=1 go test -run TestLiveManaged -v ./internal/agents/kiro/

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// isolateHomeKeepKiroAuth points HOME at a tempdir (isolating AF sids/status + a fresh
// session store) while symlinking kiro's ambient auth + settings so the CLI still logs in.
// We never read or copy the credential — the symlink lets kiro-cli read its OWN files.
func isolateHomeKeepKiroAuth(t *testing.T) {
	t.Helper()
	real, _ := os.UserHomeDir()
	home := t.TempDir()
	if real != "" {
		for _, rel := range []string{".local/share/kiro-cli", ".kiro/settings"} {
			src := filepath.Join(real, rel)
			if _, err := os.Stat(src); err != nil {
				continue
			}
			dst := filepath.Join(home, rel)
			_ = os.MkdirAll(filepath.Dir(dst), 0o755)
			_ = os.Symlink(src, dst)
		}
	}
	t.Setenv("HOME", home)
}

func waitCompleted(t *testing.T, h *threadHandle) {
	t.Helper()
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		switch h.currentState() {
		case agents.TurnCompleted:
			return
		case agents.TurnFailed, agents.TurnUnknown, agents.TurnCancelled:
			t.Fatalf("turn ended abnormally: %s", h.currentState())
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("turn did not complete (state %s)", h.currentState())
}

func TestLiveManagedSpawnPromptResume(t *testing.T) {
	liveGate(t)
	if !LoggedIn() {
		t.Fatal("expected a logged-in kiro (Builder ID) for the live managed test")
	}
	isolateHomeKeepKiroAuth(t)
	work := t.TempDir()

	h := &threadHandle{
		name: "livem1", dir: work, slotSid: "livem-slot-1",
		events: make(chan agents.Event, 64),
	}
	if err := h.spawn(agents.ThreadSettings{}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(func() { DropHandle("livem1"); Shutdown() })
	handlesMu.Lock()
	handles["livem1"] = h
	handlesMu.Unlock()

	if err := h.Send(agents.TurnInput{Prompt: "Reply with exactly: LIVE-KIRO-OK", ClientMessageID: "lm1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitState(t, h, agents.TurnRunning) // spawn の初期 Completed を追い越さないよう turn 開始を待つ
	waitCompleted(t, h)

	sid := h.sid
	if sid == "" || sids.Read("livem-slot-1") != sid {
		t.Fatalf("sid not captured: %q store=%q", sid, sids.Read("livem-slot-1"))
	}
	turns := h.buf.snapshot()
	found := false
	for _, tn := range turns {
		if tn.Role == "assistant" && strings.Contains(tn.Text, "LIVE-KIRO-OK") {
			found = true
		}
	}
	if !found {
		t.Fatalf("assistant turn missing from in-memory transcript: %+v", turns)
	}

	// Track D: 実 `_kiro.dev/metadata` から ライブ context% ＋ credits を捕捉できたか。
	// ContextFill が pct→token 変換で非 nil を返し、window が実カタログ値であることも裏取り。
	pct, window, credits, model, ok := ManagedContext("livem1")
	if !ok {
		t.Fatalf("ManagedContext: no live usage captured after a completed turn")
	}
	if pct <= 0 || pct > 100 {
		t.Fatalf("contextUsagePercentage out of range: %v", pct)
	}
	if window <= 0 {
		t.Fatalf("model %q context window not resolved (got %d)", model, window)
	}
	if credits < 0 {
		t.Fatalf("credits negative: %v", credits)
	}
	if c := (agentImpl{}).ContextFill(managedMeta("livem1", work)); c == nil || c.Window != window {
		t.Fatalf("ContextFill did not surface the live context: %+v", c)
	}
	t.Logf("Track D live usage: pct=%.2f%% window=%d credits=%.4f model=%s", pct, window, credits, model)
	// kiro は転写を v2 JSONL にも persist する（cursor と違う）— 停止中フォールバックの裏取り。
	if fileTranscript(managedMeta("livem1", work)).Turns == nil {
		t.Fatalf("kiro should have persisted the ACP turns to the v2 JSONL")
	}

	// 別プロセスでの resume（session/load）: 子を stdin EOF で正規終了させ（.lock 解放）、
	// spawn し直して文脈と転写が残ることを実プロンプトで確認する。
	h.mu.Lock()
	oldCmd, oldStdin := h.cmd, h.stdin
	h.alive = false
	h.mu.Unlock()
	stopChild(oldCmd, oldStdin)
	time.Sleep(2 * time.Second) // graceful exit ＋ .lock 解放待ち
	if err := h.spawn(agents.ThreadSettings{}); err != nil {
		t.Fatalf("respawn: %v", err)
	}
	if h.sid != sid {
		t.Fatalf("resume changed sid: %q → %q", sid, h.sid)
	}
	replayed := h.buf.snapshot()
	if len(replayed) == 0 {
		t.Fatalf("transcript not rebuilt from session/load replay")
	}
	if err := h.Send(agents.TurnInput{Prompt: "What exact string did I ask you to reply with before? Answer with just that string.", ClientMessageID: "lm2"}); err != nil {
		t.Fatalf("send2: %v", err)
	}
	waitState(t, h, agents.TurnRunning)
	waitCompleted(t, h)
	turns = h.buf.snapshot()
	last := turns[len(turns)-1]
	if last.Role != "assistant" || !strings.Contains(last.Text, "LIVE-KIRO-OK") {
		t.Fatalf("resume lost context; last turn: %+v", last)
	}
}

// managedMeta builds a managed session.Meta for the persisted-fallback check.
func managedMeta(name, dir string) session.Meta {
	return session.Meta{Name: name, Dir: dir, Kind: session.KindKiro, Driver: session.DriverManaged}
}
