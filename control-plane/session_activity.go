package main

import "time"

// セッション 1 行を「コンテナを起こし続ける理由になるか」「畳んでよいか」へ落とす分類
// （docs/log/75 §75.5）。reaper の 2 段はここだけを見る。
//
// なぜ関数 1 つに集約するのか: この判定はもともと reaper.go の中にインラインの真偽式が
// 2 つ（tier2 の busy 判定と、フェンス取得後の再判定）あるだけで、テストが 1 本も無かった。
// 実際 2026-07-31 の blocked、2026-08-19/20 の limited / spend_limit と 2 回ドリフトし、
// そのたびに 2 箇所を手で合わせている。状態は今後も増える（agents.State* を見よ）ので、
// 「増えた状態をどちらにも入れ忘れる」を構造的に潰す。
type activity int

const (
	// activityUnknown: 何が起きているか分からない。shell / ssm（走行中のジョブが
	// 見えない）と、この CP が知らない新しい状態。**どちらにも倒さない** — 起こし
	// 続ける理由にはしない（新状態が出るたび Workspace が永久に温まるのを防ぐ）が、
	// 畳みもしない（理解していないものを殺さない）。
	activityUnknown activity = iota
	// activityIdleWait: ターンは終わっていて、次に動くのは人か時計。畳んでよく、
	// 畳んでも失われるものが無い。
	activityIdleWait
	// activityHumanWait: 人の判断を待って止まっている。畳んでよいが、**畳む前に
	// 保留中の対話を持ち越す**必要がある（docs/log/75 §75.6）。コンテナを起こし続ける
	// 理由にはならない — 人待ちは何日でも続きうる。
	activityHumanWait
	// activityMachineBusy: 機械が動いている。触ってはならない。
	activityMachineBusy
)

// 状態名。Agent 側（internal/agents/notify.go・internal/status）が正で、ここは
// ワイヤ越しに来る文字列の写し。
const (
	stateWorking = "working"
	// stateCompacting は codex が文脈圧縮を走らせている（agents/codex の WireLive）。
	// ワイヤに出る 10 個目の状態で、§75.2.1 の 9 状態の表から漏れていた — 分類が
	// 無いと unknown に落ち、**圧縮の最中に Workspace ごと止まる**（機械が動いている
	// のに起こし続ける理由に数えられない）。決定 9 のとおり、分からないものは
	// どちらにも倒さないが、これは分かっている: 明確に machineBusy である。
	stateCompacting = "compacting"
	stateIdle       = "idle"
	stateQuestion   = "question"
	statePlan       = "plan"
	statePermission = "permission"
	stateBlocked    = "blocked"
	stateAuth       = "auth"
	stateLimited    = "limited"
	stateSpendLimit = "spend_limit"
)

// busyState は state 単独で「機械が動いている」を意味するか。
//
// **一覧はここだけを見ること**（idle_forecast.go の holdersOf）。この集合を 2 箇所に
// 書くと、増えた状態を片方に入れ忘れる: 実際 stateCompacting は sessionActivity に
// だけ足され、holdersOf は working しか見ないままだった＝ reaper は正しく止めないのに
// 画面は「もうすぐ止まります」と言う、という docs/log/75 決定 11（画面は reaper の決定を
// そのまま公開する）違反が再発している。
func busyState(state string) bool {
	switch state {
	case stateWorking, stateCompacting:
		return true
	}
	return false
}

