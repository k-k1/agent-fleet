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

// MarkTurnEndErr is MarkTurnEnd carrying the reason a turn failed. failure is the
// one-line summary the driver built (empty for a clean turn); it rides the notifier's
// excerpt so the operator report can say the turn errored and the chat bridge can post
// the reason. Drivers that don't yet distinguish failures keep calling MarkTurnEnd.
func MarkTurnEndErr(sid string, st TurnState, failure string) {
	previous, _ := status.Read(sid)
	status.Persist(sid, "idle")
	if st == TurnUnknown {
		return
	}
	if st == TurnFailed {
		notify(sid, previous.State, StateFailed, failure)
		return
	}
	notify(sid, previous.State, "idle", "")
}
