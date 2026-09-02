package chatx

// 指示台帳 — セッション報告 v2 Phase 2（docs/log/51 §データモデル / ADR 0035 決定1）。
//
// v1 は「指示1件 = arm の1bit（session-report/<name>.json）」だった。1bit には
// **同一性が無い**ので、指示が重なった瞬間に定義から事故が生まれる:
//
//   - 穴A: キュー投入で指示2を re-arm すると指示1の arm を上書きする。ターン1の Stop が
//     その bit を消費した時点で、指示2の完了は誰にも報告されない。
//   - 穴B: 消費側（v1 の waiter / kick）に「どの指示の分を消費したのか」を検証する手段が
//     無い（世代が無い）。誤って別指示の分を消費しても検出できない。
//
// Phase 2 は bit を**台帳の行**に置き換える。指示1件 = 1行で、投入は必ず**追加**
// （上書きしない）。行IDが同一性そのものなので:
//
//   - 穴A は定義から消える。先行指示の行が reported になっても後行指示の行は pending の
//     まま残り、その完了で改めて報告される（TestInstrLedgerQueuedInstructionSurvives）。
//   - 穴B（世代なし消費）と hint の誤着も消える。リコンサイラは「行ID の集合」を報告し、
//     その集合だけを reported に遷移させる。どの行の分かが常に書かれている。
//   - 配送はシンク側（会話ロック下）で行IDにより冪等化できる（v1 は「1回だけ」を検出側の
//     不可逆消費で担保していた — 追記に失敗すれば報告ごと消えた）。
//
// 状態機械: pending →(interim 報告)→ interim_reported →(完了/異常)→ reported
//           reported →(補償・Phase 3)→ reopened →…→ reported
//           pending/interim_reported →(stop_session の disarm)→ cancelled
// open（＝まだ報告義務が残っている）= pending | interim_reported | reopened。

