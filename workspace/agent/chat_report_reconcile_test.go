package main

// 消費判定リコンサイラ（docs/51 Phase 1 / ADR 0035）のテスト。
//
// 二層に分けてある:
//   - 述語（evalReportEvidence）は純関数なので、証拠の交差をテーブルで固定する。
//   - リコンサイラ本体は **fake clock** で tick を進める時間駆動テスト。デバウンス
//     （2 tick 連続）・シンク失敗の再試行・ヒント喪失時の回収レイテンシは、どれも
//     「時間が経つと何が起きるか」の性質なので実時間で待たない。

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// --- テスト用のリコンサイラ配線 ---------------------------------------------------

// installReconciler swaps the process-wide reconciler for the test's own and runs it.
func installReconciler(t *testing.T, rc *reportReconciler) *reportReconciler {
	t.Helper()
	old := reportRec
	reportRec = rc
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); rc.run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		reportRec = old
	})
	return rc
}

// withTestReconciler runs the real (wall-clock) reconciler at a tiny interval — used by
// the v1 regression tests, which drive the whole hook → kick → 会話カード の経路.
func withTestReconciler(t *testing.T, interval time.Duration) *reportReconciler {
	t.Helper()
	return installReconciler(t, newReportReconciler(interval))
}

// fakeReportClock drives the reconciler tick by tick.
type fakeReportClock struct {
	mu  sync.Mutex
	now time.Time
	c   chan time.Time
}

func newFakeReportClock() *fakeReportClock {
	return &fakeReportClock{
		now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC),
		c:   make(chan time.Time),
	}
}

func (f *fakeReportClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeReportClock) Ticker(time.Duration) (<-chan time.Time, func()) {
	return f.c, func() {}
}

// advance moves the clock by d, fires exactly one tick and waits for that sweep to
// finish — テストの各行が「1 tick ぶんの判定」に1対1で対応する。
func (f *fakeReportClock) advance(t *testing.T, rc *reportReconciler, d time.Duration) {
	t.Helper()
	select { // 直前の wake 起床が残した完了通知は捨てる
	case <-rc.swept:
	default:
	}
	f.mu.Lock()
	f.now = f.now.Add(d)
	now := f.now
	f.mu.Unlock()
	select {
	case f.c <- now:
	case <-time.After(3 * time.Second):
		t.Fatal("reconciler did not take the tick")
	}
	select {
	case <-rc.swept:
	case <-time.After(3 * time.Second):
		t.Fatal("sweep did not finish")
	}
}

// waitSweep waits for one already-triggered sweep (ヒント起床ぶん) to finish, so the
// tick-driven assertions stay one-to-one with advance().
func (f *fakeReportClock) waitSweep(t *testing.T, rc *reportReconciler) {
	t.Helper()
	select {
	case <-rc.swept:
	case <-time.After(3 * time.Second):
		t.Fatal("wake sweep did not finish")
	}
}

// awaitDisarmed polls until the one-shot arm is consumed. 配送（会話への追記）が
// 成功してから arm を畳む順序なので、報告カードを見つけた瞬間はまだ armed のことが
// ある — 「消費された」の確認は待って行う。
func awaitDisarmed(t *testing.T, name string) {
	t.Helper()
	for i := 0; i < 150; i++ {
		if !reportArmed(name) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("arm must be consumed by the delivered report (指示1件=報告1回): %s", name)
}

// newFakeReconciler wires a fake clock + a counting sink and starts the loop.
func newFakeReconciler(t *testing.T, interval time.Duration, sink reportSink) (*reportReconciler, *fakeReportClock) {
	t.Helper()
	clock := newFakeReportClock()
	rc := newReportReconciler(interval)
	rc.clock = clock
	rc.sink = sink
	installReconciler(t, rc)
	return rc, clock
}

// armedFixture creates a temp HOME, an operator conversation and an armed session.
func armedFixture(t *testing.T, name string) (session.Meta, string, string) {
	t.Helper()
	home := withTempHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	conv := &chatConversation{ID: randUUID(), Agent: "claude", Messages: []chatMessage{}}
	if err := saveConv(conv); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude, Title: "リコンサイラ検証"}
	session.WriteMeta(m)
	armSessionReport(m.Name, conv.ID)
	return m, session.UUID(m.Dir, m.Name), conv.ID
}

// --- 述語のテーブル ---------------------------------------------------------------

