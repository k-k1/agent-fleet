package chatx

// 補償 reopen と自己申告ファストパス（docs/log/51 Phase 3 / ADR 0035 決定4・5）のテスト。
//
// 二層の分け方は Phase 1・2 と同じ:
//   - 候補選び（instrReopenCandidates）と復帰の証拠（evalReportResumed）は純関数なので、
//     grace の境界・「新指示があれば補償しない」・「報告より前の証拠は数えない」を
//     テーブルで固定する。
//   - 補償の本体（compensate）は**単発観測**なのでデバウンスの時間軸を持たない。tick
//     ループに載せず直接呼ぶ: そうしないと fake clock（決定的な固定時刻）と status
//     マーカー（実時刻）の突き合わせになり、テストが実行時刻に依存する。
//   - ファストパスは「2 tick が 1 tick になる」性質そのものなので、fake clock の
//     tick 駆動で測る。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// --- 純関数 ---------------------------------------------------------------------

// TestInstrReopenCandidates pins WHICH reported rows stay under compensation watch.
func TestInstrReopenCandidates(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	row := func(f func(*instrRow)) instrRow {
		r := instrRow{ID: "i-1", Conv: "c", DeliveredAt: at(-30 * time.Minute),
			Cursor: instrCursor{At: at(-30 * time.Minute)}, State: instrReported, ReportedAt: at(-time.Minute)}
		f(&r)
		return r
	}
	cases := []struct {
		name string
		rows []instrRow
		want int
	}{
		{"grace 内の reported 行は監視対象", []instrRow{row(func(*instrRow) {})}, 1},
		{"grace を過ぎた報告はもう補償しない",
			[]instrRow{row(func(r *instrRow) { r.ReportedAt = at(-11 * time.Minute) })}, 0},
		{"まだ開いている行は補償の対象ではない",
			[]instrRow{row(func(r *instrRow) { r.State = instrPending; r.ReportedAt = "" })}, 0},
		{"取り消された行も対象外",
			[]instrRow{row(func(r *instrRow) { r.State = instrCancelled })}, 0},
		{"報告時刻が読めない行は監視できない（grace の始点が無い）",
			[]instrRow{row(func(r *instrRow) { r.ReportedAt = "???" })}, 0},
		{"報告のあとに新しい指示が来ていれば補償しない（busy の説明が付く）",
			[]instrRow{
				row(func(*instrRow) {}),
				{ID: "i-2", Conv: "c", DeliveredAt: at(-30 * time.Second),
					Cursor: instrCursor{At: at(-30 * time.Second)}, State: instrPending},
			}, 0},
		{"報告より前に投入された行は「新しい指示」ではない",
			[]instrRow{
				row(func(*instrRow) {}),
				{ID: "i-2", Conv: "c", DeliveredAt: at(-5 * time.Minute),
					Cursor: instrCursor{At: at(-5 * time.Minute)}, State: instrReported, ReportedAt: at(-time.Minute)},
			}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := instrReopenCandidates(tc.rows, now, reportReopenGrace); len(got) != tc.want {
				t.Fatalf("候補 = %d 件 (%+v), want %d", len(got), got, tc.want)
			}
		})
	}
}

