package main

// セッション 1 行を「コンテナを起こし続ける理由になるか」「畳んでよいか」へ落とす分類
// （docs/75 §75.5）。reaper の 2 段はここだけを見る。
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
	// 保留中の対話を持ち越す**必要がある（docs/75 §75.6）。コンテナを起こし続ける
	// 理由にはならない — 人待ちは何日でも続きうる。
	activityHumanWait
	// activityMachineBusy: 機械が動いている。触ってはならない。
	activityMachineBusy
)

// 状態名。Agent 側（internal/agents/notify.go・internal/status）が正で、ここは
// ワイヤ越しに来る文字列の写し。
const (
	stateWorking    = "working"
	stateIdle       = "idle"
	stateQuestion   = "question"
	statePlan       = "plan"
	statePermission = "permission"
	stateBlocked    = "blocked"
	stateAuth       = "auth"
	stateLimited    = "limited"
	stateSpendLimit = "spend_limit"
)

// sessionActivity classifies one live session row.
//
// 生きていない行は activityUnknown（畳む対象でも、起こし続ける理由でもない）。
func sessionActivity(s sessionWire) activity {
	if !s.Alive {
		return activityUnknown
	}
	// BackgroundBusy は state と直交する: state は idle でも run_in_background の
	// ジョブ・in-process のサブエージェント / Workflow・S 状態の背景シェルが走って
	// いることがある（Agent の WireLive が立てる）。reaper はこれを一度も見ておらず、
	// 走っている背景作業ごと halt / stop していた。**machineBusy 側で最初に見る。**
	if s.BackgroundBusy {
		return activityMachineBusy
	}
	switch s.State {
	case stateWorking:
		return activityMachineBusy
	case stateIdle, stateLimited:
		// limited は「時計待ち」。リセット時刻に CP の定時実行が起こす（docs/47 §4-9）
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
// ★過渡的な例外つき（docs/75 P0）: question は本来 humanWait だが、持ち越し（P1）が
// 入るまでは畳むと利用者の判断が無言で消えるので、今はまだ「起こし続ける」側に残す。
// P2 でこの例外を外すと、この関数は machineBusy だけを見る形になる。
func holdsWorkspace(s sessionWire) bool {
	if sessionActivity(s) == activityMachineBusy {
		return true
	}
	return s.Alive && s.State == stateQuestion
}

// tier1Reapable: tier1（セッション halt）の対象か。
//
// ★過渡的（docs/75 P0）: 現行の reapableIdle と同じ集合を、分類の言葉で書き直しただけ。
// spend_limit は分類上 humanWait だが、既に tier1 の対象なので明示的に残す。P2 で
// humanWait 全体へ広げるときにこの例外は消える。
func tier1Reapable(s sessionWire) bool {
	switch sessionActivity(s) {
	case activityIdleWait:
		return true
	case activityHumanWait:
		return s.State == stateSpendLimit
	}
	return false
}