// TestReportEvidenceTable pins the settle predicate: idle 証拠 ≥1 ∧ busy 証拠 = 0。
// 「無マーカー＝idle」の既定を廃したこと（v1 waiter の敗因）と、interim 状態・BG 実行中
// を busy 証拠として畳んだこと（saga5uc 対策の waiter 特例の行き先）が本体。
func TestReportEvidenceTable(t *testing.T) {
	idle := reportSignals{MarkerState: "idle", MarkerTurnEnd: true, MarkerAfterArm: true}
	with := func(f func(*reportSignals)) reportSignals {
		s := idle
		f(&s)
		return s
	}
	cases := []struct {
		name  string
		sig   reportSignals
		quiet bool
		kind  string
		reasn string
		term  bool
	}{
		{"明示 idle マーカー", idle, true, reportKindAnswerReady, "", false},
		{"マーカー不在は不明（idle ではない）", reportSignals{}, false, "", "", false},
		{"ターン終端でない idle（boot / runtime 喪失）は不明",
			with(func(s *reportSignals) { s.MarkerTurnEnd = false }), false, "", "", false},
		{"指示より前の idle は今回の完了ではない",
			with(func(s *reportSignals) { s.MarkerAfterArm = false }), false, "", "", false},
		{"working マーカー", with(func(s *reportSignals) { s.MarkerState = "working" }), false, "", "", false},
		{"質問待ち（interim）", with(func(s *reportSignals) { s.MarkerState = "question" }), false, "", "", false},
		{"プラン承認待ち（interim）", with(func(s *reportSignals) { s.MarkerState = "plan" }), false, "", "", false},
		{"許可待ち（interim）", with(func(s *reportSignals) { s.MarkerState = "permission" }), false, "", "", false},
		{"pending 質問ペイロードが残っている",
			with(func(s *reportSignals) { s.PendingQuestion = true }), false, "", "", false},
		{"BG サブエージェント実行中（saga5uc）",
			with(func(s *reportSignals) { s.SubagentBusy = true }), false, "", "", false},
		{"メイン transcript が新鮮（思考ギャップ・sqmconc）",
			with(func(s *reportSignals) { s.TranscriptBusy = true }), false, "", "", false},
		{"ペインに中断アフォーダンス", with(func(s *reportSignals) { s.PaneBusy = true }), false, "", "", false},
		{"意図停止中は arm 温存", with(func(s *reportSignals) { s.Stopped = true }), false, "", "", false},
		{"中断ヒント付きの完了",
			with(func(s *reportSignals) { s.HintReason = reportReasonTurnAborted }),
			true, reportKindAnswerReady, reportReasonTurnAborted, false},
		{"異常終了はデバウンスなしの終端",
			with(func(s *reportSignals) { s.Exit = "oom" }), true, "exit", "oom", true},
		{"異常終了は busy 証拠より強い（死んだ直後は転写が新鮮）",
			with(func(s *reportSignals) { s.Exit = "crashed"; s.TranscriptBusy = true }),
			true, "exit", "crashed", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := evalReportEvidence(tc.sig)
			if v.Quiet != tc.quiet || v.Terminal != tc.term {
				t.Fatalf("quiet=%v terminal=%v (want %v/%v) why=%s", v.Quiet, v.Terminal, tc.quiet, tc.term, v.Why)
			}
			if v.Quiet && (v.Kind != tc.kind || v.Reason != tc.reasn) {
				t.Fatalf("kind=%q reason=%q, want %q/%q", v.Kind, v.Reason, tc.kind, tc.reasn)
			}
		})
	}
}

// --- 時間駆動 ---------------------------------------------------------------------

// countingSink records the deliveries and lets the test fail the first N of them.
type countingSink struct {
	mu    sync.Mutex
	calls []string // "kind:reason" の並び
	fail  int      // 残り何回 Retry を返すか
}

func (cs *countingSink) sink(name, convID, kind, reason string) reportSinkResult {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.calls = append(cs.calls, kind+":"+reason)
	if cs.fail > 0 {
		cs.fail--
		return reportSinkRetry
	}
	return reportSinkOK
}

func (cs *countingSink) count() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return len(cs.calls)
}

// TestReportReconcilerSettleDebounce: 静穏を1 tick 観測しただけでは配送せず、2 tick
// 連続（かつ tick 間隔ぶんの時間経過）で初めて配送して arm を消費する。TUI の footer
// 誤読やヒールの一瞬のマーカー消失を「完了」に化けさせないための時間的裏取り。
func TestReportReconcilerSettleDebounce(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot50")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.PersistTurnEnd(sid, "idle") // 指示のターンが Stop で終わった

	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 0 {
		t.Fatalf("1 tick 目で配送した（デバウンスが効いていない）: %v", cs.calls)
	}
	if !reportArmed(m.Name) {
		t.Fatal("1 tick 目で arm を消費した")
	}

	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 {
		t.Fatalf("2 tick 連続の静穏で配送されるべき: %v", cs.calls)
	}
	if cs.calls[0] != reportKindAnswerReady+":" {
		t.Fatalf("配送 = %q", cs.calls[0])
	}
	if reportArmed(m.Name) {
		t.Fatal("配送に成功したら arm を消費する（指示1件=報告1回）")
	}

	// 消費後は何度 tick しても再配送しない。
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 {
		t.Fatalf("消費済みの arm で再配送した: %v", cs.calls)
	}
}