// TestReportResumedEvidence pins the compensation predicate. settle 述語との違いは
// 「報告より後か」を要求する点で、そこが抜けると報告直後の残り香（前のターンの working
// マーカー）で毎回 reopen する。
func TestReportResumedEvidence(t *testing.T) {
	cases := []struct {
		name string
		sig  reportSignals
		want bool
	}{
		{"報告後に working へ戻った", reportSignals{MarkerState: "working", MarkerAfterArm: true}, true},
		{"報告より前の working マーカーは数えない", reportSignals{MarkerState: "working"}, false},
		{"報告後に質問で止まった", reportSignals{MarkerState: "question", MarkerAfterArm: true}, true},
		{"終端 idle のままなら復帰していない",
			reportSignals{MarkerState: "idle", MarkerTurnEnd: true, MarkerAfterArm: true}, false},
		{"BG サブエージェントが動き出した", reportSignals{SubagentBusy: true}, true},
		{"転写が伸びた（マーカーより後の追記）", reportSignals{TranscriptBusy: true}, true},
		{"ペインに中断アフォーダンス", reportSignals{PaneBusy: true}, true},
		{"未回答の質問が残っている", reportSignals{PendingQuestion: true}, true},
		// 完了の遅着（報告の直後に本物のターン終端が書かれる）を「再開」と読むと、
		// 嘘の訂正＋同内容の再報告になる（2026-07-30 sannme2）。終端の証拠があるときは
		// 鮮度の証拠を無視する。
		{"報告後に終端 idle が来た＝完了の遅着（鮮度が残っていても再開ではない）",
			reportSignals{MarkerState: "idle", MarkerTurnEnd: true, MarkerAfterArm: true,
				TranscriptBusy: true, PaneBusy: true}, false},
		{"報告後にそのターンが中断で終わった＝再開ではない",
			reportSignals{Abort: true, TailAborted: true, AbortReason: reportReasonTurnAborted,
				TranscriptBusy: true}, false},
		// 報告が中断より**後**に出るのは自動再開（docs/log/47 §4-6）の普通の姿: 再送を 2 回
		// 試してから打ち切って報告するので、中断レコードは報告時刻より古い。時刻の下限で
		// 落として鮮度だけを見ると「報告のあとに働き出した」と読み、嘘の訂正を配ってしまう。
		{"報告より古い中断でも再開ではない（自動再開ののちの打ち切り報告）",
			reportSignals{TailAborted: true, TranscriptBusy: true}, false},
		{"何も無い", reportSignals{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := evalReportResumed(tc.sig)
			if (len(ev) > 0) != tc.want {
				t.Fatalf("resumed = %v, want %v", ev, tc.want)
			}
		})
	}
}

// --- 補償の本体 -------------------------------------------------------------------

// reportedFixture builds a session whose single instruction has already been reported
// `ago` before now — 補償の出発点（誤「完了」が出たあとの状態）。
func reportedFixture(t *testing.T, name string, ago time.Duration) (session.Meta, string, string, string) {
	t.Helper()
	m, sid, conv := ledgerFixture(t, name)
	id := addInstructionAt(m.Name, conv, turnSourceOperator, time.Now().Add(-10*time.Minute))
	markInstrReported(m.Name, []string{id}, time.Now().Add(-ago))
	return m, sid, conv, id
}

// stillReported re-closes the row so the next round of compensation has a candidate
// again（本完了ではなく「また早計な報告が出た」状況の再現）。
func stillReported(t *testing.T, name, id string, ago time.Duration) {
	t.Helper()
	markInstrReported(name, []string{id}, time.Now().Add(-ago))
}

