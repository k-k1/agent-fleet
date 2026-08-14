package agents

// managed driver の状態遷移通知 seam（docs/30）。
//
// TUI ルートでは hook が `workspace-agent session-status <state>` を叩き、package main
// の runSessionStatusHook が「status ストアを書く → recordSessionNotification で
// 応答あり通知（notice）と オペレーターへの完了報告（docs/30）を出す」までを一息に
// 行う。managed driver（§3: codex app-server / opencode serve）は hook を持たず
// driver 自身が status を書いていたため、報告の arm を消費する者がおらず「完了しても
// 報告が一切飛ばない」構造的な穴があった（docs/30 の既知制限として明記されていた）。
//
// driver は internal/agents 配下、recordSessionNotification は package main にあり、
// Go では main を import できない。そこで main が起動時にここへ通知関数を登録し、
// driver は MarkTurnStart / MarkTurnEnd 経由で status 書き込みと通知をまとめて行う。
// 「どの遷移が完了か・誰に何を通知するか」の判定は hook 経路と同じ 1 実装
// （recordSessionNotification）を共有する — driver 側に第 2 の判定を持たせない。

import (
	"sync/atomic"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// StateNotifier is the notification half of a session state transition, keyed by sid:
// (previous status, new status, turn の出力抜粋). package main registers
// recordSessionNotification — the same function the hook route calls.
type StateNotifier func(sid, previous, state, excerpt string)

// stateNotifier is registered once at startup (main) but read from driver goroutines
// (codex readLoop / opencode pump), so it goes through atomic.
var stateNotifier atomic.Pointer[StateNotifier]

// SetStateNotifier wires the notifier. main calls it before any driver can run
// (reconcile / app-server start); tests use it to observe transitions.
func SetStateNotifier(fn StateNotifier) {
	if fn == nil {
		stateNotifier.Store(nil)
		return
	}
	stateNotifier.Store(&fn)
}

// notify fires the registered notifier off the caller's goroutine. ASYNC on purpose:
// codex の dispatchNotification は全 handle 共有の readLoop 1 goroutine で回っており
// （appclient.go）、通知は notice のファイル書き込みと POST /chat/report（自プロセス
// への HTTP）を伴う。同期で呼ぶと 1 セッションの報告が全 codex managed セッションの
// 通知配送を止める。previous/state は呼び出し側が同期で確定済みなので、goroutine の
// 実行順が前後しても (previous, state) の組は壊れない。
func notify(sid, previous, state, excerpt string) {
	fn := stateNotifier.Load()
	if fn == nil {
		return
	}
	go (*fn)(sid, previous, state, excerpt)
}

// MarkTurnStart records that a managed turn began: status=working（hook 経路の
// UserPromptSubmit 相当）。開始は通知の対象外 — recordSessionNotification 側でも
// working は無反応だが、無駄な goroutine を作らないためここで止める。
func MarkTurnStart(sid string) {
	status.Persist(sid, "working")
}

// MarkTurnEnd records that a managed turn ended: status=idle（hook 経路の Stop 相当）
// を書き、完了通知を出す。idle を必ず書くのは従来どおり — turn の終端で戻さないと
// WireLive のフォールバックと anySessionWorking が 進行中 に張り付く。
//
// st == TurnUnknown のときは idle を書くが通知しない: runtime を見失っただけで turn は
// 相手側で走り続けているかもしれず、「応答が完了しました」と報告するのは嘘になる
// （回復は §6 の reconcile、プロセスの異常終了は record-exit / serve.go の責務）。
//
// excerpt は managed では空（claude の MessageDisplay hook に当たるストリーミング
// 捕捉が無い）。オペレーター報告は TUI/managed とも本文抜粋なしの事実のみ
// （docs/30）なので報告経路では使われず、TUI では全文ブリッジ（docs/37）の body に
// だけ乗る。オペレーターは get_session_output で詳細を読む。
func MarkTurnEnd(sid string, st TurnState) { MarkTurnEndErr(sid, st, "") }

// StateFailed is the transition label MarkTurnEndErr hands the notifier for a turn that
// ended in an error. It is NOT a status value — the status store still gets "idle"
// (the session really is back at 入力待ち, and WireLive / anySessionWorking depend on
// that). It exists because "終わった" and "エラーで終わった" were indistinguishable to
// every consumer: a provider-side failure was reported to the operator as 応答が完了,
// which is how an exhausted balance looked exactly like a finished turn.
const StateFailed = "failed"

// StateAborted is the transition label for a turn that was CUT OFF before it produced
// an answer but can simply be re-run (接続断・一時的なレート制限). Like StateFailed it
// is not a status value — the status store still gets "idle". It is separate from
// StateFailed because the operator's next move differs: a failed turn must not be
// re-sent until its cause is fixed, while an aborted one only needs a nudge to
// continue — which is what makes 中断時の自動再開 safe (docs/47).
const StateAborted = "aborted"

// StateBlocked is a LIVE WIRE state — unlike StateFailed / StateAborted above it is not a
// notifier label, and unlike "working" / "idle" it is never persisted to the status store.
// It means: the turn is over, but the CLI has parked the pane on a menu that only a human
// keypress clears (今のところ claude の 利用上限メニュー — tmuxx.AtRateLimitModal)。
//
// 「終わった (idle)」でも「走っている (working)」でもない第3の状態を作るのは、その2つの
// どちらに寄せても実害が出るから:
//   - working に寄せる = 元のバグ。自己修復が効かず永久に 進行中、通知も完了報告も出ず、
//     reaper が busy と見なしてコンテナが起きっぱなしになる（実測 約16時間）。
//   - idle に寄せる = より悪い。入力待ちに見えるのでミラー／オペレーター／定時実行が
//     プロンプトを送り、その文字がそのまま**メニューの選択操作に化ける**
//     （AgentsViewActive と同じ誤配達クラス）。
//
// 毎 poll ペインから導出するので status ストアには書かない: メニューは人が消すもので、
// 消えた瞬間に次の poll が普通の idle を返せばよく、消えたことを知る別経路が要らない。
const StateBlocked = "blocked"

// StateAuth is the same kind of live-only wire state as StateBlocked, for the one cause
// that no keypress in the pane can clear: **the workspace's claude login has expired**
// (docs/47 §4-8 — 資格情報の refresh も access も過ぎている)。
//
// StateBlocked と分けるのは、利用者に促す次の一手が正反対だから: 上限は「待て」、
// 認証切れは「今すぐ再認証しろ」。同じ 停止中 バッジに畳むと、待っていれば直ると
// 読めてしまう — 認証切れは待っても永久に直らない。
//
// idle に寄せてはいけない理由は StateBlocked と同じで、しかもこちらの方が実害が出た:
// 入力待ちに見えるのでミラー／オペレーター／定時実行がプロンプトを送り、TUI は
// 受け取るが**ターンは 1 つも始まらない**（送信は成功したように見え、ミラーには
// 反映待ちのプロンプトだけが残る — 利用者報告 2026-08-14）。
//
// ワークスペース単位の事実（資格情報はコンテナに 1 つ）なので claude のセッション
// 全部が同時にこれを名乗る。status ストアには書かない — 再認証した瞬間に次の poll が
// 普通の状態を返せばよく、消えたことを知る別経路が要らない（StateBlocked と同型）。
const StateAuth = "auth"

// MarkTurnEndErr is MarkTurnEnd carrying the reason a turn failed. failure is the
// one-line summary the driver built (empty for a clean turn); it rides the notifier's
// excerpt so the operator report can say the turn errored and the chat bridge can post
// the reason. Drivers that don't yet distinguish failures keep calling MarkTurnEnd.
func MarkTurnEndErr(sid string, st TurnState, failure string) {
	previous, _ := status.Read(sid)
	if st == TurnUnknown {
		// runtime を見失っただけ。idle は書く（進行中に張り付かせない）が、これは
		// 「ターンの終端」ではないので TurnEnd は立てない — レベルで判定する報告の
		// リコンサイラ（docs/51）がこの idle を完了の証拠に数えると、まだ相手側で
		// 走っているかもしれないターンを「完了しました」と報告してしまう。
		status.Persist(sid, "idle")
		return
	}
	status.PersistTurnEnd(sid, "idle")
	if st == TurnFailed {
		notify(sid, previous.State, StateFailed, failure)
		return
	}
	if st == TurnAborted {
		notify(sid, previous.State, StateAborted, failure)
		return
	}
	notify(sid, previous.State, "idle", "")
}
