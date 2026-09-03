package sessionx

// 利用上限で止まったセッションが「入力待ち」ではなく 制限解除待ち（agents.StateLimited）
// として読まれること（docs/log/47 §4-9）。上限モーダルの blocked（＝人がペインで選ぶまで動か
// ない）は別のテストが押さえているので、ここで見るのはその**後**の姿: メニューは自動解除
// 済み、あるいはそもそもメニューを出さない形で、ペインは待機プロンプトに戻っている。
//
// 誤検知側（まだ／もう上限ではないのに 制限解除待ち を名乗る）も同じ重さで固定する。
// 待ちだと言われた行に利用者は何もせず放置するので、嘘の待ちは「壊れているのに気づけ
// ない」に化ける。

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// atLimitProbe replaces the transcript probe and counts the calls: 一覧ポーリングの経路
// なので「エピソードが無いセッションでは転写を読まない」ことも確認できるようにする。
// kind が空なら「上限ではない」。
func atLimitProbe(t *testing.T, kind claude.LimitKind) *int {
	t.Helper()
	n := 0
	orig := claudeUsageLimitAbort
	claudeUsageLimitAbort = func(string) (claude.Abort, claude.LimitKind, bool) {
		n++
		if kind == "" {
			return claude.Abort{}, "", false
		}
		return claude.Abort{Msg: "You've hit your session limit."}, kind, true
	}
	t.Cleanup(func() { claudeUsageLimitAbort = orig })
	return &n
}

// limitedMeta writes a claude meta and opens a usage-limit episode for it.
func limitedMeta(t *testing.T, name string, st RateLimitState) session.Meta {
	t.Helper()
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)
	if err := RateLimitStates.Write(name, st); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestWireSessionShowsRateLimitWait: 予約済みのエピソードがあり、転写の末尾がまだ上限で
// 切れたターンのままなら、一覧もチャットも 制限解除待ち＋再開予定時刻を出す。
func TestWireSessionShowsRateLimitWait(t *testing.T) {
	isolateAgentState(t)
	calls := atLimitProbe(t, claude.LimitWindow)
	now := time.Now()
	resume := now.Add(2 * time.Hour).Format(time.RFC3339)
	m := limitedMeta(t, "rlwire1", RateLimitState{
		At: now.Format(time.RFC3339), Menu: true, Dismissed: true, ResumeAt: resume, ScheduleID: "sch_x",
	})

	s := wireSession(m, true)
	if s.State != agents.StateLimited {
		t.Fatalf("wireSession state = %q, want %q（上限待ちが 入力待ち と同じに見えている）", s.State, agents.StateLimited)
	}
	if s.RateLimitResumeAt != resume {
		t.Errorf("rateLimitResumeAt = %q, want %q（いつ動くかを言えていない）", s.RateLimitResumeAt, resume)
	}
	// 一覧のバッジとチャット／ミラーのチップは同じ状態を名乗る必要がある。
	if got := DriveState(m, true, false); got != agents.StateLimited {
		t.Errorf("DriveState = %q, want %q（一覧と本文でチップが食い違う）", got, agents.StateLimited)
	}
	if *calls == 0 {
		t.Error("転写を一度も見ていない — エピソードファイルだけで待ちを名乗っている")
	}
}

// TestWireSessionShowsSpendLimit: 支出・残高の上限（docs/log/47 §4-10）は 制限解除待ち では
// なく専用の状態で出す。同じ 429 でも待っても解けないので、「待て」と読める表示にすると
// 利用者は来ないリセットを待つ。再開予定時刻は存在しない（予約しない）ので空のまま。
func TestWireSessionShowsSpendLimit(t *testing.T) {
	isolateAgentState(t)
	atLimitProbe(t, claude.LimitSpend)
	now := time.Now()
	m := limitedMeta(t, "rlwire6", RateLimitState{At: now.Format(time.RFC3339), Spend: true})

	s := wireSession(m, true)
	if s.State != agents.StateSpendLimit {
		t.Fatalf("wireSession state = %q, want %q", s.State, agents.StateSpendLimit)
	}
	if s.RateLimitResumeAt != "" {
		t.Errorf("rateLimitResumeAt = %q, want 空（来ない再開時刻を出している）", s.RateLimitResumeAt)
	}
	if got := DriveState(m, true, false); got != agents.StateSpendLimit {
		t.Errorf("DriveState = %q, want %q（一覧と本文でチップが食い違う）", got, agents.StateSpendLimit)
	}
}

