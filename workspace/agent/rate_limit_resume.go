package main

// 利用上限モーダルの自動解除と、リセット時刻での自動再開（docs/47 §4-4）。
//
// 上限に当たった claude はメニュー（/rate-limit-options）を出してキー入力待ちで止まる。
// docs/47 §4-3 はそれを blocked として**読める**ようにしただけで、止まったままなのは
// 変わらない。ここが実際に前へ進める部分:
//
//	① メニューの既定（1. Stop and wait for limit to reset）を確定して待機プロンプトへ戻す。
//	   設定に関わらず必ず行う — メニューが出ている間セッションは何も受け付けず、選ぶ
//	   のは無課金側なので「判断の代行」にはならない（tmuxx.DismissRateLimitModal）。
//	② 上限が解ける時刻（claude.ResetAt）に、同じセッションへ「続けて」を送る一回限りの
//	   スケジュールを CP へ登録する。設定「利用上限リセット後の自動再開」で ON/OFF。
//
// なぜ待ち合わせを CP のスケジューラに預けるか: ①でメニューを消すとセッションは普通の
// 入力待ちになるので、リセットまでの数時間で idle-reaper が WS ごと停止させる（させて
// よい — ターンは終わっている）。停止中にプロセス内タイマーは生き残れないが、CP の
// 定時実行は wake_policy=wake で WS を起こしてから注入できる（docs/38 P6 の
// session_mode=reuse＝既存セッションへの投入）。
//
// 順序は「②→①」。①が成功するとメニューは消え、この検知経路は二度と開かないので、
// 先に再開を仕込んでおく。仕込み損ねた場合だけ、状態ファイルを見て後続の tick が
// リトライする（rateLimitFollowUp）。

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

const (
	rateLimitNoticeReached = "rate-limit-reached"
	rateLimitNoticeResumed = "rate-limit-resumed"
	// rateLimitWatchInterval is the sweep cadence. 1 分で十分 — 相手は数時間単位の待ちで、
	// この間隔がそのまま「メニューが出てから解除するまでの最大遅延」になる。
	rateLimitWatchInterval = time.Minute
	// maxRateLimitDismissTries bounds the Enter presses per episode. 解除できないのは
	// 選択が 2 に動いている（人が触っている）か TUI の形が変わったときで、どちらも
	// 叩き続けて直るものではない。上限に達したら人待ちの blocked に戻すだけ。
	maxRateLimitDismissTries = 3
	// maxRateLimitScheduleTries bounds the CP registration attempts per episode (CP が
	// 一時的に届かないだけなら次の tick で通る)。
	maxRateLimitScheduleTries = 5
	// rateLimitResumeLead is the floor on how soon a resume may be scheduled. リセット
	// 時刻が既に過ぎている（メニューが何時間も放置されていた）ときの即時再開もここを通る
	// — スケジューラの tick が 1 分刻みなので、それ以下を指定しても意味が無い。
	rateLimitResumeLead = 2 * time.Minute
	// rateLimitCleanupGrace is how long after the scheduled instant the episode is kept
	// before the spent once-schedule is deleted and the state file dropped.
	rateLimitCleanupGrace = 30 * time.Minute
	// rateLimitEpisodeTTL drops an episode that never got a resume time (自動再開 OFF、
	// あるいは時刻を決められなかった）so a stale file can't suppress the next episode.
	rateLimitEpisodeTTL = 12 * time.Hour
)

