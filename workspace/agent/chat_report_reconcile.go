package main

// セッション報告の消費判定 — レベル駆動リコンサイラ（docs/51 Phase 1 / ADR 0035）。
//
// v1（docs/30）は「Stop フック等のエッジを1回だけ捕まえ、不可逆な1bit（arm）を
// 消費して報告する」構造で、機械的 idle と意味的完了のズレが出るたびに報告が消えた:
//   - 2026-07-24 saga5uc: BG サブエージェント起動直後の Stop が arm を消費し、
//     数十分後の本完了が二度と報告されなかった → 保留 waiter を追加。
//   - 2026-07-28 sqmconc: その waiter が、誤 idle ヒールでマーカーが消えた十数秒を
//     「完了」と誤認して arm を消費 → 本完了は armed=false で棄却。
//
// Phase 1 は arm の1bit（session-report/<name>.json）はそのまま残し、**消費の判定
// だけ**をここへ一本化する:
//
//   - フック / notify seam / record-exit の kick（POST /chat/report）は「起床ヒント」
//     に降格する。kick 自身は何も配送しないし、何も消費しない。
//   - サーバ内の単一 goroutine が tick（既定 15s）＋ヒント起床で、armed なセッション
//     だけを**レベル**（今ディスクに書かれている状態）で再評価する。
//   - settle 述語 =「idle 証拠 ≥1 ∧ busy 証拠 = 0」を 2 tick 連続。**無マーカーは
//     「不明」であって idle ではない**（v1 waiter の敗因の恒久化）。
//   - 配送（会話への追記）が成功して初めて arm を消費する。consume-then-deliver を
//     やめるので、追記に失敗した報告は次 tick で再試行される。
//
// 効果は「取りこぼしが報告の消失ではなく報告の遅延に縮退する」こと: ヒントが全部
// 死んでいても（agent 再起動中の kick 消失・TUI 文字列契約のドリフト）、次の tick が
// 同じ状態を見て拾う。判定は冪等で再評価可能なので、誤「まだ」は次 tick で自己修正
// される（誤「完了」の補償 reopen は Phase 3）。

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

const (
	// reportTickDefault is the reconciler's sweep cadence. 定常コストは「armed な
	// セッションが1件も無ければ readdir 1回」なので短めでよいが、settle には 2 tick
	// 連続の静穏を要求するため、ヒント喪失時の配送遅延はおよそ 1〜2 tick になる
	// （15〜30s — v1 waiter の 90s TTL 待ちより悪化しない: docs/51 §トレードオフ）。
	reportTickDefault = 15 * time.Second
	// reportSettleTicks is the debounce: 静穏をこの回数だけ連続で観測して初めて
	// settle する。TUI ポーリング系（kiro/cursor 等）の footer 1回誤読や、ヒール
	// による一瞬のマーカー消失を「完了」に化けさせないための時間的な裏取り。
	reportSettleTicks = 2
)

// reportSignals is the LEVEL snapshot the settle predicate consumes: 判定に使う値を
// 一度に読み出して構造体にしておくことで、述語自体を純関数（＝テーブル駆動テスト
// 可能）に保つ。収集は collectReportSignals（ファイル/tmux を触る側）が行う。
type reportSignals struct {
	MarkerState    string // status マーカーの state。"" = ファイル無し＝**不明**
	MarkerTurnEnd  bool   // その idle が「ターンの終端」で書かれたか（status.TurnEnd）
	MarkerAfterArm bool   // マーカーが指示（arm）以降に書かれたか＝最小の progressed

	PendingQuestion   bool // 質問待ち（interim — そもそも完了ではない）
	PendingPlan       bool // プラン承認待ち
	PendingPermission bool // ツール許可待ち

	SubagentBusy   bool // BG サブエージェント / Workflow の jsonl 鮮度（claude）
	TranscriptBusy bool // メイン transcript の鮮度（claude・思考ギャップ対策）
	PaneBusy       bool // ペインの中断アフォーダンス（tmuxx.IsBusy・TUI のみ）

	Stopped bool   // 意図停止（arm は温存 — 再開後の完了で報告する v1 規約）
	Exit    string // 指示以降の異常終了（oom/crashed/killed）— 終端の事実

	HintReason string // 直近のヒントが運んだ qualifier（turn-failed / turn-aborted）
}