// TestReportReconcilerBusyResetsDebounce: 静穏カウントの途中で busy 証拠が出たら
// やり直し。誤「まだ」は次 tick で自己修正され、誤「完了」は作られない。
func TestReportReconcilerBusyResetsDebounce(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot51")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.PersistTurnEnd(sid, "idle")
	clock.advance(t, rc, reportTickDefault) // 静穏 1 回目

	status.Persist(sid, "working") // 次の指示 / チェーンターンが走り出した
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 0 {
		t.Fatalf("busy 証拠を跨いで settle した: %v", cs.calls)
	}

	status.PersistTurnEnd(sid, "idle") // 本当の完了
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 0 {
		t.Fatalf("リセット後の 1 tick 目で配送した: %v", cs.calls)
	}
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 {
		t.Fatalf("リセット後の 2 tick で配送されるべき: %v", cs.calls)
	}
	if reportArmed(m.Name) {
		t.Fatal("arm が消費されていない")
	}
}

// TestReportReconcilerSinkRetry: 配送に失敗したら台帳（arm）を動かさず、次 tick で
// 再試行する（docs/51 §配送・穴D）。v1 は consume-then-deliver だったので、会話への
// 追記が失敗すると arm だけが消えて報告が永久に失われた。
func TestReportReconcilerSinkRetry(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot52")
	cs := countingSink{fail: 2}
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.PersistTurnEnd(sid, "idle")
	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault) // settle → 1 回目の配送（失敗）
	if cs.count() != 1 || !reportArmed(m.Name) {
		t.Fatalf("失敗した配送で arm を消費した（calls=%d armed=%v）", cs.count(), reportArmed(m.Name))
	}

	clock.advance(t, rc, reportTickDefault) // 2 回目（失敗）
	if cs.count() != 2 || !reportArmed(m.Name) {
		t.Fatalf("再試行されていない（calls=%d armed=%v）", cs.count(), reportArmed(m.Name))
	}

	clock.advance(t, rc, reportTickDefault) // 3 回目（成功）
	if cs.count() != 3 {
		t.Fatalf("calls = %d, want 3", cs.count())
	}
	if reportArmed(m.Name) {
		t.Fatal("配送に成功したら arm を消費する")
	}
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 3 {
		t.Fatalf("成功後も配送し続けている: %d", cs.count())
	}
}

// TestReportReconcilerRecoversWithoutHint はレイテンシの固定（docs/51 §トレードオフ）。
// kick（ヒント）が1つも届かなくても — agent 再起動中の消失・フックの死・TUI 文字列
// 契約のドリフト — tick が同じ状態をレベルで見て拾い、**v1 waiter の 90s 待ちより
// 悪化しない**こと。
func TestReportReconcilerRecoversWithoutHint(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot53")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.PersistTurnEnd(sid, "idle") // 完了したが、誰も kick しなかった

	const v1WaiterWait = 90 * time.Second // v1: SubagentBusy の TTL 待ち（docs/30）
	var elapsed time.Duration
	for elapsed < v1WaiterWait && cs.count() == 0 {
		clock.advance(t, rc, reportTickDefault)
		elapsed += reportTickDefault
	}
	if cs.count() != 1 {
		t.Fatalf("ヒント無しで %v 経っても配送されなかった", elapsed)
	}
	if elapsed > v1WaiterWait {
		t.Fatalf("配送まで %v — v1 waiter の %v より悪化している", elapsed, v1WaiterWait)
	}
	if reportArmed(m.Name) {
		t.Fatal("arm が消費されていない")
	}
	if elapsed != 2*reportTickDefault {
		t.Logf("配送まで %v（2 tick 想定）", elapsed)
	}
}

// TestReportReconcilerExitReportsWithoutDebounce: 異常終了は終端の事実なので、証拠が
// 揃うのを待たずに（＝デバウンスなしで）報告する。ExitInfo をレベルで読むため、kick を
// 持たない経路の異常死も同じ tick で拾える。
func TestReportReconcilerExitReportsWithoutDebounce(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot54")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.Persist(sid, "working") // ターンの途中で
	status.PersistExit(m.Name, status.ExitInfo{
		Reason: "oom", Code: 137, Signal: 9, At: time.Now().Format(time.RFC3339),
	})

	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 || cs.calls[0] != "exit:oom" {
		t.Fatalf("異常終了が1 tick で報告されなかった: %v", cs.calls)
	}
	if reportArmed(m.Name) {
		t.Fatal("arm が消費されていない")
	}
}

// TestReportReconcilerHintCarriesReason: マーカーからは読めない qualifier（中断/失敗の
// 別・docs/47）だけはヒントが運ぶ。ヒントが busy 証拠を跨いだら捨てる — その中断は
// もう「今の状態」ではない。
func TestReportReconcilerHintCarriesReason(t *testing.T) {
	_, sid, _ := armedFixture(t, "slot55")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.PersistTurnEnd(sid, "idle")
	rc.hint("slot55", reportKindAnswerReady, reportReasonTurnAborted)
	clock.waitSweep(t, rc) // ヒント起床の sweep（静穏 1 回目）
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 || cs.calls[0] != reportKindAnswerReady+":"+reportReasonTurnAborted {
		t.Fatalf("中断の qualifier が報告に乗っていない: %v", cs.calls)
	}
}
