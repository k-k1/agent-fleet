package main

// 中断からの自動再開の状態機械（docs/log/47 §4-6）。中断の検知そのものは
// internal/agents/claude/abort_test.go が、ペイン判定は internal/tmuxx の
// ゴールデンコーパスが押さえているので、ここで見るのは配線の側: いつ送るか・
// 何回で打ち切るか・打ち切ったとき報告側に何を渡すか・報告をいつ抑止するか。
// tmux も claude も持たないので副作用は差し替える。

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

type abortFixture struct {
	sent      []string // 注入できた再開プロンプト
	injectErr error
	pane      tmuxx.PaneRead
}

func newAbortFixture(t *testing.T) *abortFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	f := &abortFixture{pane: tmuxx.PaneRead{OK: true, Idle: true}}
	origInject, origPane := abortResumeInject, abortResumeReadingPane
	abortResumeInject = func(name, prompt string) error {
		if f.injectErr != nil {
			return f.injectErr
		}
		f.sent = append(f.sent, prompt)
		return nil
	}
	abortResumeReadingPane = func(string) tmuxx.PaneRead { return f.pane }
	t.Cleanup(func() { abortResumeInject, abortResumeReadingPane = origInject, origPane })
	return f
}

func setAbortResumePref(t *testing.T, on bool) {
	t.Helper()
	p := uiPrefsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"claudeAbortAutoResume": on})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func abMeta() session.Meta {
	return session.Meta{Name: "ab1", Dir: "/tmp/ab1", Kind: session.KindClaude}
}

func abState(t *testing.T, name string) abortResumeState {
	t.Helper()
	st, _ := abortResumeStates.Read(name)
	return st
}

func retryableAbort(at time.Time) claude.Abort {
	return claude.Abort{Msg: "API Error: Stream idle timeout - no chunks received", Retryable: true, At: at}
}

// TestAbortResumeWaitsThenSends: 中断の直後には撃たず（バックオフ）、待ち時間が過ぎて
// から1回だけ送る。即時再送は 529 / overloaded の原因が消える前に貴重な再試行を捨てる。
func TestAbortResumeWaitsThenSends(t *testing.T) {
	f := newAbortFixture(t)
	m := abMeta()
	cut := time.Now()
	a := retryableAbort(cut)

	abortResumeAttempt(m, abState(t, m.Name), a, cut.Add(5*time.Second))
	if len(f.sent) != 0 {
		t.Fatalf("バックオフ中に送っている: %v", f.sent)
	}
	if st := abState(t, m.Name); st.At == "" {
		t.Fatal("エピソードが開いた時点で永続化されていない（毎 tick 開き直す）")
	}

	abortResumeAttempt(m, abState(t, m.Name), a, cut.Add(abortResumeFirstDelay+time.Second))
	if len(f.sent) != 1 {
		t.Fatalf("再開プロンプト = %d 回, want 1: %v", len(f.sent), f.sent)
	}
	if st := abState(t, m.Name); st.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", st.Attempts)
	}
	// 直後の tick では撃ち直さない（2回目は abortResumeBackoff のあと）。
	abortResumeAttempt(m, abState(t, m.Name), a, cut.Add(abortResumeFirstDelay+abortResumeWatchInterval))
	if len(f.sent) != 1 {
		t.Errorf("バックオフを無視して %d 回送っている", len(f.sent))
	}
}

func TestAbortInfoForManagedUsesPersistedSignal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "oc-managed", Dir: "/tmp/oc-managed", Kind: session.KindOpencode, Driver: session.DriverManaged}
	at := time.Now().Truncate(time.Second)
	if err := managedAbortSignals.Write(m.Name, managedAbortSignal{At: at.Format(time.RFC3339), Msg: "HTTP 500"}); err != nil {
		t.Fatal(err)
	}
	a, ok := abortInfoFor(m)
	if !ok || !a.Retryable || a.Msg != "HTTP 500" || !a.At.Equal(at) {
		t.Fatalf("managed abort = %+v ok=%v", a, ok)
	}
}