// reportVerdict is the predicate's answer for one session at one sweep.
type reportVerdict struct {
	Quiet    bool   // この sweep で「idle 証拠 ≥1 ∧ busy 証拠 = 0」だったか
	Terminal bool   // 異常終了 — デバウンス不要（プロセスは既に死んでいる）
	Kind     string // 報告 kind（answer-ready / exit）
	Reason   string // 報告 reason（turn-failed / turn-aborted / oom …）
	Why      string // 判定根拠（ログ用の証拠名）
}

// busyEvidence lists the signals that forbid a settle. 1つでもあれば「まだ」。
func (s reportSignals) busyEvidence() []string {
	var ev []string
	switch s.MarkerState {
	case "working":
		ev = append(ev, "marker-working")
	case "question", "plan", "permission":
		// interim 状態はそもそも完了ではない（v1 でも arm を消費しない）。
		ev = append(ev, "marker-"+s.MarkerState)
	}
	if s.PendingQuestion {
		ev = append(ev, "pending-question")
	}
	if s.PendingPlan {
		ev = append(ev, "pending-plan")
	}
	if s.PendingPermission {
		ev = append(ev, "pending-permission")
	}
	if s.SubagentBusy {
		ev = append(ev, "subagent-busy")
	}
	if s.TranscriptBusy {
		ev = append(ev, "transcript-busy")
	}
	if s.PaneBusy {
		ev = append(ev, "pane-busy")
	}
	return ev
}

// idleEvidence lists the signals that positively say "the instruction's turn ended".
// **最低1つ必要** — 何も無い状態（マーカー不在・状態不明）を idle と既定しない。
// 求めるのは3点そろった明示 idle:
//   - マーカーが実在して state=="idle"（Stop フック / MarkTurnEnd が書いた）
//   - それが「ターンの終端」由来（TurnEnd）。SessionStart の idle リセットや managed の
//     runtime 喪失（TurnUnknown）も同じ "idle" を書くので、状態文字列だけでは
//     「終わった」と「分からない」が区別できない。
//   - その書込みが指示（arm）以降であること＝最小の progressed。前のターンの終端
//     マーカーが残っているだけの状態を「今回の指示の完了」と読まないための下限。
func (s reportSignals) idleEvidence() []string {
	if s.MarkerState == "idle" && s.MarkerTurnEnd && s.MarkerAfterArm {
		return []string{"marker-idle"}
	}
	return nil
}

// evalReportEvidence is the settle predicate（docs/51 §settled/progressed 述語）。
// 純関数: 引数の証拠だけで決まる。2 tick 連続のデバウンスは呼び出し側（リコンサイラ）
// の責務で、ここは1 sweep ぶんの「静穏か」を返す。
func evalReportEvidence(s reportSignals) reportVerdict {
	// 異常終了は終端の事実。プロセスは死んでいるので busy 証拠の消滅を待つ意味がなく
	// （死ぬ直前まで transcript は新鮮なままになる）、デバウンスもしない。ExitInfo を
	// レベルで読むので、kick を持たない managed daemon の異常死も同じ経路で拾える。
	if s.Exit != "" {
		return reportVerdict{Quiet: true, Terminal: true, Kind: "exit", Reason: s.Exit, Why: "exit:" + s.Exit}
	}
	if s.Stopped {
		// 停止＝指示の取り消しではない（取り消しは stop_session の disarm）。arm は
		// 温存し、再開後の完了で報告する（docs/30 の規約）。
		return reportVerdict{Why: "stopped"}
	}
	if busy := s.busyEvidence(); len(busy) > 0 {
		return reportVerdict{Why: "busy:" + strings.Join(busy, ",")}
	}
	idle := s.idleEvidence()
	if len(idle) == 0 {
		return reportVerdict{Why: "unknown"} // 不明は不明のまま — idle と既定しない
	}
	return reportVerdict{
		Quiet: true, Kind: reportKindAnswerReady, Reason: s.HintReason,
		Why: "idle:" + strings.Join(idle, ","),
	}
}

