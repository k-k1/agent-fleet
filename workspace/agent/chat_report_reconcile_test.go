package main

// 消費判定リコンサイラ（docs/log/51 Phase 1 / ADR 0035）のテスト。
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
	"strings"
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

// awaitReported polls until the session has no open instruction row left. 配送（会話への
// 追記）が成功してから行を reported に進める順序なので、報告カードを見つけた瞬間はまだ
// pending のことがある — 「報告済み」の確認は待って行う。
func awaitReported(t *testing.T, name string) {
	t.Helper()
	for i := 0; i < 150; i++ {
		if !sessionReportPending(name) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("指示行は配送された報告で reported になるべき (指示1件=報告1回): %s", name)
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

// ledgerFixture creates a temp HOME, an operator conversation and a session with an
// EMPTY ledger — 指示の投入時刻をテスト側が決めたいケース（キュー投入・畳み込み）向け。
// 自動ターンは切っておく（実シンクを使うテストが provider を叩かないように）。
func ledgerFixture(t *testing.T, name string) (session.Meta, string, string) {
	t.Helper()
	home := withTempHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json"),
		[]byte(`{"assistantAutoTurn":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	conv := &chatConversation{ID: randUUID(), Agent: "claude", Messages: []chatMessage{}}
	if err := saveConv(conv); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude, Title: "リコンサイラ検証"}
	session.WriteMeta(m)
	return m, session.UUID(m.Dir, m.Name), conv.ID
}

// armedFixture is ledgerFixture plus one instruction row delivered now (v1 の arm 相当).
func armedFixture(t *testing.T, name string) (session.Meta, string, string) {
	t.Helper()
	m, sid, convID := ledgerFixture(t, name)
	addInstruction(m.Name, convID, turnSourceOperator)
	return m, sid, convID
}

// countReportCards counts the report cards in a conversation (会話ロック下で読む —
// saveConv は非アトミックな os.WriteFile なので、素の読みは truncate 中を掴む)。
func countReportCards(t *testing.T, convID string) int {
	t.Helper()
	unlock := lockConv(convID)
	defer unlock()
	c, err := loadConv(convID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for i := range c.Messages {
		if c.Messages[i].Role == "report" {
			n++
		}
	}
	return n
}

// waitPastCursor blocks until the wall clock has passed the row cursor, so the NEXT
// status marker（TS = 現在時刻・秒精度）はその指示より後に書かれたことになる。
// 指示と証拠の前後関係が判定の核心なので、ここだけは実時間を進める必要がある。
//
// ★ 上限は「跨ぐのに必要な最悪時間」より確実に長く取る。比較は **秒精度の文字列**なので、
// カーソルが now+2s のとき跨げるのは now の秒が 3 つ進んだ後＝最悪 3 秒弱かかる。旧実装の
// 上限はちょうど 3 秒（50ms×60）で、ランナーが少し遅れるだけで足りず、product 側は無傷
// なのに CI だけが落ちた（run 32489634863）。テストが自分の時計を測って落ちる型なので、
// 余裕は待ち時間ではなく上限だけに足す（跨いだ瞬間に返るのは変わらない）。
func waitPastCursor(t *testing.T, cursor string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().Format(time.RFC3339) > cursor {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("カーソル %s を跨げなかった", cursor)
		}
		time.Sleep(20 * time.Millisecond)
	}
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
		// 自己申告（docs/log/51 Phase 3 §ファストパス）はマーカーと同格の idle 証拠。
		// 「busy 証拠より強くはしない」ことがファストパスを backbone にしない判断の実体。
		{"自己申告だけでも idle 証拠（マーカーを持たない kind）",
			reportSignals{SelfReported: true, SelfReportAt: "2026-07-29T12:00:00Z", SelfReportAged: true},
			true, reportKindAnswerReady, "", false},
		// 早呼び（申告してから最終回答を書き続ける）は静穏窓が明けるまで待つ。実測
		// sannme2 では申告の 2 分 22 秒後に本物の回答が届き、その間 busy 証拠が全部
		// 消えていた（思考ギャップ 142s > 鮮度 TTL 90s）。
		{"申告直後は申告だけで完了にしない（早呼び）",
			reportSignals{SelfReported: true, SelfReportAt: "2026-07-29T12:00:00Z"},
			false, "", "", false},
		// 中断（docs/log/47）はマーカーを一切見ない idle 証拠。claude は中断で Stop hook を
		// 鳴らさないので、マーカー不在（誤ヒールで消えた実測 sp2qemx）でも報告できること、
		// 分類がそのまま報告の reason になることを固定する。
		{"中断はマーカー不在でも idle 証拠",
			reportSignals{Abort: true, AbortReason: reportReasonTurnAborted, AbortAt: "2026-07-30T00:41:19Z"},
			true, reportKindAnswerReady, reportReasonTurnAborted, false},
		{"再送しても直らない中断は turn-failed で報告",
			reportSignals{Abort: true, AbortReason: reportReasonTurnFailed, AbortAt: "2026-07-30T00:41:19Z"},
			true, reportKindAnswerReady, reportReasonTurnFailed, false},
		{"中断でも busy 証拠が残っていれば待つ（再開済みの可能性）",
			reportSignals{Abort: true, AbortReason: reportReasonTurnAborted, PaneBusy: true},
			false, "", "", false},
		// docs/log/47 §4-6: 再送で直る中断は Agent 自身が先に再開させる。その間の報告は
		// 「もう実行済みの依頼」をアシスタントのターンで送るだけなので出さない。
		{"自動再開が引き受けている中断は報告しない",
			reportSignals{AbortHeld: true}, false, "", "", false},
		// 抑止は**マーカー由来の idle 証拠にも**効かなければならない。中断でも Stop が
		// 鳴る形（利用上限の 429 — docs/log/47 §4-5）ではマーカーが先に idle+turnEnd になる
		// ので、Abort を落とすだけだと「素の完了」として誤報告する。
		{"抑止中はマーカー idle でも完了にしない",
			with(func(s *reportSignals) { s.AbortHeld = true }), false, "", "", false},
		// ただしプロセスが死んでいるなら再開する相手が居ない — 異常終了は抑止より強い。
		{"異常終了は抑止より強い",
			with(func(s *reportSignals) { s.AbortHeld = true; s.Exit = "oom" }),
			true, "exit", "oom", true},
		{"自己申告の早呼びは busy 証拠に止められる",
			with(func(s *reportSignals) {
				s.MarkerState, s.SelfReported = "working", true
			}), false, "", "", false},
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
	calls []string   // "kind:reason" の並び
	rows  [][]string // 各配送が畳んだ指示行のID（Phase 2 — 冪等キー）
	fail  int        // 残り何回 Retry を返すか
}

func (cs *countingSink) sink(name, convID, kind, reason string, rows []instrRow) reportSinkResult {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.calls = append(cs.calls, kind+":"+reason)
	cs.rows = append(cs.rows, instrIDs(rows))
	if cs.fail > 0 {
		cs.fail--
		return reportSinkRetry
	}
	return reportSinkOK
}

// rowIDs returns the ledger row ids the i-th delivery folded.
func (cs *countingSink) rowIDs(i int) []string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if i >= len(cs.rows) {
		return nil
	}
	return cs.rows[i]
}

func (cs *countingSink) count() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return len(cs.calls)
}

// callsSnapshot copies the recorded deliveries under the lock. 素の `cs.calls` は
// リコンサイラの goroutine が書く共有スライスなので、**失敗メッセージの中でも**
// 裸で読んではいけない — advance() 経由の読みは swept チャネルで順序が付くが、
// ポーリングで待つ assert（chat_report_compensate_test.go）にはその辺が無く、
// -race がそこを掴む。
func (cs *countingSink) callsSnapshot() []string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return append([]string(nil), cs.calls...)
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
	if !sessionReportPending(m.Name) {
		t.Fatal("1 tick 目で arm を消費した")
	}

	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 {
		t.Fatalf("2 tick 連続の静穏で配送されるべき: %v", cs.calls)
	}
	if cs.calls[0] != reportKindAnswerReady+":" {
		t.Fatalf("配送 = %q", cs.calls[0])
	}
	if sessionReportPending(m.Name) {
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
	if sessionReportPending(m.Name) {
		t.Fatal("arm が消費されていない")
	}
}

// TestReportReconcilerSinkRetry: 配送に失敗したら台帳（arm）を動かさず、次 tick で
// 再試行する（docs/log/51 §配送・穴D）。v1 は consume-then-deliver だったので、会話への
// 追記が失敗すると arm だけが消えて報告が永久に失われた。
func TestReportReconcilerSinkRetry(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot52")
	cs := countingSink{fail: 2}
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.PersistTurnEnd(sid, "idle")
	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault) // settle → 1 回目の配送（失敗）
	if cs.count() != 1 || !sessionReportPending(m.Name) {
		t.Fatalf("失敗した配送で arm を消費した（calls=%d armed=%v）", cs.count(), sessionReportPending(m.Name))
	}

	clock.advance(t, rc, reportTickDefault) // 2 回目（失敗）
	if cs.count() != 2 || !sessionReportPending(m.Name) {
		t.Fatalf("再試行されていない（calls=%d armed=%v）", cs.count(), sessionReportPending(m.Name))
	}

	clock.advance(t, rc, reportTickDefault) // 3 回目（成功）
	if cs.count() != 3 {
		t.Fatalf("calls = %d, want 3", cs.count())
	}
	if sessionReportPending(m.Name) {
		t.Fatal("配送に成功したら arm を消費する")
	}
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 3 {
		t.Fatalf("成功後も配送し続けている: %d", cs.count())
	}
}

// TestReportReconcilerRecoversWithoutHint はレイテンシの固定（docs/log/51 §トレードオフ）。
// kick（ヒント）が1つも届かなくても — agent 再起動中の消失・フックの死・TUI 文字列
// 契約のドリフト — tick が同じ状態をレベルで見て拾い、**v1 waiter の 90s 待ちより
// 悪化しない**こと。
func TestReportReconcilerRecoversWithoutHint(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot53")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.PersistTurnEnd(sid, "idle") // 完了したが、誰も kick しなかった

	const v1WaiterWait = 90 * time.Second // v1: SubagentBusy の TTL 待ち（docs/log/30）
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
	if sessionReportPending(m.Name) {
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
	if sessionReportPending(m.Name) {
		t.Fatal("arm が消費されていない")
	}
}

// TestReportReconcilerHintCarriesReason: マーカーからは読めない qualifier（中断/失敗の
// 別・docs/log/47）だけはヒントが運ぶ。ヒントが busy 証拠を跨いだら捨てる — その中断は
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

// --- 指示台帳（docs/log/51 Phase 2） -----------------------------------------------------

// TestReportReconcilerQueuedInstructionSurvives は**穴A の解消**を固定する。
// v1 では指示2の re-arm が指示1の arm（1bit）を上書きし、直後に来た指示1の Stop が
// その bit を消費した時点で「指示2の完了は誰にも報告されない」状態になっていた。
// 台帳では指示 = 行なので、先行指示が reported になっても後行指示の行は pending のまま
// 残り、その完了で別途報告される（同一性が bit から行IDへ移ったことの実利そのもの）。
func TestReportReconcilerQueuedInstructionSurvives(t *testing.T) {
	m, sid, conv := ledgerFixture(t, "slot60")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	now := time.Now()
	id1 := addInstructionAt(m.Name, conv, turnSourceOperator, now.Add(-60*time.Second))
	status.PersistTurnEnd(sid, "idle") // 指示1のターンが Stop で終わった
	// キュー投入: その終端より後に届いた指示2。まだ1文字も走っていないので、同じ静穏で
	// 「完了」になってはいけない。
	id2 := addInstructionAt(m.Name, conv, turnSourceOperator, now.Add(2*time.Second))

	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 {
		t.Fatalf("指示1の完了が報告されていない: %v", cs.calls)
	}
	if got := cs.rowIDs(0); len(got) != 1 || got[0] != id1 {
		t.Fatalf("報告が畳んだ行 = %v, want [%s]（後行指示を巻き込んだ）", got, id1)
	}
	open := openInstrRows(m.Name)
	if len(open) != 1 || open[0].ID != id2 || open[0].State != instrPending {
		t.Fatalf("後行指示の行が残っていない: %+v", open)
	}
	// 証拠（マーカー）が指示2より前のままなら、何 tick 回しても報告されない。
	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 {
		t.Fatalf("走り出していない指示を完了として報告した: %v", cs.calls)
	}

	// 指示2のターンが終わる（マーカーが指示2のカーソルより後になる）。
	waitPastCursor(t, open[0].Cursor.At)
	status.PersistTurnEnd(sid, "idle")
	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 2 {
		t.Fatalf("後行指示が別途報告されなかった: %v", cs.calls)
	}
	if got := cs.rowIDs(1); len(got) != 1 || got[0] != id2 {
		t.Fatalf("2通目が畳んだ行 = %v, want [%s]", got, id2)
	}
	if n := len(openInstrRows(m.Name)); n != 0 {
		t.Fatalf("未報告の行が %d 件残っている", n)
	}
}

// TestReportReconcilerFoldsOverlappingInstructions: 同じ静穏に**両方とも覆われる**
// 指示が複数あるときは、スパムせず1通に畳んで全行を reported にする（docs/log/51 §データ
// モデル「潰れる」ではなく「明示的に束ねる」）。
func TestReportReconcilerFoldsOverlappingInstructions(t *testing.T) {
	m, sid, conv := ledgerFixture(t, "slot61")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	now := time.Now()
	id1 := addInstructionAt(m.Name, conv, turnSourceOperator, now.Add(-90*time.Second))
	id2 := addInstructionAt(m.Name, conv, turnSourceOperator, now.Add(-60*time.Second))
	status.PersistTurnEnd(sid, "idle")

	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if cs.count() != 1 {
		t.Fatalf("配送は1通に畳まれるべき: %v", cs.calls)
	}
	got := cs.rowIDs(0)
	if len(got) != 2 || got[0] != id1 || got[1] != id2 {
		t.Fatalf("畳んだ行 = %v, want [%s %s]", got, id1, id2)
	}
	if n := len(openInstrRows(m.Name)); n != 0 {
		t.Fatalf("畳んだのに %d 件が未報告のまま", n)
	}
	// 本文には「指示N件ぶん」と各投入時刻が添えられる（1件のときは何も足さない）。
	rows := readInstrRows(m.Name)
	note := foldFact(len(rows), instrFoldAts(rows), "ja")
	if !strings.Contains(note, "2 件") || !strings.Contains(note, rows[0].DeliveredAt) {
		t.Fatalf("畳み込みの注記 = %q", note)
	}
	// 1件のときは注記そのものが出ない（本文は v1 と1文字も変わらない）。
	single := reportView{kind: reportKindAnswerReady, args: map[string]string{"display": "d", "name": m.Name}}
	if strings.Contains(single.displayText("ja"), "件ぶんの完了") {
		t.Fatal("単一指示の報告本文は v1 と1文字も変えない")
	}
}

// TestReportReconcilerDeliveryIsIdempotent: 同一行IDの重複配送が安全であること
// （docs/log/51 §配送）。deliver-then-consume なので「会話への追記は成功したが、台帳を
// 進める前にプロセスが落ちた」窓が構造的に存在する — 再送はシンク側の行ID照合で
// 二重投稿にならず、台帳だけが前に進む。
func TestReportReconcilerDeliveryIsIdempotent(t *testing.T) {
	m, sid, conv := ledgerFixture(t, "slot62")
	id := addInstruction(m.Name, conv, turnSourceOperator)
	status.PersistTurnEnd(sid, "idle")

	// 1回目: 実シンクで配送だけ済ませ、台帳は進めない（＝落ちた）。
	rows := openInstrRows(m.Name)
	if res := deliverReportCard(m.Name, conv, reportKindAnswerReady, "", rows); res != reportSinkOK {
		t.Fatalf("配送 = %v", res)
	}
	if n := countReportCards(t, conv); n != 1 {
		t.Fatalf("報告カード = %d 枚", n)
	}
	if !sessionReportPending(m.Name) {
		t.Fatal("台帳を進めていないのに行が閉じた")
	}

	// 2回目: リコンサイラが同じ行を再送する。カードは増えず、行は reported になる。
	rc, clock := newFakeReconciler(t, reportTickDefault, deliverReportCard)
	clock.advance(t, rc, reportTickDefault)
	clock.advance(t, rc, reportTickDefault)
	if n := countReportCards(t, conv); n != 1 {
		t.Fatalf("同一行の再送で報告カードが %d 枚に増えた", n)
	}
	awaitReported(t, m.Name)

	// 冪等キーには reopen 世代が混ざる: 補償で開き直した行は「配送済み」と誤判定されず、
	// 本完了でもう一度報告される（行IDだけを鍵にすると Phase 3 が壊れる）。
	if !reopenInstrRow(m.Name, id) {
		t.Fatal("reopen できない")
	}
	reopened := openInstrRows(m.Name)
	if res := deliverReportCard(m.Name, conv, reportKindAnswerReady, "", reopened); res != reportSinkOK {
		t.Fatalf("再開後の配送 = %v", res)
	}
	if n := countReportCards(t, conv); n != 2 {
		t.Fatalf("reopen 後の報告カード = %d 枚, want 2", n)
	}
}
