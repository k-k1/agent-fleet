package opencode

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// unspawnableAddr points the supervisor at an address that (a) can never be
// healthy, so ensure reaches the "would have to spawn" branch, and (b) fails
// splitServeAddr, which is the very next step — so a test that gets PAST the gate
// still cannot start a real daemon.
//
// 「使われていないポート」では不十分だった: このコンテナは
// ip_unprivileged_port_start=0 なので :1 にも本当に bind でき、ゲートを通した
// テストが 310 MB の serve を起動しっぱなしにした（実害あり）。
// パッケージ共有の Serve() は使わない: 同じパッケージの別テストが daemon を up の
// まま置いていくと、こちらの ensure が近道に入ってゲートを素通りし、検査したつもりで
// 何も見ていないことになる（codex 側で実際にそうなった）。
func unspawnableAddr(t *testing.T) *Supervisor {
	t.Helper()
	t.Setenv(serveAddrEnv, "tcp://127.0.0.1:7799")
	// HOME も隔離する。ensure は接続ゲートで secrets.Load() を通り、隔離しないと
	// **利用者の実 ~/.config/agent-fleet の資格情報ストア**を読んで（そして
	// `secrets.enc.lock` を作って）判定する —— opencode に接続済みの開発機では
	// 「未接続なら起こさない」の検査が成立しなくなる。実測: このヘルパを使う
	// テストだけが実 HOME に lock ファイルを残していた。
	t.Setenv("HOME", t.TempDir())
	s := &Supervisor{}
	t.Cleanup(s.Shutdown)
	return s
}

// stubUsagePref forces the 使う枠 setting for the duration of a test.
func stubUsagePref(t *testing.T, v string) {
	t.Helper()
	prev := UsagePref
	UsagePref = func() string { return v }
	t.Cleanup(func() { UsagePref = prev })
}

// 未接続（既定の UsageOff）のワークスペースで serve を起こさないこと。serve は認証を
// 見ずに listen できてしまい、実測 RSS 約 305 MB がまるごと無駄になる。
func TestEnsureRefusesToSpawnWhenNotConnected(t *testing.T) {
	s := unspawnableAddr(t)
	stubUsagePref(t, UsageOff)
	_, _, err := s.Ensure()
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Ensure error = %v, want ErrNotConnected", err)
	}
}

// OAuth device フローだけは未接続でも起こせること。この API 群こそが「未接続を接続に
// 変える」経路なので、未接続を理由に断ると永久にログインできない（鶏と卵）。
// ゲートを通り越したことだけを見る（アドレスは spawn に届かない形にしてある）。
func TestEnsureAllowsUnauthedForTheOAuthFlow(t *testing.T) {
	s := unspawnableAddr(t)
	stubUsagePref(t, UsageOff)
	_, _, err := s.ensure(true)
	if errors.Is(err, ErrNotConnected) {
		t.Fatal("OAuth フローが未接続ゲートで止められた — これでは一生ログインできない")
	}
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("ensure error = %v, want the address parse error (ゲートの先まで進んだ証拠)", err)
	}
}

// 無効化が最優先: 接続判定より前に落ちること。
func TestEnsureDisabledBeatsTheConnectionGate(t *testing.T) {
	t.Setenv("AF_OPENCODE_SERVE_DISABLE", "1")
	stubUsagePref(t, UsageFree)
	if _, _, err := (&Supervisor{}).Ensure(); err == nil {
		t.Fatal("Ensure succeeded while serve is disabled")
	}
}

// device フローの最中は需要として数えること。数えないと、利用者がブラウザで承認して
// いる最中に足元の daemon を畳んでしまう。
func TestDependentsHoldsDuringTheOAuthFlow(t *testing.T) {
	prevAt := oauthTouchAt
	t.Cleanup(func() {
		oauthTouchMu.Lock()
		oauthTouchAt = prevAt
		oauthTouchMu.Unlock()
	})

	oauthTouchMu.Lock()
	oauthTouchAt = time.Time{}
	oauthTouchMu.Unlock()
	if got := dependents(); got != 0 {
		t.Fatalf("dependents = %d, want 0 on an idle workspace", got)
	}

	oauthTouch()
	if got := dependents(); got != 1 {
		t.Fatalf("dependents = %d, want 1 while an OAuth flow is in progress", got)
	}

	// 窓を過ぎたら需要は消える（放置されたフローが daemon を永久に生かさない）。
	oauthTouchMu.Lock()
	oauthTouchAt = time.Now().Add(-oauthHoldTTL - time.Second)
	oauthTouchMu.Unlock()
	if got := dependents(); got != 0 {
		t.Fatalf("dependents = %d, want 0 once the OAuth window has expired", got)
	}
}

// 需要が在る間は畳まないこと（監視ループの判定と停止の間の競合を潰す再確認）。
func TestStopIfIdleRefusesWhileNeeded(t *testing.T) {
	oauthTouch()
	t.Cleanup(func() {
		oauthTouchMu.Lock()
		oauthTouchAt = time.Time{}
		oauthTouchMu.Unlock()
	})
	s := &Supervisor{up: true, watching: true}
	if s.stopIfIdle() {
		t.Fatal("需要が在るのに停止した")
	}
	if !s.up {
		t.Fatal("停止を見送ったのに up を落とした")
	}
}