// collectReportSignals reads the current level state for one armed session.
func collectReportSignals(m session.Meta, link reportLink, hintReason string) reportSignals {
	sid := session.UUID(m.Dir, m.Name)
	s := reportSignals{Stopped: m.StoppedAt != "", HintReason: hintReason}
	var markerAt time.Time
	if st, ok := status.Read(sid); ok {
		s.MarkerState, s.MarkerTurnEnd = st.State, st.TurnEnd
		s.MarkerAfterArm = !reportTimeBefore(st.TS, link.At)
		if t, err := time.Parse(time.RFC3339, st.TS); err == nil && st.TurnEnd {
			markerAt = t
		}
	}
	if q, ok := status.ReadPendingQuestion(sid); ok && len(q) > 0 {
		s.PendingQuestion = true
	}
	if p, ok := status.ReadPendingPlan(sid); ok && p != "" {
		s.PendingPlan = true
	}
	if p, ok := status.ReadPendingPermission(sid); ok && p != "" {
		s.PendingPermission = true
	}
	// サブエージェント / メイン transcript の鮮度は claude 固有のシグナル
	// （他 kind には相当する転写が無い）。
	if normalizeKind(m.Kind) == session.KindClaude {
		s.SubagentBusy = claude.SubagentBusy(sid)
		s.TranscriptBusy = reportTranscriptBusy(sid, markerAt)
	}
	if e, ok := status.ReadExit(m.Name); ok {
		switch e.Reason {
		case "oom", "crashed", "killed":
			// 指示より前の死は前の指示の話（起動時に baseline が Reason を消すが、
			// 記録が残っていても取り違えないよう時刻でも切る）。ここだけは時刻が
			// **読めること**を要求する: 異常終了はデバウンス無しで arm を消費するので、
			// 判定不能を「今回の死」に倒すと生きているセッションへ誤報告しかねない
			// （書き手は全経路 At を必ず入れる）。
			if _, err := time.Parse(time.RFC3339, e.At); err == nil && !reportTimeBefore(e.At, link.At) {
				s.Exit = e.Reason
			}
		}
	}
	return s
}

// reportMarkerGrace absorbs the RFC3339 second-truncation of the status marker when
// comparing it against a file mtime: 「Stop フックのマーカー」と「そのターン最後の
// 転写書込み」は同じ1秒に収まることがあるので、この余裕より後の追記だけを
// 「マーカーの後にも伸びた」と読む。
const reportMarkerGrace = 2 * time.Second

// reportTranscriptBusy reports whether the main transcript says the turn is still
// running. v1 の waiter は素の鮮度（90s）だけを見ていた — Stop の裏付けを持たない
// 立場ではそれしか手が無かったが、それを完了判定の常設ゲートにすると**正常な完了
// 報告が毎回 90s 遅れる**。Phase 1 には「Stop が書いた終端マーカー」という positive な
// 証拠があるので、判定を相対比較にする: マーカーより後にも転写が伸びていれば
// ターンは続いている（＝そのマーカーは終端ではない・sqmconc の思考ギャップ）。
// 鮮度も併せて要求するのは安全弁で、転写が静止したら（＝ v1 と同じ 90s が上限）
// 比較の食い違いで報告が永久に止まることが無いようにするため。
func reportTranscriptBusy(sid string, marker time.Time) bool {
	if !claude.TranscriptBusy(sid) {
		return false // 静止している転写は「実行中」の証拠にならない（上限は v1 と同じ）
	}
	if marker.IsZero() {
		return true // 終端マーカーが無い＝鮮度だけが手がかり（v1 waiter と同じ立場）
	}
	at, ok := claude.TranscriptTouched(sid)
	return ok && at.After(marker.Add(reportMarkerGrace))
}

// reportPaneBusy checks the pane's interrupt affordance (逆ヒールと同じ根拠)。tmux を
// 叩くので settle 候補のときだけ実行する（docs/51: tmux 負荷を抑える）。
// claude の TUI に限るのは tmuxx.IsBusy が claude のスピナー契約を読む実装だから
// （v1 waiter と同じ適用範囲）: 他 kind のペインで誤って busy と読むと、その kind の
// 報告が永久に出ない — 「消失」は遅延より悪い。
func reportPaneBusy(m session.Meta) bool {
	if m.DriverKind() == session.DriverManaged || normalizeKind(m.Kind) != session.KindClaude {
		return false
	}
	return tmuxx.IsBusy(m.Name)
}

// reportTimeBefore reports whether RFC3339 a is strictly before b. 判定不能（空・壊れ）
// は false＝「古くない」に倒す: 時刻が読めないせいで本物の完了証拠を捨てると、報告が
// 永久に出ない（v1 の消失モードそのもの）。
func reportTimeBefore(a, b string) bool {
	ta, err1 := time.Parse(time.RFC3339, a)
	tb, err2 := time.Parse(time.RFC3339, b)
	if err1 != nil || err2 != nil {
		return false
	}
	return ta.Before(tb)
}

