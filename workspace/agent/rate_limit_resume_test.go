package main

// 利用上限エピソードの状態機械（docs/47 §4-4）。ペイン判定は internal/tmuxx の
// ゴールデンコーパス、リセット時刻の決め方は internal/agents/claude が押さえているので、
// ここで見るのは配線の側: 何回キーを送るか・いつ予約するか・設定が何を左右するか・
// エピソードをいつ畳むか。tmux も CP も持たないので副作用は差し替える。

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// rateLimitFixture isolates the state store (HOME 直下) and replaces the two side
// effects, returning counters for what the code tried to do.
type rateLimitFixture struct {
	dismissed   int
	scheduled   int
	deleted     []string
	dismissOK   bool
	scheduleAt  time.Time
	scheduleErr error
	resetAt     time.Time
	resetOK     bool
	resetSource string // 時刻の判断材料（banner / banner+capture / capture）
}

func newRateLimitFixture(t *testing.T) *rateLimitFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	f := &rateLimitFixture{dismissOK: true, resetOK: true, resetSource: "banner"}
	origDismiss, origPut := dismissRateLimitModal, putRateLimitSchedule
	origDrop, origReset := dropRateLimitSchedule, rateLimitResetAt
	dismissRateLimitModal = func(string) bool { f.dismissed++; return f.dismissOK }
	putRateLimitSchedule = func(_ session.Meta, at time.Time) (string, error) {
		f.scheduled++
		f.scheduleAt = at
		if f.scheduleErr != nil {
			return "", f.scheduleErr
		}
		return "sch_test", nil
	}
	dropRateLimitSchedule = func(id string) { f.deleted = append(f.deleted, id) }
	rateLimitResetAt = func(string, time.Time) (time.Time, string, bool) {
		return f.resetAt, f.resetSource, f.resetOK
	}
	t.Cleanup(func() {
		dismissRateLimitModal, putRateLimitSchedule = origDismiss, origPut
		dropRateLimitSchedule, rateLimitResetAt = origDrop, origReset
	})
	return f
}

func setRateLimitPref(t *testing.T, on bool) {
	t.Helper()
	p := uiPrefsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"rateLimitAutoResume": on})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func rlMeta() session.Meta {
	return session.Meta{Name: "rl1", Dir: "/tmp/rl1", Kind: session.KindClaude}
}

func stateOf(t *testing.T, name string) rateLimitState {
	t.Helper()
	st, _ := rateLimitStates.Read(name)
	return st
}

// TestRateLimitRecoverBooksThenDismisses: 1 回の検知で「予約 → 解除」まで進み、同じ
// エピソードで二度目の予約もキー送信もしないこと。解除でメニューが消えるとこの検知
// 経路は二度と開かないので、予約が先に済んでいることが要点。
func TestRateLimitRecoverBooksThenDismisses(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	f.resetAt = now.Add(3 * time.Hour)
	m := rlMeta()

	rateLimitRecover(m, stateOf(t, m.Name), now, true, claude.LimitWindow)
	st := stateOf(t, m.Name)
	if f.scheduled != 1 || st.ScheduleID != "sch_test" {
		t.Fatalf("予約 = %d 回 / id=%q, want 1 回 / sch_test", f.scheduled, st.ScheduleID)
	}
	if !f.scheduleAt.Equal(f.resetAt) {
		t.Errorf("予約時刻 = %v, want %v", f.scheduleAt, f.resetAt)
	}
	if f.dismissed != 1 || !st.Dismissed {
		t.Fatalf("解除 = %d 回 / dismissed=%v, want 1 回 / true", f.dismissed, st.Dismissed)
	}
	// メニューがまだ見えている（解除の反映前）状態でもう一度回っても撃ち直さない。
	rateLimitRecover(m, stateOf(t, m.Name), now.Add(rateLimitWatchInterval), true, claude.LimitWindow)
	if f.scheduled != 1 || f.dismissed != 1 {
		t.Errorf("2 回目の tick で 予約=%d 解除=%d — エピソード内で撃ち直している", f.scheduled, f.dismissed)
	}
}