// rateLimitState is one usage-limit episode for one session: it exists from the moment
// the menu is seen until the scheduled resume has come and gone. 専用ファイルにするのは
// resumeState と同じ理由 — 複数の書き手が居る Meta に相乗りしない。
type rateLimitState struct {
	At            string `json:"at"`                      // エピソード検知時刻（RFC3339）
	Menu          bool   `json:"menu,omitempty"`          // メニューを伴う上限（＝アカウントの窓）
	DismissTries  int    `json:"dismissTries,omitempty"`  // Enter を送った回数
	Dismissed     bool   `json:"dismissed,omitempty"`     // メニューが消えたことを確認済み
	LastTry       string `json:"lastTry,omitempty"`       // 直近の解除試行
	ResumeAt      string `json:"resumeAt,omitempty"`      // 自動再開の予定時刻（RFC3339）
	Source        string `json:"source,omitempty"`        // 時刻の判断材料（banner / capture …）
	ScheduleID    string `json:"scheduleId,omitempty"`    // CP 側スケジュール id
	ScheduleTries int    `json:"scheduleTries,omitempty"` // 登録試行回数
}

var rateLimitStates = fstore.JSON[rateLimitState](paths.AgentConfigDir, "session-rate-limit", ".json")

// 副作用は差し替え可能にしておく（テストは tmux も CP も持たない）。
var (
	dismissRateLimitModal = tmuxx.DismissRateLimitModal
	putRateLimitSchedule  = createRateLimitSchedule
	dropRateLimitSchedule = deleteRateLimitSchedule
	rateLimitResetAt      = claude.ResetAt
	claudeUsageLimitAbort = claude.UsageLimitAbort
)

// startRateLimitWatch runs the sweep for the life of the agent.
//
// なぜ専用のループが要るか（一覧ポーリングに相乗りしない理由）: 解除も再開も**誰も見て
// いないとき**に効かなければ意味が無い。wireSession は Console/CP が叩いたときだけ走る
// 読み取り経路で、そこに副作用を置くと「誰かが画面を開いていれば直る」機能になる。
func startRateLimitWatch() {
	go func() {
		time.Sleep(45 * time.Second) // 起動直後の tmux/CP 立ち上がりを待つ
		for {
			rateLimitTick(time.Now())
			time.Sleep(rateLimitWatchInterval)
		}
	}()
}

// rateLimitTick is one sweep: every claude session is classified as "at its usage limit
// now" (recover) or "has an open episode" (follow up / clean up). ListMetas is deliberately
// the only population gate: origin=operator / owner conversation / instruction-ledger
// presence are irrelevant, so a standalone session launched directly from Console is
// recovered exactly like one launched or steered by an assistant.
//
// 検知は 2 経路ある。上限の形がひとつではないため（claude.UsageLimitAbort のコメント）:
// アカウントの窓はメニューでペインを人間待ちに固定し、モデル別の上限は 1 行のエラーを
// 出して普通の入力欄へ戻る。後者はペインからは見分けられないので転写の末尾で拾う。
// ペインを先に見るのは、メニューが出ている＝今まさに固まっている方が緊急だから。
func rateLimitTick(now time.Time) {
	for _, m := range session.ListMetas() {
		if normalizeKind(m.Kind) != session.KindClaude {
			continue
		}
		st, has := rateLimitStates.Read(m.Name)
		if sessionAlive(m) {
			if tmuxx.ReadPane(m.Name).RateLimitMenu {
				rateLimitRecover(m, st, now, true)
				continue
			}
			if _, atLimit := claudeUsageLimitAbort(session.UUID(m.Dir, m.Name)); atLimit {
				rateLimitRecover(m, st, now, false)
				continue
			}
		}
		if has {
			rateLimitFollowUp(m, st, now)
		}
	}
}

