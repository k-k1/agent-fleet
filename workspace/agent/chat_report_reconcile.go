package main

// セッション報告の消費判定 — レベル駆動リコンサイラ（docs/log/51 Phase 1 / ADR 0035）。
//
// v1（docs/log/30）は「Stop フック等のエッジを1回だけ捕まえ、不可逆な1bit（arm）を
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
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

const (
	// reportTickDefault is the reconciler's sweep cadence. 定常コストは「armed な
	// セッションが1件も無ければ readdir 1回」なので短めでよいが、settle には 2 tick
	// 連続の静穏を要求するため、ヒント喪失時の配送遅延はおよそ 1〜2 tick になる
	// （15〜30s — v1 waiter の 90s TTL 待ちより悪化しない: docs/log/51 §トレードオフ）。
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
	MarkerAfterArm bool   // マーカーが最古の未報告指示以降に書かれたか＝最小の progressed
	MarkerTS       string // そのマーカーの RFC3339（どの指示行までを覆うかの判定に使う）

	PendingQuestion   bool // 質問待ち（interim — そもそも完了ではない）
	PendingPlan       bool // プラン承認待ち
	PendingPermission bool // ツール許可待ち

	SubagentBusy   bool // BG サブエージェント / Workflow の jsonl 鮮度（claude）
	TranscriptBusy bool // メイン transcript の鮮度（claude・思考ギャップ対策）
	PaneBusy       bool // ペインの中断アフォーダンス（tmuxx.IsBusy・TUI のみ）

	Stopped bool   // 意図停止（行は温存 — 再開後の完了で報告する v1 規約）
	Exit    string // 指示以降の異常終了（oom/crashed/killed）— 終端の事実
	ExitAt  string // その異常終了の RFC3339

	// SelfReported は自己申告ファストパス（docs/log/51 §自己申告・Phase 3）: セッション自身が
	// af_report MCP ツールで「この指示は終わった」と申告した。**意味的完了を直接測る
	// 唯一のシグナル**だが、呼び忘れ・早呼びがあるので backbone にはしない — busy 証拠は
	// これより強く、申告があっても進行中なら settle しない。
	SelfReported bool
	SelfReportAt string // その申告の RFC3339
	// SelfReportAged は申告から selfReportSettleDelay 以上経っているか。申告**だけ**で
	// 完了と読むにはこれが要る（下の定数のコメント参照）。
	SelfReportAged bool

	// Abort は転写の末尾が API エラーで切れている＝ターンが中断で終わった証拠
	// （docs/log/47）。claude はこのとき Stop hook を鳴らさないので**マーカーは一切
	// 動かない**。従来これを見ていたのはペースのヒール経路（state != idle かつ
	// ペインが待機表示）だけで、誤ヒールでマーカーが消えた後は二度と評価されず、
	// 中断がどこにも報告されないまま指示が宙に浮いた（実測 sp2qemx 2026-07-30）。
	// レベルで読めば入口が状態に依存しなくなる — 転写の末尾は「今ディスクにある
	// 状態」そのもので、ユーザーが再開すれば末尾が変わって自然に消える。
	Abort       bool
	AbortReason string // turn-aborted（再送で直る）/ turn-failed（原因を直すまで無意味）
	AbortAt     string // その中断が記録された RFC3339

	// TailAborted は「転写の末尾が中断である」という**時刻を問わない**事実。Abort との
	// 違いは since（＝この指示の下限 / 補償では報告時刻）で切らないこと。
	//
	// 完了判定（settle）は Abort を使う — 前の指示の中断で今の指示を閉じないため、時刻の
	// 下限が要る。**補償（reopen）は逆で、下限があると誤訂正になる**: 報告より古い中断は
	// 落とされ、代わりに転写の鮮度が「報告のあとに働き出した」証拠に見えてしまう。中断
	// レコード自身の新しさなのに。v1 は中断と同時に報告していたので時刻が並び偶然守られて
	// いたが、§4-6 で報告が数分遅れるようになって表面化した（自分の回帰テストが捕捉）。
	TailAborted bool

	// AbortHeld は「中断を見つけたが、Agent 自身の自動再開が引き受けている」状態
	// （docs/log/47 §4-6）。報告を**遅らせる**だけで握り潰さない: 再開が成功すれば、その
	// ターンの完了が指示を閉じる（報告は2回でなく1回になる）。再送しても中断が続いて
	// 打ち切られたら、そこで抑止が外れて中断報告が出る。
	//
	// 抑止は Abort だけでなく**マーカー由来の idle 証拠にも効かなければならない**:
	// 中断でも Stop が鳴る形（利用上限の 429 — docs/log/47 §4-5）があり、その形では
	// マーカーが先に idle+turnEnd になるので、Abort を落とすだけだと「素の完了」として
	// 報告してしまう。よって evalReportEvidence の入口で見る。
	AbortHeld bool

	HintReason string // 直近のヒントが運んだ qualifier（turn-failed / turn-aborted）
}