// TestRateLimitRecoverDoesNotDependOnSessionOrigin: 利用上限の監視はセッションの
// 出自やオペレーター会話への紐付けを条件にしない。Console から直接起動した独立
// セッション（origin=user / originConv 空）にも、アシスタント起点のセッションと
// 同じ予約・解除が走ることを固定する。
func TestRateLimitRecoverDoesNotDependOnSessionOrigin(t *testing.T) {
	for _, tc := range []struct {
		name       string
		origin     string
		originConv string
	}{
		{"Console から直接起動", session.OriginUser, ""},
		{"アシスタント起点", session.OriginOperator, "a1b2c3d"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRateLimitFixture(t)
			now := time.Now()
			f.resetAt = now.Add(time.Hour)
			m := rlMeta()
			m.Name = "rl-" + tc.origin
			m.Origin = tc.origin
			m.OriginConv = tc.originConv

			rateLimitRecover(m, stateOf(t, m.Name), now, true, claude.LimitWindow)
			if f.scheduled != 1 || f.dismissed != 1 {
				t.Fatalf("origin=%q originConv=%q: 予約=%d 解除=%d, want 1 / 1",
					m.Origin, m.OriginConv, f.scheduled, f.dismissed)
			}
		})
	}
}

// TestRateLimitNotificationsAreOnceAndDeliveryBased: 上限到達はエピソードの初回だけ、
// 再開は once 予約の時刻ではなく内部プロンプトの配達確認後だけ通知する。watcher の
// 再走査や CP の再送で未読通知を増殖させないことも同時に固定する。
func TestRateLimitNotificationsAreOnceAndDeliveryBased(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	f.resetAt = now.Add(time.Hour)
	m := rlMeta()
	m.Title = "API 整理"
	session.WriteMeta(m)

	rateLimitRecover(m, stateOf(t, m.Name), now, true, claude.LimitWindow)
	got := notice.List()
	if len(got) != 1 || got[0].Kind != rateLimitNoticeReached || got[0].DisplayName != m.Title {
		t.Fatalf("初回通知 = %+v, want reached 1件", got)
	}
	// 同じメニューを watcher がもう一度見ても到達通知は増えない。
	rateLimitRecover(m, stateOf(t, m.Name), now.Add(rateLimitWatchInterval), true, claude.LimitWindow)
	if got = notice.List(); len(got) != 1 {
		t.Fatalf("再走査後の通知 = %+v, want 1件のまま", got)
	}

	// 予約しただけ、別プロンプト、手動発火では「再開した」と言わない。
	notifyRateLimitResumeDelivered(m.Name, rateLimitResumePromptFor("en"), turnSourceScheduleManual, now)
	notifyRateLimitResumeDelivered(m.Name, "unrelated scheduled prompt", turnSourceSchedule, now)
	if got = notice.List(); len(got) != 1 {
		t.Fatalf("未配達の再開通知が出た: %+v", got)
	}

	deliveredAt := f.resetAt.Add(time.Minute)
	notifyRateLimitResumeDelivered(m.Name, rateLimitResumePromptFor("en"), turnSourceSchedule, deliveredAt)
	notifyRateLimitResumeDelivered(m.Name, rateLimitResumePromptFor("en"), turnSourceSchedule, deliveredAt.Add(time.Second))
	got = notice.List()
	if len(got) != 2 || got[1].Kind != rateLimitNoticeResumed {
		t.Fatalf("配達後の通知 = %+v, want reached + resumed", got)
	}
	if got[1].Payload["resumeAt"] != stateOf(t, m.Name).ResumeAt {
		t.Errorf("resumeAt payload = %v, want %q", got[1].Payload["resumeAt"], stateOf(t, m.Name).ResumeAt)
	}
}