// rateLimitRecover handles a session stopped by its usage limit. onMenu says which form it
// is: true = ペインが /rate-limit-options メニューで固定されている、false = 転写の末尾が
// 上限で切れたターン（メニューは無く、セッションは入力を受け付けられる）。
func rateLimitRecover(m session.Meta, st rateLimitState, now time.Time, onMenu bool) {
	if episodeStale(st, now) {
		st = rateLimitState{} // 前のエピソードは終わっている — 新しい上限として扱う
	}
	if st.At == "" {
		st.At = now.Format(time.RFC3339)
		if onMenu {
			log.Printf("rate-limit: %s が利用上限メニューで停止している", m.Name)
		} else {
			log.Printf("rate-limit: %s のターンが利用上限で打ち切られている", m.Name)
		}
	}
	// 単調に上げるだけ（下げない）: 同じエピソードの途中でメニューが出たら、それ以降は
	// メニューのあるエピソードとして扱ってよい。逆に消えたのは解除できた印なので戻さない。
	if onMenu {
		st.Menu = true
	}
	st = scheduleRateLimitResume(m, st, now)
	notifyRateLimitReached(m, st)

	if onMenu && !st.Dismissed && st.DismissTries < maxRateLimitDismissTries && !triedRecently(st, now) {
		st.DismissTries++
		st.LastTry = now.Format(time.RFC3339)
		// 送る前に記録する: 途中で落ちても回数が巻き戻らないようにして、キーを撃ち続けない。
		_ = rateLimitStates.Write(m.Name, st)
		if dismissRateLimitModal(m.Name) {
			st.Dismissed = true
			log.Printf("rate-limit: %s の利用上限メニューを解除した（1. リセットまで待つ）", m.Name)
		} else {
			log.Printf("rate-limit: %s のメニューを解除できなかった（%d/%d 回目）",
				m.Name, st.DismissTries, maxRateLimitDismissTries)
		}
	}
	_ = rateLimitStates.Write(m.Name, st)
}

// notifyRateLimitReached records the attention event once per episode. PutOnce's marker
// survives the CP draining the outbox, so a menu that remains visible across many watcher
// ticks cannot keep reappearing as unread. This event intentionally says only that the
// limit was reached: scheduling may be disabled or may still succeed on a later retry.
func notifyRateLimitReached(m session.Meta, st rateLimitState) {
	if st.At == "" {
		return
	}
	ev := notice.New(rateLimitNoticeReached, m.Name, m.Kind, session.Display(m))
	if at, err := time.Parse(time.RFC3339, st.At); err == nil {
		ev.CreatedAt = at.UTC().Format(time.RFC3339)
	}
	if err := notice.PutOnce("rate-limit-reached:"+m.Name+":"+st.At, ev); err != nil {
		log.Printf("rate-limit: %s の利用上限通知を保存できなかった: %v", m.Name, err)
	}
}