// reportVerdict is the predicate's answer for one session at one sweep.
type reportVerdict struct {
	Quiet    bool   // この sweep で「idle 証拠 ≥1 ∧ busy 証拠 = 0」だったか
	Terminal bool   // 異常終了 — デバウンス不要（プロセスは既に死んでいる）
	Fast     bool   // 自己申告あり — デバウンスを 2 tick から 1 tick に短縮
	Kind     string // 報告 kind（answer-ready / exit）
	Reason   string // 報告 reason（turn-failed / turn-aborted / oom …）
	At       string // 証拠の時刻（RFC3339）— どの指示行までを覆うかの判定に使う
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
//
// Phase 3 で2つ目の証拠として自己申告（af_report）が加わる。マーカーと同格に**列挙**
// するだけで、busy 証拠より強くはしない — 早呼びは「busy 証拠がゼロになるまで保留」に
// 落ちる。
func (s reportSignals) idleEvidence() []string {
	var ev []string
	if s.markerIdle() {
		ev = append(ev, "marker-idle")
	}
	if s.SelfReported && s.SelfReportAged {
		ev = append(ev, "self-report")
	}
	if s.Abort {
		ev = append(ev, "abort")
	}
	return ev
}

// markerIdle reports whether the status marker itself is the 3点そろった明示 idle.
func (s reportSignals) markerIdle() bool {
	return s.MarkerState == "idle" && s.MarkerTurnEnd && s.MarkerAfterArm
}

// evidenceAt is the time the settle evidence was observed — 「この時刻までに投入された
// 指示行だけが、この静穏で完了になり得る」の基準（instrRowsCoveredBy）。マーカーが
// 立っていればその書込み時刻、自己申告だけならその申告時刻。両方あればマーカーを採る
// （ターンの終端そのものを指す、より強い証拠）。
func (s reportSignals) evidenceAt() string {
	if s.markerIdle() {
		return s.MarkerTS
	}
	if s.Abort && s.AbortAt != "" {
		return s.AbortAt
	}
	if s.SelfReported && s.SelfReportAged {
		return s.SelfReportAt
	}
	return ""
}

// evalReportEvidence is the settle predicate（docs/log/51 §settled/progressed 述語）。
// 純関数: 引数の証拠だけで決まる。2 tick 連続のデバウンスは呼び出し側（リコンサイラ）
// の責務で、ここは1 sweep ぶんの「静穏か」を返す。
func evalReportEvidence(s reportSignals) reportVerdict {
	// 異常終了は終端の事実。プロセスは死んでいるので busy 証拠の消滅を待つ意味がなく
	// （死ぬ直前まで transcript は新鮮なままになる）、デバウンスもしない。ExitInfo を
	// レベルで読むので、kick を持たない managed daemon の異常死も同じ経路で拾える。
	if s.Exit != "" {
		return reportVerdict{
			Quiet: true, Terminal: true, Kind: "exit", Reason: s.Exit,
			At: s.ExitAt, Why: "exit:" + s.Exit,
		}
	}
	if s.Stopped {
		// 停止＝指示の取り消しではない（取り消しは stop_session の disarm）。arm は
		// 温存し、再開後の完了で報告する（docs/log/30 の規約）。
		return reportVerdict{Why: "stopped"}
	}
	if s.AbortHeld {
		// 中断は自動再開が引き受けている（docs/log/47 §4-6）。まだ「終わった」と言わない —
		// 異常終了（上）だけは先に見る: プロセスが死んでいるなら再開する相手が居ない。
		return reportVerdict{Why: "abort-held"}
	}
	if busy := s.busyEvidence(); len(busy) > 0 {
		return reportVerdict{Why: "busy:" + strings.Join(busy, ",")}
	}
	idle := s.idleEvidence()
	if len(idle) == 0 {
		return reportVerdict{Why: "unknown"} // 不明は不明のまま — idle と既定しない
	}
	// 中断は「なぜ終わったか」を転写から直接読めるので、ヒント（フック由来・
	// 中断では鳴らないので普通は空）より優先する。
	reason := s.HintReason
	if s.Abort {
		reason = s.AbortReason
	}
	return reportVerdict{
		Quiet: true, Fast: s.SelfReported, Kind: reportKindAnswerReady, Reason: reason,
		At: s.evidenceAt(), Why: "idle:" + strings.Join(idle, ","),
	}
}

// evalReportResumed is the COMPENSATION predicate（docs/log/51 §補償）: 完了報告のあと、
// セッションが**その報告より後に**働き始めた証拠。settle 述語の busy 証拠と同じ列を見る
// が、判定の向きが逆なので条件が1つ増える —「報告の時点で既にそう見えていた」ものを
// 除く必要がある。マーカー系は書込み時刻で切れる（MarkerAfterArm は since=報告時刻で
// 集めた場合「報告より後のマーカー」を意味する）。鮮度ベースの証拠（サブエージェント・
// 転写・ペイン）は、報告が出た時点では必ず false だったもの（busy 証拠がゼロでなければ
// 報告は出ない）なので、いま true なら新しい書込みがあったということ。
func evalReportResumed(s reportSignals) []string {
	// 報告のあとに「ターンが**終わった**」証拠が来ているなら、それは作業の再開ではなく、
	// いま報告した完了そのものの遅着（あるいはそのターンが中断で終わったこと）である。
	// ここを見分けないと、鮮度の証拠（報告の数秒後に書かれる最後の assistant 行）で
	// 「先の完了報告は早計でした — まだ作業中です」という**嘘の訂正**を出し、次の tick で
	// 同じ完了をもう一度報告することになる（実測 2026-07-30 sannme2: 09:59:34 報告 →
	// 09:59:50 に本物の回答が書かれる → 10:00:08 訂正 → 10:00:34 同内容を再報告）。
	// 本当に再開していれば最新のマーカーは working / question 側になり、下の列で拾える。
	if s.markerIdle() || s.TailAborted {
		return nil
	}
	var ev []string
	switch s.MarkerState {
	case "working", "question", "plan", "permission":
		if s.MarkerAfterArm {
			ev = append(ev, "marker-"+s.MarkerState)
		}
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

// collectReportSignals reads the current level state for one session with open
// instruction rows. since は**最古の未報告指示のカーソル**（docs/log/51 §progressed の
// 下限）— それより前の証拠は「前の指示の話」なので今回の完了にはならない。
// selfAt は自己申告の時刻（無ければ空）。since より前の申告は前の指示の話なので捨てる —
// マーカーに課している progressed の下限を、自己申告にも同じ形で課す。
func collectReportSignals(m session.Meta, since, hintReason, selfAt string) reportSignals {
	sid := session.UUID(m.Dir, m.Name)
	s := reportSignals{Stopped: m.StoppedAt != "", HintReason: hintReason}
	if selfAt != "" && !reportTimeBefore(selfAt, since) {
		s.SelfReported, s.SelfReportAt = true, selfAt
		if at, err := time.Parse(time.RFC3339, selfAt); err == nil {
			s.SelfReportAged = time.Since(at) >= selfReportSettleDelay
		}
	}
	var markerAt time.Time
	if st, ok := status.Read(sid); ok {
		s.MarkerState, s.MarkerTurnEnd, s.MarkerTS = st.State, st.TurnEnd, st.TS
		s.MarkerAfterArm = !reportTimeBefore(st.TS, since)
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
		collectAbortSignal(&s, m.Name, sid, since)
	}
	if e, ok := status.ReadExit(m.Name); ok {
		switch e.Reason {
		case "oom", "crashed", "killed":
			// 指示より前の死は前の指示の話（起動時に baseline が Reason を消すが、
			// 記録が残っていても取り違えないよう時刻でも切る）。ここだけは時刻が
			// **読めること**を要求する: 異常終了はデバウンス無しで arm を消費するので、
			// 判定不能を「今回の死」に倒すと生きているセッションへ誤報告しかねない
			// （書き手は全経路 At を必ず入れる）。
			if _, err := time.Parse(time.RFC3339, e.At); err == nil && !reportTimeBefore(e.At, since) {
				s.Exit, s.ExitAt = e.Reason, e.At
			}
		}
	}
	return s
}

// selfReportSettleDelay is how long a session must stay quiet AFTER calling af_report
// before that self-report alone may settle the instruction (docs/log/51 §自己申告).
//
// 申告は「意味的完了を直接測る唯一のシグナル」だが、**早呼び**がある: セッションが
// 「終わった」と申告してから、まだ最終回答を書き続けることがある。実測 2026-07-30
// sannme2 では申告の 2 分 22 秒後に本物の回答が届いた — その間、転写は 142 秒沈黙して
// おり（鮮度 TTL 90s を超える）、ペインのスピナーもエージェント表示のせいで読めず、
// busy 証拠がすべて消えて早すぎる完了報告が出た。
//
// 正常系はこの遅延を踏まない: 申告のあとターンが終われば Stop フックが終端マーカーを
// 書き、そちら（marker-idle）で即座に settle する。この窓が効くのは「マーカーが最後まで
// 来ない」ケース＝申告が唯一の手掛かりのときだけで、そこでは数分の遅延より誤報告を
// 避ける方が価値が高い。値は観測された思考ギャップ（142s）に余裕を足したもの。
const selfReportSettleDelay = 3 * time.Minute

// collectAbortSignal reads the転写末尾 for a turn that died on an API error (docs/log/47).
// マーカーを一切見ないのが肝: claude は中断で Stop hook を鳴らさないので、マーカーは
// 「working のまま」「誤ヒールで消えた」「前のターンの idle が残っている」のどれにも
// なり得る。中断そのものは転写の末尾という**レベル**に書かれているので、そこだけを見る。
//
// since より前の中断は前の指示の話なので捨てる（マーカー・自己申告と同じ下限）。時刻を
// 持たないレコードは切らずに採る — 中断は「今ディスクにある末尾」なので、時刻が読めなくても
// 現在の状態を指しているから。
func collectAbortSignal(s *reportSignals, name, sid, since string) {
	a, ok := claude.AbortInfo(sid)
	if !ok {
		return
	}
	s.TailAborted = true // 時刻で切らない事実（補償が読む — 上のコメント参照）
	at := ""
	if !a.At.IsZero() {
		at = a.At.Format(time.RFC3339)
	}
	if at != "" && reportTimeBefore(at, since) {
		return
	}
	// 再送で直る中断は、まず Agent 自身が再開させる（docs/log/47 §4-6）。その間は報告を
	// 出さない — 出すとアシスタントのターンが1つ走り、しかもその内容（「再開させろ」）は
	// もう実行済みになる。打ち切られたら holds が false になり、通常経路へ落ちる。
	if abortResumeHolds(name, a, time.Now()) {
		s.AbortHeld = true
		return
	}
	s.Abort, s.AbortAt = true, at
	s.AbortReason = reportReasonTurnFailed
	if a.Retryable {
		s.AbortReason = reportReasonTurnAborted
	}
	// 中断が末尾にあるなら、転写の「新しさ」はその中断レコード自身のもの — 進行中の
	// 証拠ではなく、終わり方そのものである。ここで下ろさないと、中断のたびに鮮度の
	// 窓（90s）が明けるまで報告が足止めされる。ペイン／サブエージェントの busy 証拠は
	// 別の事実（再開した・BG が動いている）なので残す。
	s.TranscriptBusy = false
}

// reportMarkerGrace absorbs the RFC3339 second-truncation of the status marker when
// comparing it against a transcript record's timestamp: 「Stop フックのマーカー」と
// 「そのターン最後の転写書込み」は同じ1秒に収まることがあるので、この余裕より後の
// 追記だけを「マーカーの後にも伸びた」と読む。
const reportMarkerGrace = 2 * time.Second

// reportTranscriptBusy reports whether the main transcript says the turn is still
// running. v1 の waiter は素の鮮度（90s）だけを見ていた — Stop の裏付けを持たない
// 立場ではそれしか手が無かったが、それを完了判定の常設ゲートにすると**正常な完了
// 報告が毎回 90s 遅れる**。Phase 1 には「Stop が書いた終端マーカー」という positive な
// 証拠があるので、判定を相対比較にする: マーカーより後にも転写が伸びていれば
// ターンは続いている（＝そのマーカーは終端ではない・sqmconc の思考ギャップ）。
// 鮮度も併せて要求するのは安全弁で、転写が静止したら（＝ v1 と同じ 90s が上限）
// 比較の食い違いで報告が永久に止まることが無いようにするため。
//
// TranscriptTouched は「最後の user/assistant 行の時刻」を返す（記帳行では動かない）。
// 1回だけ読んで鮮度と相対比較の両方に使う — 以前は TranscriptBusy と2回読んでいた。
func reportTranscriptBusy(sid string, marker time.Time) bool {
	at, ok := claude.TranscriptTouched(sid)
	if !ok || !claude.TranscriptFresh(at) {
		return false // 静止している転写は「実行中」の証拠にならない（上限は v1 と同じ）
	}
	if marker.IsZero() {
		return true // 終端マーカーが無い＝鮮度だけが手がかり（v1 waiter と同じ立場）
	}
	return at.After(marker.Add(reportMarkerGrace))
}

// reportPaneBusy checks the pane's interrupt affordance (逆ヒールと同じ根拠)。tmux を
// 叩くので settle 候補のときだけ実行する（docs/log/51: tmux 負荷を抑える）。
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
// without a conversation store). rows は畳んだ指示行 — 冪等キー（行ID）と「指示N件ぶん」
// の本文はシンクが組み立てる。
type reportSink func(name, convID, kind, reason string, rows []instrRow) reportSinkResult

// deliverReportCard is the production sink: 会話への追記は同期（失敗を返せるように）、
// オペレーターの自動ターンはデバウンサへ（chat_report_autoturn.go — 近接する報告を
// 1ターンに束ねる）。発火はタイマー goroutine 上なので、provider 呼び出しが分単位
// かかってもリコンサイラの単一 goroutine を塞がない。
func deliverReportCard(name, convID, kind, reason string, rows []instrRow) reportSinkResult {
	res := recordSessionReport(name, convID, kind, reason, rows)
	if res == reportSinkOK && uiprefs.ChatAutoTurn() && !quietReport(kind, reason) {
		reportAutoTurns.schedule(convID)
	}
	return res
}

// quietReport reports whether 静かな完了報告（設定 > アシスタント・既定 OFF）が
// この報告の自動ターンを抑止するか。対象は**正常な**完了（answer-ready・reason
// なし）と、その訂正（reopened・reason なし — 静かなモードでは取り消すべき
// 「完了しました」発言がそもそも無い）だけ。報告カードと通知センターへの配信は
// 従来どおり即時で、報告は未配信のまま残って次のターン（利用者の発話・別報告の
// 自動ターン）に相乗りする（injectPendingReports）— 消えるのは LLM の追撃ターン
// だけ。異常系（中断・失敗・exit）と訂正打ち切り（reopen-capped）はオペレーターの
// 判断・行動（自動再開・原因説明）が要るので従来どおり回す。
func quietReport(kind, reason string) bool {
	if !uiprefs.ChatQuietCompletion() {
		return false
	}
	return reason == "" && (kind == reportKindAnswerReady || kind == reportKindReopened)
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
	selfs  map[string]string // name → 自己申告（af_report）の RFC3339
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
		selfs:    map[string]string{},
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

// selfReport records the session's own「終わった」claim (docs/log/51 §自己申告ファストパス)
// and wakes the sweep. **これは backbone ではない** — 記録するのは申告の時刻だけで、
// 報告そのものは通常どおりリコンサイラが述語で決める。申告が来なければ settle が拾い、
// 早すぎれば busy 証拠に止められる。
//
// 保持がプロセス内メモリなのは意図的: agent が落ちれば申告は消えるが、そのとき縮退する
// のは「2 tick が 1 tick になる」高速化だけで、報告の有無は台帳（ディスク）が持っている。
// ファストパスの状態を永続化して台帳と二重に真実を持つ方が高くつく。
func (rc *reportReconciler) selfReport(name string, at time.Time) {
	if !session.ValidName(name) {
		return
	}
	rc.mu.Lock()
	rc.selfs[name] = at.Format(time.RFC3339)
	rc.mu.Unlock()
	rc.nudge()
}

func (rc *reportReconciler) selfReportFor(name string) string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.selfs[name]
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
	delete(rc.selfs, name)
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
//
// **自己申告は捨てない**。それは「今の状態」ではなく「この指示は自分としては終わった」
// という指示単位の主張なので、早呼び（申告のあとにまだ動いていた）で失効させると、
// ファストパスが「モデルが最後の1トークンを吐いた後に呼んだときだけ効く」ものになる。
// 申告が捨てられるのは新しい指示が来たときと配送が済んだとき（どちらも forget）。
func (rc *reportReconciler) resetSettle(name string) {
	rc.mu.Lock()
	if _, ok := rc.states[name]; ok {
		delete(rc.states, name)
	}
	delete(rc.hints, name)
	rc.mu.Unlock()
}

// sweep re-evaluates every session with open instruction rows once, then compensates
// the sessions whose recent completion report may have been premature (docs/log/51 §補償)。
func (rc *reportReconciler) sweep(now time.Time) {
	pending, grace := instrSweepSessions(now)
	for _, name := range pending {
		rc.evaluate(name, now)
	}
	for _, name := range grace {
		rc.compensate(name, now)
	}
	rc.prune(pending)
}

// prune drops bookkeeping for sessions with no open rows left (報告済み・cancelled・
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
	for name := range rc.selfs {
		if !live[name] {
			delete(rc.selfs, name)
		}
	}
	rc.mu.Unlock()
}

// evaluate is the whole decision for one session: 未報告の指示行を読む → 証拠を集める →
// 述語 → デバウンス → 証拠が覆う行だけを配送 → 配送できた行だけを reported にする。
//
// docs/log/51 Phase 2 の肝は最後の2段。判定は「セッションが静穏か」という**セッション単位**
// の話だが、報告義務は**指示単位**なので、静穏の証拠（idle マーカー / ExitInfo）より
// **後に**投入された指示は同じ静穏では完了になり得ない。その行は pending のまま残り、
// 次のターンの終端で改めて報告される — v1 で arm が上書きされて消えていた穴A が、
// ここでは「消えない行」として定義から外れる。
func (rc *reportReconciler) evaluate(name string, now time.Time) {
	open := openInstrRows(name)
	if len(open) == 0 {
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		return // メタが無ければ判定材料が無い（行はそのまま — 誤報告より温存）
	}
	sig := collectReportSignals(m, open[0].Cursor.At, rc.hintFor(name), rc.selfReportFor(name))
	v := evalReportEvidence(sig)
	if v.Quiet && !v.Terminal && reportPaneBusy(m) {
		sig.PaneBusy = true
		v = evalReportEvidence(sig)
	}
	if !v.Quiet {
		rc.resetSettle(name)
		return
	}
	covered := instrRowsCoveredBy(open, v.At)
	if len(covered) == 0 {
		// 証拠は静穏だが、どの指示もその証拠より後に投入されている（＝まだ走り出して
		// いない）。デバウンスも積まない — 次の本物の終端を待つ。
		rc.resetSettle(name)
		return
	}
	// Fast: 自己申告があるときはデバウンスを1 tick に短縮する（docs/log/51 §ファストパス）。
	// 時間的な裏取りが要るのは「機械的 idle が意味的完了とズレる」からで、セッション自身が
	// 完了だと言っている以上、その2つはズレていない。busy 証拠のゲートは通ったままなので、
	// 早呼びは短縮の対象にならない（そもそも Quiet にならない）。
	if !v.Terminal && !v.Fast && !rc.debounce(name, now) {
		return // まだ 2 tick 連続に足りない
	}
	retry := false
	delivered := false
	for _, conv := range instrConvs(covered) {
		rows := instrRowsForConv(covered, conv)
		switch rc.sink(name, conv, v.Kind, v.Reason, rows) {
		case reportSinkRetry:
			// 台帳は動かさない。次 tick で同じ判定に戻ってきて再送する（行IDで冪等）。
			retry = true
			continue
		case reportSinkDrop:
			log.Printf("session-report: %s の報告先会話 %s が見つからない — 行を畳む", name, conv)
		default:
			delivered = true
		}
		markInstrReported(name, instrIDs(rows), now)
		log.Printf("session-report: settled %s kind=%s reason=%q rows=%v (%s)",
			name, v.Kind, v.Reason, instrIDs(rows), v.Why)
	}
	// 自動再開のカウンタ（docs/log/47）は**セッション単位のイベント**を数える。1つの静穏を
	// 複数のオペレーター会話へ配ったときに会話数ぶん加算すると、2会話から指示されている
	// セッションは中断1回で上限（2回）に届いてしまう。数えるのは「中断報告を配った」と
	// いう事実1つなので、会話ループの外で1回だけ動かす。
	if delivered && v.Kind == reportKindAnswerReady {
		switch v.Reason {
		case reportReasonTurnAborted:
			bumpAutoResume(name)
		case "":
			resetAutoResume(name)
		}
	}
	if !retry {
		rc.forget(name)
	}
}

// reportReopenGrace is how long a reported row stays under compensation watch
// (docs/log/51 §補償)。この窓を過ぎてからの busy 復帰は「新しい仕事」であって誤報告の
// 続きではない、という線引き。
const reportReopenGrace = 10 * time.Minute

// compensate is the self-repair for a WRONG「完了」(docs/log/51 §補償 / ADR 0035 決定4)。
//
// v1 の非対称はここだった: 誤って arm を消費すると二度と報告されない（誤消費＝回復
// 不能）。台帳では報告は行の状態でしかないので、grace の間だけ「その報告のあとに
// セッションが働き出していないか」を見張り、働き出していたら**訂正を配ってから**行を
// 開き直す。以後は通常の settle 経路が本完了を報告する。
//
// 訂正 → reopen の順は落とせない: 逆順だと、訂正の配送に失敗したときに「黙って
// 開き直しただけ」になり、利用者から見れば v1 の消失と区別が付かない。
func (rc *reportReconciler) compensate(name string, now time.Time) {
	cands := instrReopenCandidates(readInstrRows(name), now, reportReopenGrace)
	if len(cands) == 0 {
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		return
	}
	// since = 監視対象のうち最も新しい報告時刻。これより後の証拠だけを「復帰」と読む。
	since := ""
	for _, r := range cands {
		if r.ReportedAt > since {
			since = r.ReportedAt
		}
	}
	sig := collectReportSignals(m, since, "", "")
	if sig.Stopped || sig.Exit != "" {
		return // 停止・異常終了は「続行中」ではない（異常終了は別の指示行が拾う）
	}
	resumed := evalReportResumed(sig)
	if len(resumed) == 0 {
		if !reportPaneBusy(m) {
			return
		}
		sig.PaneBusy = true
		resumed = evalReportResumed(sig)
	}
	why := strings.Join(resumed, ",")
	reopened := false
	for _, conv := range instrConvs(cands) {
		var reopen, capped []instrRow
		for _, r := range instrRowsForConv(cands, conv) {
			if r.ReopenCount >= instrReopenMax {
				capped = append(capped, r)
			} else {
				reopen = append(reopen, r)
			}
		}
		if len(reopen) > 0 && rc.sink(name, conv, reportKindReopened, "", reopen) == reportSinkOK {
			for _, r := range reopen {
				if reopenInstrRow(name, r.ID) {
					reopened = true
				}
			}
			log.Printf("session-report: reopened %s rows=%v (%s)", name, instrIDs(reopen), why)
		}
		// 上限に達した行は開き直さない。黙って打ち切ると「報告が来ない」だけになるので、
		// 判定が振動している事実そのものを1回だけ利用者向けに報告する（docs/log/47 の
		// 自動再開上限と同じイディオム）。配送は行IDで冪等なので毎 tick は繰り返さない。
		if len(capped) > 0 {
			rc.sink(name, conv, reportKindReopened, reportReasonReopenCapped, capped)
		}
	}
	if reopened {
		// 開き直した行は「これから完了する指示」なので、判定はまっさらから始める。
		rc.forget(name)
	}
}