import (
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// 行の状態（docs/log/51 §データモデル）。
const (
	instrPending   = "pending"
	instrInterim   = "interim_reported" // 質問/プラン の途中経過を報告済み（**非消費**）
	instrReported  = "reported"
	instrReopened  = "reopened"  // 誤「完了」の補償で開き直した行（Phase 3）
	instrCancelled = "cancelled" // stop_session = 指示の取り消し
)

// instrCursor is the row's progress cursor: 「この時刻より後にセッションが働いた証拠」を
// 完了報告の前提にするための下限（docs/log/51 §progressed）。Phase 2 は投入時刻1本
// （＝ Phase 1 の arm 時刻と同じ意味）で、kind 別の濃いカーソル（jsonl サイズ・ターン
// 連番）は後続の課題。**秒精度の RFC3339 で持つ**のが肝で、比較相手の status マーカーも
// 秒精度 — ここを nano にすると「投入と同じ秒に終わった速いターン」が永久に settle
// しなくなる（マーカーがカーソルより前に見える）。
type instrCursor struct {
	At string `json:"at"` // RFC3339（秒）
}

// instrInterimAt records that this row already carried an interim report. 非消費の
// 意味論は v1 と同一（完了のワンショットは温存される）— ここは「既報の記録」であって
// 抑止ではない: 1つの指示の中で質問が2回起きるのは普通で、2回目を握り潰すと
// オペレーターが答えられなくなる。
type instrInterimAt struct {
	QuestionAt string `json:"question_at,omitempty"`
	PlanAt     string `json:"plan_at,omitempty"`
}

// instrRow is one instruction (docs/log/51 §データモデル).
type instrRow struct {
	ID          string         `json:"id"`     // 行ID（配送の冪等キー）
	Conv        string         `json:"conv"`   // 報告先の会話
	Source      string         `json:"source"` // operator | schedule | schedule-manual …
	DeliveredAt string         `json:"delivered_at"`
	Cursor      instrCursor    `json:"cursor"`
	State       string         `json:"state"`
	Interim     instrInterimAt `json:"interim,omitempty"`
	ReportedAt  string         `json:"reported_at,omitempty"`
	ReopenCount int            `json:"reopen_count,omitempty"`
}

// open reports whether the row still owes a completion report.
func (r instrRow) open() bool {
	switch r.State {
	case instrPending, instrInterim, instrReopened:
		return true
	}
	return false
}

// instrLedger is the per-session file: rows in投入順.
type instrLedger struct {
	Rows []instrRow `json:"rows"`
}

var instrLedgers = fstore.JSON[instrLedger](paths.AgentConfigDir, "instr-ledger", ".json")

// instrClosedKeep bounds the history kept per session: open 行は必ず残し、閉じた行は
// 新しい方から instrClosedKeep 件だけ残す（reopen の補償・調査に足りる量。長寿の
// セッションが何百回も steer されても台帳が太らない）。
const instrClosedKeep = 20

// 台帳の read-modify-write はセッション単位で直列化する（docs/log/51: 書き手はサーバ
// プロセス内の投入ハンドラとリコンサイラだけ）。臨界区間はファイル1枚の read+write
// だけで、配送（会話ロック・provider 呼び出し）はこの外側で行う。
var (
	instrLocksMu sync.Mutex
	instrLocks   = map[string]*sync.Mutex{}
)

func lockInstr(name string) func() {
	instrLocksMu.Lock()
	mu, ok := instrLocks[name]
	if !ok {
		mu = &sync.Mutex{}
		instrLocks[name] = mu
	}
	instrLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// newInstrID mints a row id. 短い id で十分（衝突面はセッション1件の台帳の中だけ）だが、
// 予測可能である必要も無いので randUUID の乱数から取る。
func newInstrID() string {
	return "i-" + strings.ReplaceAll(randUUID(), "-", "")[:10]
}

// readInstrRows returns the session's ledger rows (投入順).
func readInstrRows(name string) []instrRow {
	l, ok := instrLedgers.Read(name)
	if !ok {
		return nil
	}
	return l.Rows
}

// openInstrRows returns the rows that still owe a report, oldest cursor first —
// リコンサイラはこの順に依存する（先頭 = 最古の指示 = 証拠の下限）。
func openInstrRows(name string) []instrRow {
	var out []instrRow
	for _, r := range readInstrRows(name) {
		if r.open() && r.Conv != "" {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Cursor.At < out[j].Cursor.At })
	return out
}

// sessionReportPending reports whether the session owes at least one report — hook /
// record-exit プロセスが「そもそも kick する意味があるか」を見るのに使う
// （v1 の reportArmed の後継）。
func sessionReportPending(name string) bool { return len(openInstrRows(name)) > 0 }

// writeInstrRows persists the rows with the retention trim. 呼び出し側が lockInstr を
// 保持していること。
func writeInstrRows(name string, rows []instrRow) {
	var open, closed []instrRow
	for _, r := range rows {
		if r.open() {
			open = append(open, r)
		} else {
			closed = append(closed, r)
		}
	}
	if len(closed) > instrClosedKeep {
		closed = closed[len(closed)-instrClosedKeep:]
	}
	if len(open) == 0 && len(closed) == 0 {
		instrLedgers.Remove(name)
		return
	}
	// 追加順（＝ DeliveredAt 順）を保つ: 閉じた行は必ず開いている行より前に投入された
	// わけではないので、時刻で並べ直す。
	merged := append(append([]instrRow{}, closed...), open...)
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].DeliveredAt < merged[j].DeliveredAt })
	_ = instrLedgers.Write(name, instrLedger{Rows: merged})
}

// addInstruction records one delivered instruction as a NEW ledger row (docs/log/51 §移行
// Phase 2: arm の書込み箇所を行追加へ)。create_session（report_to 付き）と report_to を
// 運ぶ /input・/turn の成功時に呼ぶ。**既存行は絶対に触らない** — 重なった指示が
// 潰れないことがこの置き換えの目的そのもの（穴A）。
func addInstruction(name, convID, source string) string {
	return addInstructionAt(name, convID, source, time.Now())
}

// addInstructionAt is addInstruction with an explicit delivery time (テストが投入と
// 証拠の前後関係を決定的に組むための seam)。
func addInstructionAt(name, convID, source string, at time.Time) string {
	if !session.ValidName(name) || !paths.ValidIDSegment(convID) {
		return ""
	}
	if _, err := loadConv(convID); err != nil {
		return "" // unknown conversation — 宛先の無い行は作らない（v1 の arm と同じ判断）
	}
	ts := at.Format(time.RFC3339)
	row := instrRow{
		ID: newInstrID(), Conv: convID, Source: source,
		DeliveredAt: ts, Cursor: instrCursor{At: ts}, State: instrPending,
	}
	unlock := lockInstr(name)
	writeInstrRows(name, append(readInstrRows(name), row))
	unlock()
	// 新しい指示 = 判定のやり直し。前の指示で溜まったデバウンス（静穏カウント）や中断
	// ヒントを持ち越すと、走り出す前のセッションを「完了」と読みかねない。
	reportRec.forget(name)
	return row.ID
}