// TestAbortResumeCapsThenHandsOver: 上限まで再送しても中断が続いたら手を引き、
// 報告側のカウンタを合わせる（＝配られる報告が「上限に達した」文面になる）。
// ここが「打ち切ったときだけアシスタントへ」の接点。
func TestAbortResumeCapsThenHandsOver(t *testing.T) {
	f := newAbortFixture(t)
	m := abMeta()
	cut := time.Now()
	a := retryableAbort(cut)

	now := cut.Add(abortResumeFirstDelay + time.Second)
	for i := 0; i < maxAutoResumeAttempts; i++ {
		abortResumeAttempt(m, abState(t, m.Name), a, now)
		now = now.Add(abortResumeBackoff + time.Second)
	}
	if len(f.sent) != maxAutoResumeAttempts {
		t.Fatalf("再開 = %d 回, want %d", len(f.sent), maxAutoResumeAttempts)
	}
	if abState(t, m.Name).GaveUp != "" {
		t.Fatal("上限に達する前に打ち切っている")
	}

	abortResumeAttempt(m, abState(t, m.Name), a, now)
	st := abState(t, m.Name)
	if st.GaveUp != abortGaveUpCapped {
		t.Fatalf("gaveUp = %q, want %q", st.GaveUp, abortGaveUpCapped)
	}
	if len(f.sent) != maxAutoResumeAttempts {
		t.Errorf("打ち切ったのに送っている: %v", f.sent)
	}
	if got := autoResumeAttempts(m.Name); got != maxAutoResumeAttempts {
		t.Errorf("報告側のカウンタ = %d, want %d（上限文面にならない）", got, maxAutoResumeAttempts)
	}
	// 打ち切ったら抑止は外れる — 中断報告が出せる状態に戻る。
	if abortResumeHolds(m.Name, a, now) {
		t.Error("打ち切ったのに報告を抑止し続けている")
	}
}

// TestAbortResumeSkipsBusyOrBlockedPane: 走行中／質問待ちのセッションへは撃たない。
// 中断レコードは末尾に残り続けるので、利用者が自分で再開した後も転写は中断に見える —
// そこへ「続けて」を送ると走っているターンへの割り込み指示になる。
func TestAbortResumeSkipsBusyOrBlockedPane(t *testing.T) {
	for _, tc := range []struct {
		name string
		pane tmuxx.PaneRead
	}{
		{"走行中", tmuxx.PaneRead{OK: true, Busy: true}},
		{"待機表示ではない", tmuxx.PaneRead{OK: true}},
		{"ペインが読めない", tmuxx.PaneRead{}},
		{"利用上限メニュー", tmuxx.PaneRead{OK: true, Idle: true, RateLimitMenu: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAbortFixture(t)
			f.pane = tc.pane
			m := abMeta()
			cut := time.Now()
			abortResumeAttempt(m, abState(t, m.Name), retryableAbort(cut), cut.Add(abortResumeFirstDelay+time.Second))
			if len(f.sent) != 0 {
				t.Fatalf("送ってはいけない状態で送った: %v", f.sent)
			}
			if st := abState(t, m.Name); st.DeliverTries != 1 {
				t.Errorf("deliverTries = %d, want 1", st.DeliverTries)
			}
		})
	}
}

// TestAbortResumeUndeliverableGivesUp: 届かない試行が続いたら打ち切る。叩き続けて直る
// 類（人が操作している・TUI の形が変わった）ではないので、人へ渡す方が早い。
func TestAbortResumeUndeliverableGivesUp(t *testing.T) {
	f := newAbortFixture(t)
	f.injectErr = errors.New("no pane")
	m := abMeta()
	cut := time.Now()
	a := retryableAbort(cut)

	now := cut.Add(abortResumeFirstDelay + time.Second)
	for i := 0; i < abortResumeMaxDeliverTries; i++ {
		abortResumeAttempt(m, abState(t, m.Name), a, now)
		now = now.Add(abortResumeBackoff + time.Second)
	}
	st := abState(t, m.Name)
	if st.GaveUp != abortGaveUpUndeliverable {
		t.Fatalf("gaveUp = %q, want %q", st.GaveUp, abortGaveUpUndeliverable)
	}
	if st.Attempts != 0 {
		t.Errorf("attempts = %d, want 0（送れていないのに数えている）", st.Attempts)
	}
}