// TestRateLimitRecoverWithoutMenu: メニューを伴わない上限（モデル別上限＝1 行のエラーを
// 出して普通の入力欄へ戻る形。実測 2026-08-05 s6no6jv）でもエピソードが開き、上限到達が
// 通知されること。同時に、この形では**キーを送らない**ことを固定する — メニューが無いのに
// Enter を撃つと、それはそのまま利用者のプロンプト送信になる。
func TestRateLimitRecoverWithoutMenu(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	f.resetAt = now.Add(time.Hour)
	m := rlMeta()
	m.Title = "読者レポート生成"
	session.WriteMeta(m)

	rateLimitRecover(m, stateOf(t, m.Name), now, false, claude.LimitWindow)
	if f.dismissed != 0 {
		t.Errorf("解除 = %d 回, want 0 回（メニューが無いのに Enter を送っている）", f.dismissed)
	}
	got := notice.List()
	if len(got) != 1 || got[0].Kind != rateLimitNoticeReached {
		t.Fatalf("通知 = %+v, want reached 1件", got)
	}
	if st := stateOf(t, m.Name); st.At == "" || st.Menu {
		t.Errorf("状態 = %+v, want At あり / Menu=false", st)
	}
	// 同じ上限を watcher が何度見ても、通知もキーも増えない。
	rateLimitRecover(m, stateOf(t, m.Name), now.Add(rateLimitWatchInterval), false, claude.LimitWindow)
	if len(notice.List()) != 1 || f.dismissed != 0 {
		t.Errorf("再走査で 通知=%d 解除=%d — エピソード内で撃ち直している", len(notice.List()), f.dismissed)
	}
}

// TestRateLimitWithoutMenuNeedsBannerTime: メニューを伴わない上限では、リセット時刻は
// バナー由来のときだけ信用する。statusline 捕捉のフォールバック（source=capture）が答えるのは
// アカウントの 5時間/週次の窓で、モデル別上限は別の窓だから — そこで再開しても同じ上限に
// 当たるだけ。メニューを伴う形（＝アカウントの窓）では従来どおり捕捉も使う。
func TestRateLimitWithoutMenuNeedsBannerTime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		onMenu  bool
		source  string
		wantSch int
	}{
		{"メニュー無し・捕捉のみ", false, "capture", 0},
		{"メニュー無し・バナーあり", false, "banner", 1},
		{"メニュー無し・バナー＋捕捉", false, "banner+capture", 1},
		{"メニューあり・捕捉のみ", true, "capture", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRateLimitFixture(t)
			now := time.Now()
			f.resetAt, f.resetSource = now.Add(time.Hour), tc.source
			m := rlMeta()

			rateLimitRecover(m, stateOf(t, m.Name), now, tc.onMenu, claude.LimitWindow)
			if f.scheduled != tc.wantSch {
				t.Errorf("予約 = %d 回, want %d 回（source=%s onMenu=%v）",
					f.scheduled, tc.wantSch, tc.source, tc.onMenu)
			}
			if st := stateOf(t, m.Name); tc.wantSch == 0 && st.ResumeAt != "" {
				t.Errorf("ResumeAt = %q, want 空（時刻を信用できない）", st.ResumeAt)
			}
		})
	}
}

// TestRateLimitDismissRetriesAreBounded: 解除できないときは間隔を空けて数回だけ試し、
// 上限に達したら諦めること（選択が動いている・TUI の形が変わった、のどちらでも叩き
// 続けて直るものではない）。
func TestRateLimitDismissRetriesAreBounded(t *testing.T) {
	f := newRateLimitFixture(t)
	f.dismissOK = false
	now := time.Now()
	f.resetAt = now.Add(time.Hour)
	m := rlMeta()

	for i := 0; i < maxRateLimitDismissTries+3; i++ {
		rateLimitRecover(m, stateOf(t, m.Name), now.Add(time.Duration(i)*rateLimitWatchInterval), true, claude.LimitWindow)
	}
	if f.dismissed != maxRateLimitDismissTries {
		t.Fatalf("解除試行 = %d 回, want %d 回で打ち止め", f.dismissed, maxRateLimitDismissTries)
	}
	if st := stateOf(t, m.Name); st.Dismissed {
		t.Error("解除できていないのに dismissed = true")
	}
	// 予約は 1 回のまま — 解除に失敗しても再開の予約を撃ち直さない。
	if f.scheduled != 1 {
		t.Errorf("予約 = %d 回, want 1 回", f.scheduled)
	}
}