// markInstrReported closes the given rows after a SUCCESSFUL delivery (deliver-then-
// consume)。行IDで指定するので、配送してからここへ戻るまでに追加された指示の行は
// 巻き添えにならない（穴B の「世代なし消費」がここで構造的に消える）。
func markInstrReported(name string, ids []string, at time.Time) {
	if len(ids) == 0 {
		return
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	unlock := lockInstr(name)
	defer unlock()
	rows := readInstrRows(name)
	for i := range rows {
		if want[rows[i].ID] && rows[i].open() {
			rows[i].State = instrReported
			rows[i].ReportedAt = at.Format(time.RFC3339)
		}
	}
	writeInstrRows(name, rows)
}

// markInstrInterim stamps the interim (non-consuming) report on every open row: 質問 /
// プランは「この指示の途中経過」なので、その時点で開いている行に既報として刻む。
// 状態は interim_reported へ進むが **open のまま** — 完了報告の義務は残る。
func markInstrInterim(name, kind string, at time.Time) {
	unlock := lockInstr(name)
	defer unlock()
	rows := readInstrRows(name)
	ts := at.Format(time.RFC3339)
	for i := range rows {
		if !rows[i].open() {
			continue
		}
		switch kind {
		case "question":
			rows[i].Interim.QuestionAt = ts
		case "plan-approval":
			rows[i].Interim.PlanAt = ts
		default:
			continue
		}
		if rows[i].State == instrPending {
			rows[i].State = instrInterim
		}
	}
	writeInstrRows(name, rows)
}

// instrReopenMax caps how often one row may be re-opened by the compensation path
// （docs/log/51 §補償）。上限に達した行は「判定が振動している」ので開き直さない。
const instrReopenMax = 2

// reopenInstrRow re-opens a reported row so the real completion gets another report
// （docs/log/51 §補償 — 誤「完了」の自己修復）。遷移を引く検出（reported 行の grace 監視中に
// busy 証拠が復活したか）と訂正の配送は reportReconciler.compensate（Phase 3）にあり、
// ここは台帳側の遷移だけを持つ。**訂正を配送してから**呼ぶこと: 逆順にすると、訂正が
// 配送できなかったときに「黙って開き直しただけ」になり、v1 の消失と同じ見え方になる。
func reopenInstrRow(name, id string) bool {
	unlock := lockInstr(name)
	defer unlock()
	rows := readInstrRows(name)
	for i := range rows {
		if rows[i].ID != id || rows[i].State != instrReported {
			continue
		}
		if rows[i].ReopenCount >= instrReopenMax {
			return false // 振動している — 開き直さず打ち切る（Phase 3 が利用者へ報告）
		}
		rows[i].State = instrReopened
		rows[i].ReopenCount++
		rows[i].ReportedAt = ""
		writeInstrRows(name, rows)
		return true
	}
	return false
}

// cancelInstructions marks every open row cancelled — オペレーターの stop_session
// （halt + disarm_report）は「指示の取り消し」なので、後日ユーザーが再開して完了しても
// 古い報告は届かない。Console の停止（body なし）はこれを呼ばず、行を残す。
func cancelInstructions(name string) int {
	unlock := lockInstr(name)
	rows := readInstrRows(name)
	n := 0
	for i := range rows {
		if rows[i].open() {
			rows[i].State = instrCancelled
			n++
		}
	}
	writeInstrRows(name, rows)
	unlock()
	reportRec.forget(name)
	return n
}

// instrPendingSessions lists the sessions with at least one open row — リコンサイラの
// sweep 対象。定常コストは台帳ディレクトリの readdir 1回＋小さな read だけ。
func instrPendingSessions() []string {
	open, _ := instrSweepSessions(time.Now())
	return open
}

// instrSweepSessions splits the ledger directory into the reconciler's two work sets in
// ONE readdir + read pass: 完了を待っている（open 行あり）セッションと、誤「完了」の
// 監視中（grace 内の reported 行あり）セッション（docs/log/51 §補償）。両方に載ることは
// 普通にある — 1件が報告済みでもう1件が pending、という状態がそれ。
func instrSweepSessions(now time.Time) (open, grace []string) {
	ents, err := os.ReadDir(instrLedgers.Dir())
	if err != nil {
		return nil, nil
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if !session.ValidName(name) {
			continue
		}
		rows := readInstrRows(name)
		for _, r := range rows {
			if r.open() && r.Conv != "" {
				open = append(open, name)
				break
			}
		}
		if len(instrReopenCandidates(rows, now, reportReopenGrace)) > 0 {
			grace = append(grace, name)
		}
	}
	return open, grace
}

// --- 行集合のユーティリティ（リコンサイラ / シンクが使う） ------------------------

// instrIDs projects the row ids (台帳の更新対象・ログ).
func instrIDs(rows []instrRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// instrDeliveryKey is the row's IDEMPOTENCY key for the conversation: 行ID＋reopen 世代。
// 世代を混ぜるのは補償（§Phase 3）のため — reopen された行は同じ行IDで**もう一度**
// 報告されなければならないので、行IDだけを鍵にすると「配送済み」と誤判定して本完了の
// 報告を握り潰す。世代 0（通常の指示）は行ID そのもの。
func instrDeliveryKey(r instrRow) string {
	if r.ReopenCount == 0 {
		return r.ID
	}
	return r.ID + "#" + strconv.Itoa(r.ReopenCount)
}

// instrReopenKeySuffix namespaces the COMPENSATION notice's idempotency key away from
// the completion report's (docs/log/51 §補償 / Phase 3)。訂正は「その世代の完了報告」に
// 1対1で対応するので、鍵は同じ行ID＋同じ世代の別名前空間にする。行IDだけを共有すると
// 訂正が「配送済み」の完了報告と衝突し、逆に世代を1つ進めた鍵にすると、次の本完了
// （reopen 後の世代）の報告を握り潰す。
const instrReopenKeySuffix = "~reopen"

// instrDeliveryKeyFor is the row's idempotency key for a report of the given kind.
func instrDeliveryKeyFor(kind string, r instrRow) string {
	if kind == reportKindReopened {
		return instrDeliveryKey(r) + instrReopenKeySuffix
	}
	return instrDeliveryKey(r)
}

func instrKeysFor(kind string, rows []instrRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, instrDeliveryKeyFor(kind, r))
	}
	return out
}