// TestAbortResumeHoldsSuppressesReport: 報告の抑止条件。抑止は「自動再開が引き受けて
// いる間だけ」で、機能 OFF・打ち切り済み・エピソードが古い・そもそも再送で直らない
// 中断（blocked）では外れていなければならない — 外れないと中断がどこにも届かなくなる。
func TestAbortResumeHoldsSuppressesReport(t *testing.T) {
	now := time.Now()
	blocked := claude.Abort{Msg: "You've reached your Fable 5 limit.", At: now}

	t.Run("エピソード未作成でも中断が新しければ抑止する", func(t *testing.T) {
		newAbortFixture(t)
		if !abortResumeHolds("ab1", retryableAbort(now), now.Add(5*time.Second)) {
			t.Error("sweep 前の中断で抑止していない（報告が先回りする）")
		}
	})
	t.Run("エピソードが無く中断が古ければ抑止しない", func(t *testing.T) {
		newAbortFixture(t)
		if abortResumeHolds("ab1", retryableAbort(now), now.Add(10*time.Minute)) {
			t.Error("watcher が動いていないのに抑止し続けている（報告が永久に出ない）")
		}
	})
	t.Run("時刻の無い中断は抑止しない", func(t *testing.T) {
		newAbortFixture(t)
		if abortResumeHolds("ab1", claude.Abort{Msg: "x", Retryable: true}, now) {
			t.Error("時刻が読めない中断で抑止している（窓を閉じられない）")
		}
	})
	t.Run("blocked な中断は抑止しない", func(t *testing.T) {
		newAbortFixture(t)
		if abortResumeHolds("ab1", blocked, now) {
			t.Error("再送で直らない中断を抑止している")
		}
	})
	t.Run("設定 OFF では抑止しない", func(t *testing.T) {
		newAbortFixture(t)
		setAbortResumePref(t, false)
		if abortResumeHolds("ab1", retryableAbort(now), now) {
			t.Error("OFF なのに抑止している（従来の報告経路が塞がれる）")
		}
	})
	t.Run("開いているエピソードは抑止する", func(t *testing.T) {
		newAbortFixture(t)
		_ = abortResumeStates.Write("ab1", abortResumeState{At: now.Format(time.RFC3339), Attempts: 1})
		if !abortResumeHolds("ab1", retryableAbort(now), now.Add(2*time.Minute)) {
			t.Error("再開の途中なのに抑止していない")
		}
	})
	t.Run("TTL を過ぎたエピソードは抑止しない", func(t *testing.T) {
		newAbortFixture(t)
		_ = abortResumeStates.Write("ab1", abortResumeState{At: now.Format(time.RFC3339)})
		if abortResumeHolds("ab1", retryableAbort(now), now.Add(abortResumeEpisodeTTL+time.Minute)) {
			t.Error("進んでいないエピソードが報告を抑止し続けている")
		}
	})
}

// TestAbortResumeTickClosesEpisode: 末尾が中断でなくなったらエピソードを畳む。
// これが「クリーンな完了で再試行の予算が戻る」仕組みそのもの（別カウンタを持たない）。
func TestAbortResumeTickClosesEpisode(t *testing.T) {
	newAbortFixture(t)
	m := abMeta()
	session.WriteMeta(m)
	_ = abortResumeStates.Write(m.Name, abortResumeState{At: time.Now().Format(time.RFC3339), Attempts: 2})
	orig := claudeAbortInfo
	claudeAbortInfo = func(string) (claude.Abort, bool) { return claude.Abort{}, false }
	t.Cleanup(func() { claudeAbortInfo = orig })

	abortResumeTick(time.Now())
	if _, ok := abortResumeStates.Read(m.Name); ok {
		t.Error("中断が末尾から消えてもエピソードが残っている（次の中断で予算が無い）")
	}
}

// TestAbortResumePromptIsShort: 再開プロンプトは一語＋注記。長い指示文にしないのは、
// 中断は数十秒前の出来事で文脈がそのまま残っているから。括弧を残すのは、利用者が自分で
// 打つ「続けて」と区別するため（注入元の照合は本文の完全一致）。
func TestAbortResumePromptIsShort(t *testing.T) {
	for _, locale := range []string{"ja", "en"} {
		p := abortResumePromptFor(locale)
		if len([]rune(p)) > 25 {
			t.Errorf("%s の再開プロンプトが長い（%d 文字）: %q", locale, len([]rune(p)), p)
		}
		if p == "続けて" || p == "continue" {
			t.Errorf("%s の再開プロンプトが素の一語 — 利用者の入力と区別できない: %q", locale, p)
		}
	}
}