// TestRateLimitDismissRunsWithSettingOff: 設定が左右するのは再開の予約だけで、メニュー
// の解除は OFF でも必ず行う（人が触るまでセッションは何も受け付けられないため）。
func TestRateLimitDismissRunsWithSettingOff(t *testing.T) {
	f := newRateLimitFixture(t)
	setRateLimitPref(t, false)
	now := time.Now()
	f.resetAt = now.Add(time.Hour)
	m := rlMeta()

	rateLimitRecover(m, stateOf(t, m.Name), now, true, claude.LimitWindow)
	if f.dismissed != 1 {
		t.Errorf("解除 = %d 回, want 1 回（設定 OFF でも解除はする）", f.dismissed)
	}
	if f.scheduled != 0 {
		t.Errorf("予約 = %d 回, want 0 回（設定 OFF）", f.scheduled)
	}
	if st := stateOf(t, m.Name); st.ResumeAt != "" {
		t.Errorf("ResumeAt = %q, want 空", st.ResumeAt)
	}
}

// TestRateLimitPastResetResumesSoon: 放置されて既にリセット時刻を過ぎたメニューは、
// 過去の時刻ではなく直近（now + lead）で予約する。
func TestRateLimitPastResetResumesSoon(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	f.resetAt = now.Add(-16 * time.Hour)
	m := rlMeta()

	rateLimitRecover(m, stateOf(t, m.Name), now, true, claude.LimitWindow)
	if f.scheduled != 1 {
		t.Fatalf("予約 = %d 回, want 1 回", f.scheduled)
	}
	if want := now.Add(rateLimitResumeLead); !f.scheduleAt.Equal(want) {
		t.Errorf("予約時刻 = %v, want %v（過ぎたリセットは最短で回す）", f.scheduleAt, want)
	}
}

// TestRateLimitNoResetTimeNoSchedule: 時刻を決められないときは何も仕込まない。当てずっぽう
// で起こしても、また上限に当たって同じメニューが出るだけ。解除だけはする。
func TestRateLimitNoResetTimeNoSchedule(t *testing.T) {
	f := newRateLimitFixture(t)
	f.resetOK = false
	m := rlMeta()

	rateLimitRecover(m, stateOf(t, m.Name), time.Now(), true, claude.LimitWindow)
	if f.scheduled != 0 {
		t.Errorf("予約 = %d 回, want 0 回", f.scheduled)
	}
	if f.dismissed != 1 {
		t.Errorf("解除 = %d 回, want 1 回", f.dismissed)
	}
}

// TestRateLimitFollowUpRetriesRegistration: メニューを消したあとは検知経路が開かないので、
// 登録に失敗したエピソードは状態ファイルを見て後続の tick がリトライする（回数は有界）。
func TestRateLimitFollowUpRetriesRegistration(t *testing.T) {
	f := newRateLimitFixture(t)
	f.scheduleErr = errTest{}
	now := time.Now()
	f.resetAt = now.Add(2 * time.Hour)
	m := rlMeta()

	rateLimitRecover(m, stateOf(t, m.Name), now, true, claude.LimitWindow)
	if f.scheduled != 1 || stateOf(t, m.Name).ScheduleID != "" {
		t.Fatalf("前提が崩れている: 予約=%d id=%q", f.scheduled, stateOf(t, m.Name).ScheduleID)
	}
	f.scheduleErr = nil
	rateLimitFollowUp(m, stateOf(t, m.Name), now.Add(rateLimitWatchInterval), true)
	if f.scheduled != 2 || stateOf(t, m.Name).ScheduleID != "sch_test" {
		t.Fatalf("リトライされていない: 予約=%d id=%q", f.scheduled, stateOf(t, m.Name).ScheduleID)
	}
	// 失敗し続けても無限には試さない。
	f.scheduleErr = errTest{}
	rateLimitStates.Write(m.Name, rateLimitState{At: now.Format(time.RFC3339), ScheduleTries: maxRateLimitScheduleTries})
	rateLimitFollowUp(m, stateOf(t, m.Name), now.Add(2*rateLimitWatchInterval), true)
	if f.scheduled != 2 {
		t.Errorf("試行上限を超えて登録を試みている（予約 = %d 回）", f.scheduled)
	}
}