// instrConvs lists the distinct report targets in投入順. 同じセッションを別々の
// オペレーター会話が指示していることがあるので、畳み込みは**会話ごと**に行う
// （1通に混ぜると、片方の会話へもう片方の指示の完了が漏れる）。
func instrConvs(rows []instrRow) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if r.Conv != "" && !seen[r.Conv] {
			seen[r.Conv] = true
			out = append(out, r.Conv)
		}
	}
	return out
}

func instrRowsForConv(rows []instrRow, conv string) []instrRow {
	var out []instrRow
	for _, r := range rows {
		if r.Conv == conv {
			out = append(out, r)
		}
	}
	return out
}

// instrRowsCoveredBy returns the rows whose cursor is not AFTER the settle evidence
// （at = idle マーカー / ExitInfo の時刻）。証拠より後に投入された指示は、その静穏では
// 完了になり得ない — 開いたまま残して次の終端で報告する（穴A）。
// 時刻が読めないときは reportTimeBefore が false を返す＝「覆っている」に倒れる:
// 判定不能を理由に本物の完了証拠を捨てると報告が永久に出ない（v1 の消失モード）。
func instrRowsCoveredBy(rows []instrRow, at string) []instrRow {
	var out []instrRow
	for _, r := range rows {
		if !reportTimeBefore(at, r.Cursor.At) {
			out = append(out, r)
		}
	}
	return out
}

// undeliveredInstrRows filters out the rows whose report of THIS kind already exists in
// the conversation（配送の冪等化 — 呼び出し側は会話ロックを保持していること）。kind を
// 取るのは補償の訂正（docs/log/51 §補償）のため: 訂正と完了報告は同じ行・同じ世代を指すが
// 別々の1通なので、鍵の名前空間を分けないと片方がもう片方を握り潰す。
func undeliveredInstrRows(c *chatConversation, rows []instrRow, kind string) []instrRow {
	if len(rows) == 0 {
		return rows
	}
	done := map[string]bool{}
	for i := range c.Messages {
		if c.Messages[i].Role != "report" {
			continue
		}
		for _, k := range c.Messages[i].Instr {
			done[k] = true
		}
	}
	var out []instrRow
	for _, r := range rows {
		if !done[instrDeliveryKeyFor(kind, r)] {
			out = append(out, r)
		}
	}
	return out
}