// notifyRateLimitResumeDelivered records a resume only after /input's delivery
// confirmation succeeded. The schedule instant alone is insufficient: overlap/target
// guards may skip it, and delivery may fail. The open episode + exact internal prompt +
// scheduler source keep an ordinary scheduled or Console prompt from impersonating it.
func notifyRateLimitResumeDelivered(name, prompt, source string, now time.Time) {
	if injectionSource(source) != turnSourceSchedule || !isRateLimitResumePrompt(prompt) {
		return
	}
	st, ok := rateLimitStates.Read(name)
	if !ok || st.ScheduleID == "" {
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok || normalizeKind(m.Kind) != session.KindClaude {
		return
	}
	ev := notice.New(rateLimitNoticeResumed, m.Name, m.Kind, session.Display(m))
	ev.CreatedAt = now.UTC().Format(time.RFC3339)
	ev.Payload["resumeAt"] = st.ResumeAt
	if err := notice.PutOnce("rate-limit-resumed:"+st.ScheduleID, ev); err != nil {
		log.Printf("rate-limit: %s の自動再開通知を保存できなかった: %v", m.Name, err)
	}
}

// rateLimitFollowUp runs for a session with an open episode whose menu is already gone:
// retry a registration that failed while the menu was still up, then retire the episode.
func rateLimitFollowUp(m session.Meta, st rateLimitState, now time.Time) {
	if next := scheduleRateLimitResume(m, st, now); next != st {
		st = next
		_ = rateLimitStates.Write(m.Name, st)
	}
	if !episodeStale(st, now) {
		return
	}
	// 使い切った once スケジュールを残さない（Console の一覧に無効な行が溜まる）。
	if st.ScheduleID != "" {
		dropRateLimitSchedule(st.ScheduleID)
	}
	rateLimitStates.Remove(m.Name)
}

// scheduleRateLimitResume registers the one-shot resume when it is wanted and not yet
// in place. 返り値は更新後の状態（呼び出し側が書く）。
func scheduleRateLimitResume(m session.Meta, st rateLimitState, now time.Time) rateLimitState {
	if st.ScheduleID != "" || !rateLimitAutoResumeEnabled() || st.ScheduleTries >= maxRateLimitScheduleTries {
		return st
	}
	at, source, ok := rateLimitResetAt(session.UUID(m.Dir, m.Name), now)
	// メニューを伴わない上限では、時刻はバナー（そのセッションが実際に受け取った文言）
	// からしか信用しない。resolveResetAt のフォールバックは statusline 捕捉＝アカウントの
	// 5時間 / 週次の窓だが、モデル別の上限はそれとは**別の窓**で、statusline には出て
	// こない（claude が statusLine へ詰めるのは five_hour と seven_day だけ）。実測
	// 2026-08-05 s6no6jv では上限に当たった時点で 5時間窓 23% / 週次 75%、フォールバックは
	// その日の 19:30（5時間窓のリセット）を返す — そこで再開しても同じ上限に当たるだけ。
	if ok && !st.Menu && !strings.HasPrefix(source, "banner") {
		ok = false
	}
	if !ok {
		// 時刻が決められない（バナーが読めず捕捉も使えない）。当てずっぽうで起こしても
		// また上限に当たるだけなので仕込まない。試行回数は消費する。
		st.ScheduleTries++
		log.Printf("rate-limit: %s のリセット時刻が読めなかったので自動再開は仕込まない", m.Name)
		return st
	}
	if floor := now.Add(rateLimitResumeLead); at.Before(floor) {
		at = floor // 既に過ぎている / 直前 — スケジューラの刻みに合わせて最短で回す
	}
	st.ScheduleTries++
	st.ResumeAt, st.Source = at.Format(time.RFC3339), source
	id, err := putRateLimitSchedule(m, at)
	if err != nil {
		log.Printf("rate-limit: %s の自動再開を登録できなかった: %v", m.Name, err)
		return st
	}
	st.ScheduleID = id
	log.Printf("rate-limit: %s の自動再開を %s に予約した（%s・schedule=%s）",
		m.Name, at.Local().Format("01/02 15:04"), source, id)
	return st
}

// triedRecently rate-limits the Enter presses inside one episode.
func triedRecently(st rateLimitState, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, st.LastTry)
	return err == nil && now.Sub(t) < rateLimitWatchInterval/2
}

// episodeStale reports whether an episode is finished: its resume time (plus the grace
// for the scheduler to fire) has passed, or it never got one and has aged out.
func episodeStale(st rateLimitState, now time.Time) bool {
	if st.At == "" {
		return true
	}
	if st.ResumeAt != "" {
		t, err := time.Parse(time.RFC3339, st.ResumeAt)
		return err != nil || now.After(t.Add(rateLimitCleanupGrace))
	}
	t, err := time.Parse(time.RFC3339, st.At)
	return err != nil || now.After(t.Add(rateLimitEpisodeTTL))
}