func rowByID(t *testing.T, name, id string) instrRow {
	t.Helper()
	for _, r := range readInstrRows(name) {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("行 %s が台帳に無い", id)
	return instrRow{}
}

// newIdleReconciler builds a reconciler that is NOT running its loop — compensate を
// 直接呼ぶテスト用（tick は要らない）。
func newIdleReconciler(sink reportSink) *reportReconciler {
	rc := newReportReconciler(reportTickDefault)
	rc.sink = sink
	return rc
}

// TestReportCompensationReopensOnBusyReturn は ADR 0035 決定4 の本体。v1 では誤って
// arm を消費したら回復不能だった（誤消費＝報告の永久消失）。台帳では報告は行の状態
// でしかないので、grace の間に busy へ戻ったら訂正を配って行を開き直せる。
func TestReportCompensationReopensOnBusyReturn(t *testing.T) {
	m, sid, _, id := reportedFixture(t, "slot70", 30*time.Second)
	var cs countingSink
	rc := newIdleReconciler(cs.sink)

	status.Persist(sid, "working") // 報告のあとにセッションが動き出した

	rc.compensate(m.Name, time.Now())
	if got := cs.callsSnapshot(); len(got) != 1 || got[0] != reportKindReopened+":" {
		t.Fatalf("訂正が配送されていない: %v", got)
	}
	r := rowByID(t, m.Name, id)
	if r.State != instrReopened || r.ReopenCount != 1 {
		t.Fatalf("行が開き直されていない: %+v", r)
	}
	if r.ReportedAt != "" {
		t.Fatalf("reopen した行に報告時刻が残っている: %q", r.ReportedAt)
	}
	if !sessionReportPending(m.Name) {
		t.Fatal("開き直した行は未報告として扱われるべき（本完了で改めて報告する）")
	}
	// 開き直しは1回だけ。行は reopened なので、次の観測ではもう候補にならない。
	rc.compensate(m.Name, time.Now())
	if got := cs.callsSnapshot(); len(got) != 1 {
		t.Fatalf("同じ報告を二度訂正した: %v", got)
	}
}

// TestReportCompensationSkipsWhenNewInstruction: 報告のあとに新しい指示が入っていれば、
// busy はその指示で説明が付く。ここを見落とすと、キュー投入のたびに直前の**正しい**
// 報告を訂正して回ることになる。
func TestReportCompensationSkipsWhenNewInstruction(t *testing.T) {
	m, sid, conv, id := reportedFixture(t, "slot71", 30*time.Second)
	var cs countingSink
	rc := newIdleReconciler(cs.sink)

	addInstructionAt(m.Name, conv, turnSourceOperator, time.Now().Add(-10*time.Second))
	status.Persist(sid, "working")

	rc.compensate(m.Name, time.Now())
	if got := cs.callsSnapshot(); len(got) != 0 {
		t.Fatalf("新指示で走っているセッションを誤報告扱いにした: %v", got)
	}
	if r := rowByID(t, m.Name, id); r.State != instrReported {
		t.Fatalf("行を開き直してしまった: %+v", r)
	}
}

// TestReportCompensationStopsAtCap: 開き直しは行あたり instrReopenMax 回まで。上限に
// 達したら黙って諦めず、「判定が振動している」事実を1回だけ報告して打ち切る
// （docs/log/47 の自動再開上限と同じイディオム）。
func TestReportCompensationStopsAtCap(t *testing.T) {
	m, sid, _, id := reportedFixture(t, "slot72", 30*time.Second)
	var cs countingSink
	rc := newIdleReconciler(cs.sink)

	for i := 1; i <= instrReopenMax; i++ {
		status.Persist(sid, "working")
		rc.compensate(m.Name, time.Now())
		if r := rowByID(t, m.Name, id); r.ReopenCount != i {
			t.Fatalf("%d 回目の reopen で ReopenCount = %d", i, r.ReopenCount)
		}
		stillReported(t, m.Name, id, 30*time.Second) // また早計な報告が出た
	}
	if got := cs.callsSnapshot(); len(got) != instrReopenMax {
		t.Fatalf("訂正の回数 = %d, want %d: %v", len(got), instrReopenMax, got)
	}

	status.Persist(sid, "working")
	rc.compensate(m.Name, time.Now())
	got := cs.callsSnapshot()
	if len(got) != instrReopenMax+1 || got[instrReopenMax] != reportKindReopened+":"+reportReasonReopenCapped {
		t.Fatalf("上限到達が利用者へ報告されていない: %v", got)
	}
	r := rowByID(t, m.Name, id)
	if r.State != instrReported || r.ReopenCount != instrReopenMax {
		t.Fatalf("上限を超えて開き直した: %+v", r)
	}
	if sessionReportPending(m.Name) {
		t.Fatal("打ち切った行が未報告のまま残っている")
	}
}

// TestReportCompensationCorrectionIsIdempotent: 訂正も完了報告と同じく**行ID＋reopen
// 世代**で冪等化される（申し送り②）。訂正を配ってから行を開き直すので、その間に落ちる
// 窓が構造的にあり、再試行が二重投稿になってはいけない。同時に、鍵の名前空間が完了報告と
// 分かれていること — 分けないと、開き直した行の**本完了**の報告が「配送済み」と誤判定
// されて握り潰される。
func TestReportCompensationCorrectionIsIdempotent(t *testing.T) {
	m, sid, conv, id := reportedFixture(t, "slot73", 30*time.Second)
	rc := newIdleReconciler(deliverReportCard)

	// 先に「早計だった完了報告」を会話へ置く（訂正はこれを指す）。
	first := readInstrRows(m.Name)
	if res := deliverReportCard(m.Name, conv, reportKindAnswerReady, "", first); res != reportSinkOK {
		t.Fatalf("完了報告の配送 = %v", res)
	}
	status.Persist(sid, "working")

	rc.compensate(m.Name, time.Now())
	if n := countReportCards(t, conv); n != 2 {
		t.Fatalf("訂正カード込みで 2 枚のはず: %d", n)
	}
	// 訂正は「どの報告の訂正か」を会話メッセージ側の時刻で名指しする（申し送り①）。
	// 台帳の ReportedAt はこの時点で既に消えているので、そこから取っていたら空になる。
	if r := rowByID(t, m.Name, id); r.ReportedAt != "" {
		t.Fatalf("reopen 後も ReportedAt が残っている: %+v", r)
	}
	body := lastReportCard(t, conv)
	if !strings.Contains(body, "早計") || !strings.Contains(body, "訂正の対象:") {
		t.Fatalf("訂正カード = %q", body)
	}

	// 訂正は配ったが reopen する前に落ちた、を再現して再試行する。
	stillReported(t, m.Name, id, 30*time.Second)
	rows := readInstrRows(m.Name)
	for i := range rows {
		rows[i].ReopenCount = 0 // 世代も巻き戻す＝完全な再試行
	}
	unlock := lockInstr(m.Name)
	writeInstrRows(m.Name, rows)
	unlock()

	rc.compensate(m.Name, time.Now())
	if n := countReportCards(t, conv); n != 2 {
		t.Fatalf("訂正が二重投稿された: %d 枚", n)
	}

	// 開き直した行の本完了は、同じ行IDでも改めて報告される（鍵の名前空間が別）。
	reopened := openInstrRows(m.Name)
	if len(reopened) != 1 {
		t.Fatalf("開き直した行が1件ではない: %+v", reopened)
	}
	if res := deliverReportCard(m.Name, conv, reportKindAnswerReady, "", reopened); res != reportSinkOK {
		t.Fatalf("本完了の配送 = %v", res)
	}
	if n := countReportCards(t, conv); n != 3 {
		t.Fatalf("本完了の報告が握り潰された: %d 枚", n)
	}
}

// lastReportCard returns the newest report card's body.
func lastReportCard(t *testing.T, convID string) string {
	t.Helper()
	unlock := lockConv(convID)
	defer unlock()
	c, err := loadConv(convID)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].Role == "report" {
			return c.Messages[i].Content
		}
	}
	t.Fatal("報告カードが無い")
	return ""
}