// TestRateLimitEpisodeRetired: 予約時刻を過ぎたエピソードは、使い切った once スケジュール
// を消して畳む — 残すと Console の一覧に無効な行が溜まり、状態ファイルが次のエピソードの
// 検知（新しい上限）を抑止してしまう。
func TestRateLimitEpisodeRetired(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	f.resetAt = now.Add(time.Hour)
	m := rlMeta()
	rateLimitRecover(m, stateOf(t, m.Name), now, true, claude.LimitWindow)

	after := now.Add(time.Hour + rateLimitCleanupGrace + time.Minute)
	rateLimitFollowUp(m, stateOf(t, m.Name), after, true)
	if len(f.deleted) != 1 || f.deleted[0] != "sch_test" {
		t.Errorf("使い終わったスケジュールの削除 = %v, want [sch_test]", f.deleted)
	}
	if _, ok := rateLimitStates.Read(m.Name); ok {
		t.Error("エピソードの状態ファイルが残っている — 次の上限で予約されなくなる")
	}
	// 次の上限は新しいエピソードとして扱われる。
	f.resetAt = after.Add(2 * time.Hour)
	rateLimitRecover(m, stateOf(t, m.Name), after, true, claude.LimitWindow)
	if f.scheduled != 2 {
		t.Errorf("予約 = %d 回, want 2 回（新しいエピソード）", f.scheduled)
	}
}

// TestRateLimitSpendLimitNeverSchedules: 支出・残高の上限（docs/47 §4-10）は「待てば解ける」
// 側の機械にかけない。予約すれば来ないリセットに向けて起こし、通知すれば利用者は待つ —
// どちらも増枠かクレジット追加という課金側の一手を遅らせるだけになる。
func TestRateLimitSpendLimitNeverSchedules(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	f.resetAt = now.Add(time.Hour) // 決まっても使わない（時刻の問題ではない）
	m := rlMeta()

	rateLimitRecover(m, stateOf(t, m.Name), now, false, claude.LimitSpend)
	if f.scheduled != 0 {
		t.Errorf("予約 = %d 回, want 0 回（来ないリセットへ向けて起こしている）", f.scheduled)
	}
	if f.dismissed != 0 {
		t.Errorf("解除 = %d 回, want 0 回（メニューは出ていない）", f.dismissed)
	}
	if got := notice.List(); len(got) != 0 {
		t.Errorf("通知 = %+v, want 0 件（「利用上限に達しました」は待てば解ける前提の文言）", got)
	}
	st := stateOf(t, m.Name)
	if !st.Spend || st.At == "" || st.ResumeAt != "" {
		t.Errorf("状態 = %+v, want Spend=true / At あり / ResumeAt 空", st)
	}
}