// sessionActivity classifies one live session row.
//
// 生きていない行は activityUnknown（畳む対象でも、起こし続ける理由でもない）。
func sessionActivity(s sessionWire) activity {
	if !s.Alive {
		return activityUnknown
	}
	// 利用者の「停止しない」ピン（docs/log/75）。分類の一番外に置くのは、これが**唯一の
	// 逃げ道**である shell / ssm では state が空＝ unknown で、その先の分岐に一切
	// 引っかからないから。生きている行にしか効かない（上の !Alive で落ちる）ので、
	// 死んだセッションのピンがコンテナを抱え込むことはない。
	if keepAwake(s.KeepAwakeUntil, time.Now()) {
		return activityMachineBusy
	}
	// BackgroundBusy は state と直交する: state は idle でも run_in_background の
	// ジョブ・in-process のサブエージェント / Workflow・S 状態の背景シェルが走って
	// いることがある（Agent の WireLive が立てる）。reaper はこれを一度も見ておらず、
	// 走っている背景作業ごと halt / stop していた。**machineBusy 側で最初に見る。**
	if s.BackgroundBusy {
		return activityMachineBusy
	}
	if busyState(s.State) {
		return activityMachineBusy
	}
	switch s.State {
	case stateIdle, stateLimited:
		// limited は「時計待ち」。リセット時刻に CP の定時実行が起こす（docs/log/47 §4-9）
		// ので、畳んでも失われるものは無い＝ idle と同じ扱い。
		return activityIdleWait
	case stateQuestion, statePlan, statePermission, stateBlocked, stateAuth, stateSpendLimit:
		// blocked（上限メニュー）・auth（再認証）・spend_limit（増枠）は「人が今やる」側で、
		// question / plan / permission と同じく人待ち。待っても自分では解けない。
		return activityHumanWait
	}
	return activityUnknown
}

// holdsWorkspace: この行があるあいだ tier2 は Workspace を止めてはならないか。
//
// **機械が動いているときだけ**。人待ち（question / plan / permission / blocked / auth /
// spend_limit）は理由にならない — 人待ちは何日でも続きうるので、それでコンテナを起こし
// 続けるとそのまま課金になる（docs/log/75 §75.1。question が唯一の例外として残っていた頃が
// 「AUQ が出ていると Workspace が永久に停止しない」の原因そのものだった）。
//
// 畳んでも失われないことは持ち越し（docs/log/75 §75.6）が担保する: 保留中の質問/プラン/許可は
// halt の直前に carried へ退避され、Console から答えれば再開して届く。
func holdsWorkspace(s sessionWire) bool {
	return sessionActivity(s) == activityMachineBusy
}

// tier1Reapable: tier1（セッション halt）の対象か。畳んでよい＝ machineBusy でも
// unknown でもないもの。どのタイムアウトを当てるかは呼び出し側が分類で決める
// （idleWait は session_idle_timeout、humanWait は interaction_idle_timeout）。
func tier1Reapable(s sessionWire) bool {
	a := sessionActivity(s)
	return a == activityIdleWait || a == activityHumanWait
}

// keepAwake reports whether a user pin is still in force.
//
// 読めない値は「ピンされていない」に倒す: この文字列は af 自身の Agent が書くので、
// 壊れているのはバグであり、そのバグが「Workspace が永久に止まらない」＝黙って課金し
// 続ける、という形で表に出るのは最悪の縮退である。逆側（守り損ねる）はジョブが 1 本
// 落ちるだけで、利用者が押し直せる。
func keepAwake(until string, now time.Time) bool {
	if until == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, until)
	if err != nil {
		return false
	}
	return now.Before(t)
}

// tier1Foldable は kind を「halt して安全か」で分ける（docs/log/75 P5）。
//
// halt は resumable な停止（claude は --resume、managed は runtime handle の再接続）
// なので、エージェントのセッションはどの kind でも畳んでよい。**shell / ssm だけが
// 例外**で、こちらの halt は走っているジョブごと殺すことを意味し、しかも af からは
// 何が走っているのか見えない（前景コマンド名では放置された less とビルドが区別できず、
// ssm は常に aws を張る）。守りたいものがあるときは「自動停止しない」ピンで宣言する。
//
// 空文字は kind を持たない古いセッション＝ claude（normalizeKind と同じ既定）。
func tier1Foldable(kind string) bool {
	switch kind {
	case "shell", "ssm":
		return false
	}
	return true
}