// createRateLimitSchedule books the resume with the CP scheduler (docs/38).
//
//	spec_kind=once           — 一回限り。発火すると自身を disable する。
//	session_mode=reuse       — 新規セッションではなく、止まった**そのセッション**へ投入する。
//	reuse_target=<name>      — pinned reuse（利用者のセッションなので rotation は効かない）。
//	missing_target_policy=fail — セッションが消えていたら作り直さない。上限で止まった作業を
//	                           引き継げない別セッションを勝手に生やす方が害が大きい。
//	overlap_policy=skip      — その時刻に人が既に動かしていたら黙って見送る。
//	wake_policy=wake         — 待っている間に WS が止まっていても起こして届ける（これが
//	                           プロセス内タイマーではなく CP に預ける理由そのもの）。
//	report=false             — この投入をオペレーター会話へ報告しない（owner_conv が空で
//	                           報告先が無い）。再開したターンが終われば「応答あり」通知は
//	                           通常どおり出るので、利用者から見えなくなる訳ではない。
func createRateLimitSchedule(m session.Meta, at time.Time) (string, error) {
	body, err := json.Marshal(map[string]any{
		"spec_kind":             "once",
		"spec":                  at.UTC().Format(time.RFC3339),
		"spec_label":            rateLimitScheduleLabel(m.Name),
		"tz":                    scheduleTZName(),
		"session_mode":          "reuse",
		"reuse_target":          m.Name,
		"missing_target_policy": "fail",
		"overlap_policy":        "skip",
		"wake_policy":           "wake",
		"agent_kind":            session.KindClaude,
		"prompt":                rateLimitResumePrompt(),
		"report":                false,
	})
	if err != nil {
		return "", err
	}
	out, err := cpScheduleDo(http.MethodPost, "/internal/schedules", body)
	if err != nil {
		return "", err
	}
	var wire struct {
		ID      string `json:"id"`
		Warning string `json:"warning"`
	}
	if json.Unmarshal([]byte(out), &wire) != nil || wire.ID == "" {
		return "", fmt.Errorf("スケジュール id を読めなかった: %s", out)
	}
	if wire.Warning != "" {
		log.Printf("rate-limit: %s", wire.Warning)
	}
	return wire.ID, nil
}

// scheduleTZName is the zone the CP renders this schedule's時刻 in. once の発火自体は
// 絶対時刻（UTC の RFC3339）で決まるので表示専用だが、"Local" のような CP 側で別物に
// 解決される名前を送っても意味が無いので、IANA 名を名乗れるときだけ送る。
func scheduleTZName() string {
	if tz := os.Getenv("TZ"); strings.Contains(tz, "/") {
		return tz
	}
	if n := time.Local.String(); strings.Contains(n, "/") {
		return n
	}
	return ""
}

// deleteRateLimitSchedule removes a spent once-schedule, best-effort.
func deleteRateLimitSchedule(id string) {
	if _, err := cpScheduleDo(http.MethodDelete, "/internal/schedules/"+url.PathEscape(id), nil); err != nil {
		log.Printf("rate-limit: 使い終わったスケジュール %s を消せなかった: %v", id, err)
	}
}

// rateLimitResumePrompt is the nudge sent when the limit lifts. 新しい指示は混ぜない
// （docs/47 §3-4 の再開文と同じ方針）。言語は表示言語に合わせる — 会話ごとの言語を
// 持たない以上、その利用者が読み書きしている言語が最良の推定。
func rateLimitResumePrompt() string {
	return rateLimitResumePromptFor(uiLocale())
}

func rateLimitResumePromptFor(locale string) string {
	if locale == "en" {
		return "The usage limit has reset. Continue the work that was cut off, from where it stopped. " +
			"This is an automatic resume — there is no new instruction. " +
			"If you cannot tell where it stopped, say so instead of starting something new."
	}
	return "利用上限がリセットされました。上限で中断した作業を、止まったところから続けてください。" +
		"これは自動再開なので新しい指示はありません。" +
		"どこで止まったか分からない場合は、新しい作業を始めずにその旨を伝えてください。"
}

func isRateLimitResumePrompt(prompt string) bool {
	prompt = strings.TrimSpace(prompt)
	return prompt == strings.TrimSpace(rateLimitResumePromptFor("ja")) ||
		prompt == strings.TrimSpace(rateLimitResumePromptFor("en"))
}

func rateLimitScheduleLabel(name string) string {
	if uiLocale() == "en" {
		return "auto-resume after usage limit (" + name + ")"
	}
	return "利用上限リセット後の自動再開（" + name + "）"
}