// TestRateLimitSpendEpisodeEndsWithTheTranscript: 支出のエピソードは時刻では畳まない。
// 12時間 TTL で消すと、増枠されるまで続いている事実の方が先に画面から消える（チップが
// 「入力待ち」へ戻る）。畳んでよいのは、生きているセッションの転写がもう上限ではなくなった
// ときだけ。
func TestRateLimitSpendEpisodeEndsWithTheTranscript(t *testing.T) {
	newRateLimitFixture(t)
	now := time.Now()
	m := rlMeta()
	rateLimitRecover(m, stateOf(t, m.Name), now, false, claude.LimitSpend)

	// TTL をはるかに過ぎても畳まない。
	late := now.Add(rateLimitEpisodeTTL + 6*time.Hour)
	if episodeStale(stateOf(t, m.Name), late) {
		t.Error("支出のエピソードが時刻で畳まれている — 増枠前にチップが消える")
	}
	// セッションが止まっているだけなら残す（再開しても同じ上限のままかもしれない）。
	rateLimitFollowUp(m, stateOf(t, m.Name), late, false)
	if _, ok := rateLimitStates.Read(m.Name); !ok {
		t.Error("停止中のセッションでエピソードを消している")
	}
	// 生きているセッションでここへ来た＝転写の末尾がもう上限ではない＝増枠された。
	rateLimitFollowUp(m, stateOf(t, m.Name), late, true)
	if _, ok := rateLimitStates.Read(m.Name); ok {
		t.Error("解消後もエピソードが残っている — 次の上限の検知を抑止する")
	}
}

// TestRateLimitResumeNoteOnFailedReport: 上限で止まったターンは turn-failed（＝再送しても
// 同じ）として報告されるので、そのままだと「対処を相談」で止まり、利用者にはあとから
// 勝手に再開したように見える。予約済みの事実が報告本文に乗ること。
func TestRateLimitResumeNoteOnFailedReport(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	f.resetAt = now.Add(90 * time.Minute)
	m := rlMeta()
	rateLimitRecover(m, stateOf(t, m.Name), now, true, claude.LimitWindow)

	body := reportBodyForTest("表示名", m.Name, reportKindAnswerReady, reportReasonTurnFailed)
	if !strings.Contains(body, "利用上限による停止です") ||
		!strings.Contains(body, f.resetAt.Local().Format("1月2日 15:04")) {
		t.Errorf("報告本文に自動再開の予約が出ていない:\n%s", body)
	}
	// 予約が無いセッション（普通の失敗）には足さない。
	if other := reportBodyForTest("表示名", "rl-none", reportKindAnswerReady, reportReasonTurnFailed); strings.Contains(other, "利用上限による停止です") {
		t.Errorf("予約の無いセッションの失敗報告に上限の注記が出ている:\n%s", other)
	}
	// 完了報告には足さない。
	if done := reportBodyForTest("表示名", m.Name, reportKindAnswerReady, ""); strings.Contains(done, "利用上限による停止です") {
		t.Errorf("完了報告に上限の注記が出ている:\n%s", done)
	}
}

// TestDismissRateLimitModalLive drives the real key path against a real tmux pane:
// メニューのフレームを描いて 1 行入力を待つプログラムを立て、Enter が届いたら待機
// フレームへ切り替える。押した／読み直したの往復が本当に成立するかは、ここでしか
// 確かめられない（純関数側の判定はゴールデンコーパスが持っている）。
func TestDismissRateLimitModalLive(t *testing.T) {
	isolateAgentState(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	name := "ratelimit-live"
	tn := session.TmuxName(name)
	// Enter を受け取ったら画面を消して待機フレームに差し替える（claude がメニューを
	// 閉じたときと同じ「確認フッタが消える」変化）。
	script := `cat internal/tmuxx/testdata/footers/modal_rate_limit.txt; read x; ` +
		`printf '\033[2J\033[H'; cat internal/tmuxx/testdata/footers/idle_bypass_hint.txt; sleep 60`
	if out, err := tmuxx.Cmd("new-session", "-d", "-s", tn, "-x", "200", "-y", "50", "sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v\n%s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !tmuxx.AtRateLimitModal(name) {
		time.Sleep(50 * time.Millisecond)
	}
	if !tmuxx.AtRateLimitModal(name) {
		t.Fatal("メニューのフレームが描けていない（前提が崩れている）")
	}
	if !tmuxx.DismissRateLimitModal(name) {
		t.Fatal("DismissRateLimitModal = false — Enter が届いていないか、消えたことを確認できていない")
	}
	if tmuxx.AtRateLimitModal(name) {
		t.Error("解除後もメニュー判定が真のまま")
	}
}

type errTest struct{}

func (errTest) Error() string { return "CP 到達不能（テスト）" }