// TestWireSessionSpendKindComesFromTranscript: 種別はエピソードファイルの記録ではなく
// 転写から今引き直す。増枠されて今度は窓の上限に当たった、のような遷移でも表示が追随する。
func TestWireSessionSpendKindComesFromTranscript(t *testing.T) {
	isolateAgentState(t)
	atLimitProbe(t, claude.LimitWindow)
	now := time.Now()
	m := limitedMeta(t, "rlwire7", RateLimitState{
		At: now.Format(time.RFC3339), Spend: true, // 古い記録は支出のまま
		ResumeAt: now.Add(time.Hour).Format(time.RFC3339),
	})

	if s := wireSession(m, true); s.State != agents.StateLimited {
		t.Errorf("state = %q, want %q（転写は窓の上限を指している）", s.State, agents.StateLimited)
	}
}

// TestWireSessionRateLimitClearedByTranscript: エピソードは開いたままでも、転写の末尾が
// 上限のレコードでなくなったら（利用者がモデルを切り替えた・別のターンが走った）待ちは
// 名乗らない。ファイルの寿命（予約時刻＋猶予／12時間 TTL）だけを見ると、ここで嘘になる。
func TestWireSessionRateLimitClearedByTranscript(t *testing.T) {
	isolateAgentState(t)
	atLimitProbe(t, "")
	now := time.Now()
	m := limitedMeta(t, "rlwire2", RateLimitState{
		At: now.Format(time.RFC3339), ResumeAt: now.Add(time.Hour).Format(time.RFC3339),
	})

	if s := wireSession(m, true); s.State == agents.StateLimited {
		t.Errorf("state = %q — 上限が解けた後も 制限解除待ち のまま貼り付いている", s.State)
	}
	if got := DriveState(m, true, false); got == agents.StateLimited {
		t.Errorf("DriveState = %q — 同上", got)
	}
}

// TestWireSessionRateLimitEpisodeExpired: 予約時刻＋猶予を過ぎた（＝畳まれる直前の）
// エピソードでは待ちを名乗らない。過ぎた時刻の「制限解除待ち」は待てば直ると読めない。
func TestWireSessionRateLimitEpisodeExpired(t *testing.T) {
	isolateAgentState(t)
	calls := atLimitProbe(t, claude.LimitWindow)
	now := time.Now()
	m := limitedMeta(t, "rlwire3", RateLimitState{
		At:       now.Add(-6 * time.Hour).Format(time.RFC3339),
		ResumeAt: now.Add(-RateLimitCleanupGrace - time.Hour).Format(time.RFC3339),
	})

	if s := wireSession(m, true); s.State == agents.StateLimited {
		t.Errorf("state = %q — 終わったエピソードで待ちを名乗っている", s.State)
	}
	if *calls != 0 {
		t.Error("終わったエピソードのために転写を読んでいる（先に状態ファイルで刈る）")
	}
}

// TestWireSessionWithoutEpisodeSkipsTranscript: 上限に当たっていない普通のセッションでは
// 状態も変えず、転写にも触らない。一覧ポーリングは全 claude セッションを毎回なめるので、
// ここで転写を読むと「滅多に起きないことの検出」がポーリングの常時コストになる。
func TestWireSessionWithoutEpisodeSkipsTranscript(t *testing.T) {
	isolateAgentState(t)
	calls := atLimitProbe(t, claude.LimitWindow)
	m := session.Meta{Name: "rlwire4", Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)

	if s := wireSession(m, true); s.State == agents.StateLimited {
		t.Errorf("state = %q — エピソードが無いのに待ちを名乗っている", s.State)
	}
	if *calls != 0 {
		t.Errorf("転写を %d 回読んだ — エピソードが無いセッションでは読まない", *calls)
	}
}

// TestWireSessionRateLimitOnlyWhileAlive: 停止中のセッションは 停止中 のまま。上限の
// エピソードは生きているセッションの状態で、行の意味を上書きしてはいけない。
func TestWireSessionRateLimitOnlyWhileAlive(t *testing.T) {
	isolateAgentState(t)
	atLimitProbe(t, claude.LimitWindow)
	now := time.Now()
	m := limitedMeta(t, "rlwire5", RateLimitState{
		At: now.Format(time.RFC3339), ResumeAt: now.Add(time.Hour).Format(time.RFC3339),
	})

	if s := wireSession(m, false); s.State == agents.StateLimited || s.RateLimitResumeAt != "" {
		t.Errorf("停止中の state = %q / resumeAt = %q, want 空", s.State, s.RateLimitResumeAt)
	}
}