// --- 自己申告ファストパス -----------------------------------------------------------

// awaitSinkCalls waits until the sink has recorded exactly n deliveries and returns the
// locked snapshot.
//
// **sweep の回数を数えて assert してはいけない**。`swept` は容量1の「1回終わった」通知で
// しかなく、どの sweep のものかを運ばない — 自己申告は `nudge` で起床を1つ積むので、
// 直前の起床が残した通知を掴んで、狙った状態を観測する前に読んでしまう窓がある
// （フレークと、goroutine が書いている最中のスライスを読む -race 検出の両方の原因）。
// 待ちたいのは sweep ではなく**配送**なので、配送そのものをロック越しに待つ。
func awaitSinkCalls(t *testing.T, cs *countingSink, n int) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := cs.callsSnapshot()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("配送が %d 件に届かなかった: %v", n, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// expectSinkQuiet asserts the sink stays at n over a bounded window — 「配送されない
// こと」は待っても確定しないので、時間を区切って観測するしかない。失敗方向は安全側
// （本当に配送されたときだけ落ちる）。
func expectSinkQuiet(t *testing.T, cs *countingSink, n int, why string) {
	t.Helper()
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := cs.callsSnapshot(); len(got) != n {
			t.Fatalf("%s: 配送 = %v, want %d 件", why, got, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSelfReportKickWiresHintSeam: MCP ツールの受信口は既存の kick（POST /chat/report）
// —— 自己申告はそこに乗る「ヒント＋証拠1つ」であって、独自の配送経路ではない。
func TestSelfReportKickWiresHintSeam(t *testing.T) {
	m, _, _ := armedFixture(t, "slot74")
	rc := newIdleReconciler(nil)
	old := reportRec
	reportRec = rc
	t.Cleanup(func() { reportRec = old })

	req := httptest.NewRequest(http.MethodPost, "/chat/report",
		strings.NewReader(`{"name":"slot74","kind":"self-report"}`))
	rec := httptest.NewRecorder()
	handleChatReport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"reported":true`) {
		t.Fatalf("申告そのものが報告として配送された: %s", rec.Body.String())
	}
	if rc.selfReportFor(m.Name) == "" {
		t.Fatal("自己申告が記録されていない（hint seam に繋がっていない）")
	}
}

// TestSelfReportSettlesInOneTick は ADR 0035 決定5 の効能そのもの: 意味的完了を直接
// 測れる唯一のシグナルなので、機械的 idle に課している 2 tick の裏取りを 1 tick に縮める。
func TestSelfReportSettlesInOneTick(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot75")
	var cs countingSink
	rc, _ := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.PersistTurnEnd(sid, "idle")
	rc.selfReport(m.Name, time.Now()) // ツール呼出 → ヒント起床

	// tick を1つも進めずに配送されること＝2 tick のデバウンスを踏んでいないこと
	// （TestNoSelfReportStillSettles が同じ土俵で 2 tick かかることを示す）。
	awaitSinkCalls(t, &cs, 1)
	awaitReported(t, m.Name) // 台帳が進むのは配送が返ったあと
}

// TestNoSelfReportStillSettles: 申告が来なくても、リコンサイラが従来どおり settle で
// 拾う（ファストパスは backbone ではない — ADR 0035 §捨てた案）。同じ土俵で 2 tick。
func TestNoSelfReportStillSettles(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot76")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.PersistTurnEnd(sid, "idle") // 申告は無い

	clock.advance(t, rc, reportTickDefault)
	expectSinkQuiet(t, &cs, 0, "申告なしの 1 tick 目")
	clock.advance(t, rc, reportTickDefault)
	awaitSinkCalls(t, &cs, 1)
	awaitReported(t, m.Name)
}

// TestSelfReportTooEarlyIsHeld: 早呼び（まだ走っているのに申告した）は busy 証拠に
// 止められる。申告は busy より強くない、というのがファストパスを backbone にしない
// 判断の実装そのもの。止まっている間も申告は捨てない — 捨てると「最後の1トークンの
// 直後に呼んだときだけ効く」ものになる。
func TestSelfReportTooEarlyIsHeld(t *testing.T) {
	m, sid, _ := armedFixture(t, "slot77")
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	status.Persist(sid, "working") // まだターンの途中
	rc.selfReport(m.Name, time.Now())
	expectSinkQuiet(t, &cs, 0, "早呼び（起床直後）")
	clock.advance(t, rc, reportTickDefault)
	expectSinkQuiet(t, &cs, 0, "busy のまま 1 tick")

	status.PersistTurnEnd(sid, "idle") // 本当に終わった
	clock.advance(t, rc, reportTickDefault)
	// 申告が生きているので、busy が晴れた最初の tick で配送される（2 tick 待たない）。
	awaitSinkCalls(t, &cs, 1)
	awaitReported(t, m.Name)
}

// TestSelfReportIsIdleEvidenceWithoutMarker: マーカーを持たない kind（TUI ポーリング系）
// でも申告そのものが idle 証拠になる。証拠の時刻は申告時刻なので、申告より後に投入された
// 指示は巻き込まれない。
func TestSelfReportIsIdleEvidenceWithoutMarker(t *testing.T) {
	m, _, conv := ledgerFixture(t, "slot78")
	// 台帳は**リコンサイラを起こす前に**組む。addInstruction は判定をやり直させる
	// （forget）ので、間に申告を挟むと申告が捨てられ、起床も指示の数だけ増えて
	// 「どの sweep を待てばよいか」が決まらなくなる。申告の時刻は引数で作れるので、
	// 呼び出し順ではなくタイムスタンプで前後関係を組めばよい。
	// 申告**だけ**で settle するには「申告から selfReportSettleDelay 以上経っている」
	// ことが要る（早呼び対策）。ここは申告が唯一の証拠になる筋なので、十分に古い申告で組む。
	selfAt := time.Now().Add(-selfReportSettleDelay - time.Minute)
	id1 := addInstructionAt(m.Name, conv, turnSourceOperator, selfAt.Add(-60*time.Second))
	id2 := addInstructionAt(m.Name, conv, turnSourceOperator, selfAt.Add(2*time.Second))

	var cs countingSink
	rc, _ := newFakeReconciler(t, reportTickDefault, cs.sink)
	rc.selfReport(m.Name, selfAt) // 起床はこの1回だけ

	awaitSinkCalls(t, &cs, 1) // マーカーが無くても申告だけで settle する
	if got := cs.rowIDs(0); len(got) != 1 || got[0] != id1 {
		t.Fatalf("報告が畳んだ行 = %v, want [%s]（申告より後の指示を巻き込んだ）", got, id1)
	}
	// 指示1の行が閉じるまで待ってから、残っているのが指示2だけであることを見る。
	var open []instrRow
	for i := 0; i < 150; i++ {
		if open = openInstrRows(m.Name); len(open) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(open) != 1 || open[0].ID != id2 {
		t.Fatalf("後行指示の行だけが残るべき: %+v", open)
	}
	expectSinkQuiet(t, &cs, 1, "申告より後に投入された指示")
}

// TestAutoResumeCountsSessionEventsNotConversations: 自動再開のカウンタ（docs/log/47）は
// セッションの中断イベントを数える。1つの静穏を2つのオペレーター会話へ配ったときに
// 会話数ぶん加算すると、2会話から指示されているセッションは中断1回で上限に届いてしまう。
func TestAutoResumeCountsSessionEventsNotConversations(t *testing.T) {
	m, sid, conv1 := ledgerFixture(t, "slot79")
	conv2 := &chatConversation{ID: randUUID(), Agent: "claude", Messages: []chatMessage{}}
	if err := saveConv(conv2); err != nil {
		t.Fatal(err)
	}
	var cs countingSink
	rc, clock := newFakeReconciler(t, reportTickDefault, cs.sink)

	past := time.Now().Add(-60 * time.Second)
	addInstructionAt(m.Name, conv1, turnSourceOperator, past)
	addInstructionAt(m.Name, conv2.ID, turnSourceOperator, past)
	status.PersistTurnEnd(sid, "idle")
	rc.hint(m.Name, reportKindAnswerReady, reportReasonTurnAborted)
	clock.waitSweep(t, rc) // 静穏 1 回目
	clock.advance(t, rc, reportTickDefault)

	if got := cs.callsSnapshot(); len(got) != 2 {
		t.Fatalf("会話ごとに1通配るべき: %v", got)
	}
	if n := autoResumeAttempts(m.Name); n != 1 {
		t.Fatalf("自動再開カウンタ = %d, want 1（会話数ぶん加算している）", n)
	}
}
