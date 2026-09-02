package codex

import (
	"errors"
	"testing"
)

// stubLoggedIn swaps the `codex login status` probe for the duration of a test.
func stubLoggedIn(t *testing.T, v bool) {
	t.Helper()
	prev := loggedIn
	loggedIn = func() bool { return v }
	t.Cleanup(func() { loggedIn = prev })
}

// gateSupervisor returns a SEPARATE supervisor pointed at a port nothing listens
// on, so Ensure takes the "would have to spawn a daemon" branch.
//
// パッケージ共有の Serve() は使わない: 同じパッケージの別テストが偽 app-server に
// つないだまま置いていくと、こちらの Ensure が「もう up」の近道に入って素通しし、
// ゲートを検査したつもりで何も見ていないことになる（実際そうなった）。
// Shutdown 後始末つき — ゲートが壊れて本当に起動したとき、テストが 110 MB の
// daemon を置き去りにしないため（opencode 側では実際にやらかした）。
func gateSupervisor(t *testing.T) *Supervisor {
	t.Helper()
	t.Setenv(appServerAddrEnv, "ws://127.0.0.1:1")
	s := &Supervisor{}
	t.Cleanup(s.Shutdown)
	return s
}

// 未ログインのワークスペースで app-server を起こさないこと。app-server 自体は認証を
// 見ずに listen できてしまい、実測 RSS 約 110 MB がまるごと無駄になる。
func TestEnsureRefusesToSpawnWhenLoggedOut(t *testing.T) {
	s := gateSupervisor(t)
	stubLoggedIn(t, false)
	_, _, err := s.Ensure()
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("Ensure error = %v, want ErrNotLoggedIn", err)
	}
}

// 無効化が最優先: 未ログイン判定より前に落ちること（`codex login status` の exec を
// 無効化したワークスペースで走らせない）。
func TestEnsureDisabledBeatsTheAuthGate(t *testing.T) {
	t.Setenv("AF_CODEX_APP_SERVER_DISABLE", "1")
	probed := false
	prev := loggedIn
	loggedIn = func() bool { probed = true; return true }
	t.Cleanup(func() { loggedIn = prev })
	if _, _, err := (&Supervisor{}).Ensure(); err == nil {
		t.Fatal("Ensure succeeded while the app-server is disabled")
	}
	if probed {
		t.Fatal("無効化されているのに codex login status を叩いた")
	}
}

// 需要の勘定に TUI ルートを必ず含めること。ここを落とすと、managed が 0 の瞬間に
// 生きている TUI セッションの backend（codex --remote）を引き抜く。
func TestDependentsCountsTUISessions(t *testing.T) {
	prev := TUIDependents
	t.Cleanup(func() { TUIDependents = prev })

	TUIDependents = func() int { return 0 }
	if got := dependents(); got != 0 {
		t.Fatalf("dependents = %d, want 0 on an empty workspace", got)
	}
	TUIDependents = func() int { return 2 }
	if got := dependents(); got != 2 {
		t.Fatalf("dependents = %d, want 2 (TUI sessions alone are demand)", got)
	}
}

// managed ハンドルは live ではなく **登録済み** で数える: daemon が死ぬと
// runtimeLost が全ハンドルの alive を落とすので、live で数えると復旧すべき場面が
// 需要ゼロに見えて、起こし直しも自動停止の判断も逆になる。
func TestDependentsCountsRegisteredHandlesNotLiveOnes(t *testing.T) {
	prev := TUIDependents
	TUIDependents = func() int { return 0 }
	t.Cleanup(func() { TUIDependents = prev })

	handlesMu.Lock()
	handles["gate-test"] = &threadHandle{name: "gate-test"} // alive=false（＝runtime を失った状態）
	handlesMu.Unlock()
	t.Cleanup(func() {
		handlesMu.Lock()
		delete(handles, "gate-test")
		handlesMu.Unlock()
	})

	if got := len(liveHandles()); got != 0 {
		t.Fatalf("liveHandles = %d, want 0（このテストの前提）", got)
	}
	if got := dependents(); got != 1 {
		t.Fatalf("dependents = %d, want 1 — 死んだ runtime のハンドルも需要", got)
	}
}

// 需要が在る間は畳まないこと（監視ループの判定と停止の間の競合を潰す再確認）。
func TestStopIfIdleRefusesWhileNeeded(t *testing.T) {
	prev := TUIDependents
	TUIDependents = func() int { return 1 }
	t.Cleanup(func() { TUIDependents = prev })

	s := &Supervisor{up: true, watching: true}
	if s.stopIfIdle() {
		t.Fatal("需要が在るのに停止した")
	}
	if !s.up {
		t.Fatal("停止を見送ったのに up を落とした")
	}
}

// 既に落ちている supervisor に対しては「停止済み」を返して監視を降りる。
func TestStopIfIdleOnAlreadyDownStopsWatching(t *testing.T) {
	prev := TUIDependents
	TUIDependents = func() int { return 0 }
	t.Cleanup(func() { TUIDependents = prev })

	s := &Supervisor{watching: true}
	if !s.stopIfIdle() {
		t.Fatal("落ちている supervisor で監視を降りなかった")
	}
	if s.watching {
		t.Fatal("watching が立ったまま — 次の Ensure が監視を張り直せない")
	}
}