// --- シンク（配送） -------------------------------------------------------------

// reportSinkResult is what the sink tells the reconciler about the delivery, and it is
// what decides whether the arm may be consumed（穴D の解消 — 検出側から「1回だけ」の
// 責務を外し、配送が成功したときだけ台帳を進める）。
type reportSinkResult int

const (
	reportSinkOK    reportSinkResult = iota // 追記できた → arm を消費
	reportSinkRetry                         // 追記に失敗 → arm 据え置き・次 tick で再試行
	reportSinkDrop                          // 報告先の会話が消えている → 届け先が無いので arm を畳む
)

// reportSink is the delivery seam (tests substitute a fake to drive the reconciler
// without a conversation store).
type reportSink func(name, convID, kind, reason string) reportSinkResult

// deliverReportCard is the production sink: 会話への追記は同期（失敗を返せるように）、
// オペレーターの自動ターンは別 goroutine。自動ターンは provider 呼び出しで分単位
// かかり得るので、リコンサイラの単一 goroutine を塞がせない。
func deliverReportCard(name, convID, kind, reason string) reportSinkResult {
	res := recordSessionReport(name, convID, kind, reason)
	if res == reportSinkOK && chatAutoTurnEnabled() {
		go runReportAutoTurn(convID)
	}
	return res
}

// --- リコンサイラ本体 -----------------------------------------------------------

// reportClock is the reconciler's time source (fake clock でデバウンスと再試行を
// 時間駆動テストするための seam)。
type reportClock interface {
	Now() time.Time
	Ticker(d time.Duration) (<-chan time.Time, func())
}

type reportRealClock struct{}

func (reportRealClock) Now() time.Time { return time.Now() }

func (reportRealClock) Ticker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// reportSettleState is the per-session debounce counter: 連続で静穏だった sweep 数と、
// その静穏が始まった時刻。
type reportSettleState struct {
	quiet      int
	quietSince time.Time
}

type reportReconciler struct {
	interval time.Duration
	clock    reportClock
	sink     reportSink
	wake     chan struct{}
	swept    chan struct{} // テスト用: 1 sweep 完了ごとの通知（本番は誰も読まない）

	mu     sync.Mutex
	hints  map[string]string // name → 直近ヒントの reason（turn-failed / turn-aborted）
	states map[string]reportSettleState
}

func newReportReconciler(interval time.Duration) *reportReconciler {
	return &reportReconciler{
		interval: interval,
		clock:    reportRealClock{},
		sink:     deliverReportCard,
		wake:     make(chan struct{}, 1),
		swept:    make(chan struct{}, 1),
		hints:    map[string]string{},
		states:   map[string]reportSettleState{},
	}
}

// reportRec is the process-wide reconciler. 判定は1プロセス1 goroutine に集約する
// （v1 の reportArmMu / reportWaiters / 世代レースの議論がこれで消える）。フック
// サブプロセス側にも同じ変数は存在するが、そこでは run() が回っていないので hint() は
// ただのメモ書きになる（実配送はサーバプロセスの tick が行う）。
var reportRec = newReportReconciler(reportTickDefault)

// startReportReconciler launches the sweep loop (main 起動時に1回)。
func startReportReconciler() { go reportRec.run(context.Background()) }

func (rc *reportReconciler) run(ctx context.Context) {
	tick, stop := rc.clock.Ticker(rc.interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
		case <-rc.wake:
		}
		rc.sweep(rc.clock.Now())
		select {
		case rc.swept <- struct{}{}:
		default:
		}
	}
}

// hint wakes the reconciler for a kick that used to deliver a report itself
// （POST /chat/report・notify seam・record-exit）。ヒントは「今すぐ見に行け」という
// 起床信号でしかなく、判定材料そのものではない — 唯一運ぶのは answer-ready の
// qualifier（中断/失敗の別。マーカーからは読めないので、失われたら素の完了報告に
// 縮退する＝消失ではなく情報の欠落に留める）。
func (rc *reportReconciler) hint(name, kind, reason string) {
	if kind == reportKindAnswerReady && reason != "" {
		rc.mu.Lock()
		rc.hints[name] = reason
		rc.mu.Unlock()
	}
	rc.nudge()
}

