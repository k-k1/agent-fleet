//go:build drift

// 共有 daemon の生涯（docs/log/27 §7.1）を**実 codex バイナリ**で通す Tier 1 ドリフト検知。
// ターンは消費しない（app-server を起こして畳むだけ）。兄弟: drift_test.go。
//
// ここが埋めるのは「需要で起き、需要ゼロで畳む」の**プロセスまで含めた**確認 —
// ユニットテストはゲートと数え方しか見ておらず、実際に起動・停止したかは見ていない。

package codex

import (
	"os/exec"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func TestDriftCodexDaemonStartsOnDemandAndStopsWhenIdle(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex binary not on PATH")
	}
	if !loggedIn() {
		t.Skip("codex is not logged in — the auth gate would (correctly) refuse to start")
	}
	// 既定ポートは実フリートの daemon が居るかもしれないので避ける。
	const addr = "ws://127.0.0.1:7897"
	t.Setenv(appServerAddrEnv, addr)
	if healthy(addr) {
		t.Fatalf("%s に既に何かが listen している — テスト用ポートを変えること", addr)
	}

	prev := TUIDependents
	needs := 1
	TUIDependents = func() int { return needs }
	t.Cleanup(func() { TUIDependents = prev })

	s := &Supervisor{}
	t.Cleanup(s.Shutdown) // 停止が効かなかったときに daemon を置き去りにしない

	if _, _, err := s.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !healthy(addr) {
		t.Fatal("Ensure が成功したのに daemon が listen していない")
	}

	// 需要ゼロ監視を短い猶予で直接回す（armIdleWatchLocked の既定 2 分は待てない）。
	prevTick := agents.IdleTickForTest(10 * time.Millisecond)
	t.Cleanup(func() { agents.IdleTickForTest(prevTick) })
	stopped := make(chan bool, 1)
	go func() {
		agents.WatchIdle("drift", dependents, s.stopIfIdle, 50*time.Millisecond)
		stopped <- true
	}()

	// 需要が在る間は畳まないこと。
	time.Sleep(300 * time.Millisecond)
	if !healthy(addr) {
		t.Fatal("需要が在るのに daemon が停止した")
	}

	needs = 0
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("需要ゼロになっても監視が停止に踏み切らなかった")
	}
	deadline := time.Now().Add(10 * time.Second)
	for healthy(addr) {
		if time.Now().After(deadline) {
			t.Fatal("停止したはずの daemon がまだ listen している")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