// reportedInstrTS finds when the conversation actually carried these rows' completion
// report (unix millis, 0 when not found). **台帳の ReportedAt を使わない**のがこの関数
// の存在理由: 補償は行を reopen した瞬間に ReportedAt を消す（reopenInstrRow）ので、
// 「訂正の対象はいつの報告か」を台帳から読むと、訂正が再試行された second pass や
// 2回目の補償で参照先が消えている。会話メッセージは訂正の対象そのものなので、そこから
// 取れば世代がずれない。
func reportedInstrTS(c *chatConversation, rows []instrRow) int64 {
	want := map[string]bool{}
	for _, r := range rows {
		want[instrDeliveryKey(r)] = true
	}
	var ts int64
	for i := range c.Messages {
		if c.Messages[i].Role != "report" {
			continue
		}
		for _, k := range c.Messages[i].Instr {
			if want[k] && (ts == 0 || c.Messages[i].TS < ts) {
				ts = c.Messages[i].TS
			}
		}
	}
	return ts
}

// instrReopenCandidates returns the reported rows still inside the compensation grace
// window (docs/log/51 §補償: reported 行を grace 期間監視する)。純関数なので、grace の境界と
// 「新指示があれば補償しない」規則をテーブルで固定できる。
//
// 「**新しい指示行が無いまま**」の実装がここ: セッションが再び busy になった理由が
// 「その報告より後に投入された指示」で説明できるなら、それは誤報告ではなく次の仕事なので
// 補償の対象から外す（外さないと、キュー投入のたびに直前の正しい報告を訂正してしまう）。
func instrReopenCandidates(rows []instrRow, now time.Time, grace time.Duration) []instrRow {
	newest := ""
	for _, r := range rows {
		if r.DeliveredAt > newest {
			newest = r.DeliveredAt
		}
	}
	var out []instrRow
	for _, r := range rows {
		if r.State != instrReported || r.ReportedAt == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, r.ReportedAt)
		if err != nil {
			continue // 時刻が読めない行は監視できない（grace の始点が無い）
		}
		if now.Before(at) || now.Sub(at) > grace {
			continue
		}
		if reportTimeBefore(r.ReportedAt, newest) {
			continue // 報告のあとに新しい指示が来ている — busy は説明が付く
		}
		out = append(out, r)
	}
	return out
}

// instrFoldAts joins the dispatch times of the rows a folded report covers (docs/log/51
// §データモデル): 複数の指示が同じ静穏で完了したときは1通に**明示的に束ねる**（v1 のように
// 潰さない）。「指示N件ぶん」の文言そのものは chat_report_text.go（表示言語で組む）。
func instrFoldAts(rows []instrRow) string {
	ats := make([]string, 0, len(rows))
	for _, r := range rows {
		ats = append(ats, r.DeliveredAt)
	}
	return strings.Join(ats, " / ")
}

// --- v1 arm からの移行 ------------------------------------------------------------

// migrateReportArms converts leftover v1 arm files (session-report/<name>.json,
// armed=true) into one ledger row each, then removes them（docs/log/51 §移行 Phase 2:
// 「起動時に既存 armed=true を1行に変換」）。起動時に1回だけ走る。
//
// 変換後に v1 ファイルを消すのは、再起動のたびに同じ arm を再変換して行が増えるのを
// 防ぐため。代償は「Phase 1 バイナリへロールバックすると、移行済みの未完了指示の報告が
// 出ない」こと（指示を出し直せば復旧する）— 二重報告より軽い方に倒した。
func migrateReportArms() {
	ents, err := os.ReadDir(reportLinks.Dir())
	if err != nil {
		return
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if !session.ValidName(name) {
			continue
		}
		l, ok := reportLinks.Read(name)
		if !ok {
			continue
		}
		if l.Armed && l.Conv != "" && !sessionReportPending(name) {
			at := l.At
			if _, err := time.Parse(time.RFC3339, at); err != nil {
				at = time.Now().Format(time.RFC3339)
			}
			unlock := lockInstr(name)
			writeInstrRows(name, append(readInstrRows(name), instrRow{
				ID: newInstrID(), Conv: l.Conv, Source: "v1-arm",
				DeliveredAt: at, Cursor: instrCursor{At: at}, State: instrPending,
			}))
			unlock()
			n++
		}
		reportLinks.Remove(name)
	}
	if n > 0 {
		log.Printf("session-report: v1 の arm %d 件を指示台帳へ移行した（docs/log/51 Phase 2）", n)
	}
}