// nudge wakes the sweep loop without blocking (wake は容量1 — 連打は畳まれる)。
func (rc *reportReconciler) nudge() {
	select {
	case rc.wake <- struct{}{}:
	default:
	}
}

// forget drops the per-session bookkeeping — 新しい指示（re-arm）と、報告を配送し終えた
// ときの両方で呼ぶ。
func (rc *reportReconciler) forget(name string) {
	rc.mu.Lock()
	delete(rc.states, name)
	delete(rc.hints, name)
	rc.mu.Unlock()
}

func (rc *reportReconciler) hintFor(name string) string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.hints[name]
}

// debounce records one quiet observation and reports whether the settle may fire:
// 静穏を reportSettleTicks 回連続で観測し、かつ最初の観測から tick 間隔ぶん時間が
// 経っていること。ヒント起床で sweep が立て続けに走っても「2回観測した」だけで
// settle させないための時間条件（デバウンスの実体はこの間隔）。
func (rc *reportReconciler) debounce(name string, now time.Time) bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	st := rc.states[name]
	if st.quiet == 0 {
		st.quietSince = now
	}
	st.quiet++
	rc.states[name] = st
	return st.quiet >= reportSettleTicks && !now.Before(st.quietSince.Add(rc.interval))
}

// resetSettle clears the debounce for a session that is demonstrably not quiet.
// ヒントの qualifier も捨てる: 中断の報告が出る前に次のターンが走り出したなら、その
// 中断はもう「今の状態」ではない。
func (rc *reportReconciler) resetSettle(name string) {
	rc.mu.Lock()
	if _, ok := rc.states[name]; ok {
		delete(rc.states, name)
	}
	delete(rc.hints, name)
	rc.mu.Unlock()
}

// sweep re-evaluates every armed session once.
func (rc *reportReconciler) sweep(now time.Time) {
	armed := armedReportSessions()
	for _, name := range armed {
		rc.evaluate(name, now)
	}
	rc.prune(armed)
}

// prune drops bookkeeping for sessions that are no longer armed (配送済み・disarm・
// セッション削除)。
func (rc *reportReconciler) prune(armed []string) {
	live := make(map[string]bool, len(armed))
	for _, n := range armed {
		live[n] = true
	}
	rc.mu.Lock()
	for name := range rc.states {
		if !live[name] {
			delete(rc.states, name)
		}
	}
	for name := range rc.hints {
		if !live[name] {
			delete(rc.hints, name)
		}
	}
	rc.mu.Unlock()
}

// evaluate is the whole decision for one session: 証拠を集める → 述語 → デバウンス →
// 配送 → 配送できたときだけ arm を消費。
func (rc *reportReconciler) evaluate(name string, now time.Time) {
	link, ok := reportLinks.Read(name)
	if !ok || !link.Armed || link.Conv == "" {
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		return // メタが無ければ判定材料が無い（arm はそのまま — 誤消費より温存）
	}
	sig := collectReportSignals(m, link, rc.hintFor(name))
	v := evalReportEvidence(sig)
	if v.Quiet && !v.Terminal && reportPaneBusy(m) {
		sig.PaneBusy = true
		v = evalReportEvidence(sig)
	}
	if !v.Quiet {
		rc.resetSettle(name)
		return
	}
	if !v.Terminal && !rc.debounce(name, now) {
		return // まだ 2 tick 連続に足りない
	}
	switch rc.sink(name, link.Conv, v.Kind, v.Reason) {
	case reportSinkRetry:
		// 台帳（arm）は動かさない。次 tick で同じ判定に戻ってきて再送する。
		return
	case reportSinkDrop:
		log.Printf("session-report: %s の報告先会話が見つからない — arm を畳む", name)
	}
	consumeReportArm(name)
	rc.forget(name)
	log.Printf("session-report: settled %s kind=%s reason=%q (%s)", name, v.Kind, v.Reason, v.Why)
}

// armedReportSessions lists the sessions with a pending one-shot report. arm ファイルの
// ディレクトリを直接見るので、定常コストは readdir 1回＋armed な行の小さな read だけ。
func armedReportSessions() []string {
	ents, err := os.ReadDir(reportLinks.Dir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if !session.ValidName(name) {
			continue
		}
		if l, ok := reportLinks.Read(name); ok && l.Armed && l.Conv != "" {
			out = append(out, name)
		}
	}
	return out
}
