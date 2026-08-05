package main

// 再送で直る中断（接続断・一時的なレート制限・ストリームの番犬）からの自動再開
// （docs/47 §4-6）。
//
// docs/47 §3-4 の再開はアシスタント主導だった: 中断 → 完了報告 → オペレーターが
// send_to_session で「続けて」を送る。これには2つの穴がある。
//
//	① 会話に紐付いていないセッション（Console から直接起動）は再開されない。報告先が
//	   無いので、中断の通知は出るが誰も再開させないまま止まる（§5 の積み残し）。
//	② 会話持ちでも、往復のたびにアシスタントのターンが1つ走る。中断は「利用者が既に
//	   頼んだ作業を走らせ直すだけ」で判断を含まないのに、判断のための LLM を経由する
//	   ぶんのトークンを毎回払っていた。
//
// よって再開の一手目は Agent 自身が直接送る（利用上限が既に例外としてそうしている
// ように — rate_limit_resume.go）。アシスタントは**打ち切ったときだけ**の受け皿に
// なる: 上限（maxAutoResumeAttempts）まで再送しても中断が続くなら、それは一時的な
// 不調ではないので、そこで初めて報告して利用者へエスカレーションする。
//
// ADR0030 §3 が Agent 直送を避けた第一の理由「誰が何を送ったか見えなくなる」は、
// docs/37/38 の注入元記録（recordInjection → ミラーのバッジ）で解消済み。再開の
// プロンプトは注入元 auto-resume として転写に残り、ミラーで見分けられる。
//
// **報告の抑止は「中断を握り潰す」ことではない。** 中断の通知（通知センター）は従来
// どおり出る。抑えるのは会話への報告＝アシスタントのターンだけで、再開後にターンが
// 完了すれば、その完了報告が指示1件を正しく閉じる（報告が2回から1回に減る）。
// 抑止の判断は chat_report_reconcile.go の collectAbortSignal / evalReportEvidence が
// abortResumeHolds を見て行う。

import (
	"log"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

const (
	// abortResumeWatchInterval is the sweep cadence. 上限エピソード（1分）より短いのは、
	// こちらの相手が「数秒で直っている一時的な不調」だから — 待ち時間はそのまま利用者の
	// 待ち時間になる。1 sweep のコストは claude セッションごとの転写末尾の読み取り。
	abortResumeWatchInterval = 30 * time.Second
	// abortResumeFirstDelay is how long to wait after the cut-off before the first
	// resume. 即時に撃たない理由は 529 / overloaded で、原因が消える前に再送しても
	// 同じ中断をもう一度引くだけだから（そしてそれは貴重な再試行を1回捨てる）。
	abortResumeFirstDelay = 30 * time.Second
	// abortResumeBackoff is the wait before a SECOND resume in the same episode. 1回目が
	// また中断で終わったということは相手側の不調が続いている。
	abortResumeBackoff = 5 * time.Minute
	// abortResumeMaxDeliverTries bounds the injection attempts that never reached the
	// session (ペインが読めない・注入が失敗する)。叩き続けて直る類ではないので、
	// 打ち切ってアシスタント／利用者へ渡す。
	abortResumeMaxDeliverTries = 3
	// abortResumeEpisodeTTL retires an episode that stopped making progress. 保険であって
	// 通常経路ではない（正常時は「転写の末尾が中断でなくなる」でエピソードが閉じる）—
	// これが無いと、書き込めない状態のファイルが報告を永久に抑止しうる。
	abortResumeEpisodeTTL = 30 * time.Minute
)

// 打ち切りの理由（GaveUp）。空 = まだ自動再開が引き受けている。
const (
	abortGaveUpCapped        = "capped"        // 再送したが中断が続く（連続 maxAutoResumeAttempts 回）
	abortGaveUpUndeliverable = "undeliverable" // 再開プロンプトをセッションへ届けられない
	abortGaveUpStale         = "stale"         // エピソードが TTL を過ぎた（進んでいない）
)

// abortResumeState is one cut-off episode for one session: it opens when the transcript
// tail is a retryable abort and closes when the tail is no longer one (＝再開できた／
// 利用者が自分で進めた／正常に終わった). 専用ファイルなのは rateLimitState と同じ理由。
type abortResumeState struct {
	At           string `json:"at"`                     // エピソード開始（中断レコードの時刻、無ければ検知時刻）
	Msg          string `json:"msg,omitempty"`          // 中断の文言（ログ・打ち切り時の理由）
	Attempts     int    `json:"attempts,omitempty"`     // 送れた再開プロンプトの数
	DeliverTries int    `json:"deliverTries,omitempty"` // 届かなかった試行の数
	LastTry      string `json:"lastTry,omitempty"`      // 直近の試行（成否によらず）
	GaveUp       string `json:"gaveUp,omitempty"`       // 非空 = 自動再開は手を引いた（報告の抑止も外れる）
}

var abortResumeStates = fstore.JSON[abortResumeState](paths.AgentConfigDir, "session-abort-resume", ".json")

// 副作用は差し替え可能にしておく（テストは tmux を持たない）。
var (
	abortResumeInject      = injectSessionPrompt
	abortResumeReadingPane = func(name string) tmuxx.PaneRead { return tmuxx.ReadPane(name) }
)

// startAbortResumeWatch runs the sweep in its own loop. 一覧ポーリングに相乗りしない
// 理由は rate_limit_resume.go と同じ: 誰も画面を見ていないときに効かなければ意味が無い。
func startAbortResumeWatch() {
	go func() {
		time.Sleep(40 * time.Second) // 起動直後の tmux 立ち上がりを待つ
		for {
			abortResumeTick(time.Now())
			time.Sleep(abortResumeWatchInterval)
		}
	}()
}

// abortResumeTick is one sweep over every claude session.
//
// 母集団のゲートは ListMetas だけ（rateLimitTick と同じ）: 会話に紐付いているか・指示
// 台帳に行があるかは無関係で、Console から直接起動した単独セッションもまったく同じに
// 扱う。それがこの機能の主目的だから。
func abortResumeTick(now time.Time) {
	for _, m := range session.ListMetas() {
		if normalizeKind(m.Kind) != session.KindClaude {
			continue // 判別材料（isApiErrorMessage）が claude 固有（docs/47 §5）
		}
		st, has := abortResumeStates.Read(m.Name)
		a, ok := claudeAbortInfo(session.UUID(m.Dir, m.Name))
		if !ok || !a.Retryable {
			// 末尾が中断ではない＝再開できた・利用者が自分で進めた・正常に終わった。
			// blocked な中断（上限・残高・プロンプト超過）もここで閉じる — そちらは
			// 従来どおり即座に報告へ流す（抑止しない）。
			if has {
				abortResumeStates.Remove(m.Name)
			}
			continue
		}
		if !sessionAlive(m) {
			continue // 死んだセッションは record_exit.go の領分（中断ではなく異常終了）
		}
		if !abortAutoResumeEnabled() {
			continue // OFF: エピソードを開かない＝抑止もしない（従来の報告経路のまま）
		}
		abortResumeAttempt(m, st, a, now)
	}
}

// abortResumeAttempt advances one open episode: 開始 → バックオフ待ち → 注入 → 打ち切り。
func abortResumeAttempt(m session.Meta, st abortResumeState, a claude.Abort, now time.Time) {
	if st.At == "" {
		st.At = abortEpisodeStart(a, now)
		st.Msg = a.Msg
		// 開いた時点で必ず書く。以降の分岐は途中で return するので、ここで永続化しないと
		// エピソードが毎 tick 生まれ直し、報告の抑止（abortResumeHolds）もファイルではなく
		// 「中断が新しいうち」の短い窓にしか乗らない。
		_ = abortResumeStates.Write(m.Name, st)
		log.Printf("abort-resume: %s のターンが中断で終わっている（%s）", m.Name, a.Msg)
	}
	if st.GaveUp != "" {
		return // 既に手を引いた episode — 報告経路が引き取っている
	}
	if abortEpisodeStale(st, now) {
		st.GaveUp = abortGaveUpStale
		log.Printf("abort-resume: %s の自動再開を打ち切る（%s）", m.Name, st.GaveUp)
		_ = abortResumeStates.Write(m.Name, st)
		return
	}
	if st.Attempts >= maxAutoResumeAttempts {
		// 再送しても中断が続く＝一時的な不調ではない。ここから先はアシスタント／利用者の
		// 領分なので、報告が「上限に達した」文面（reportKeyTurnAbortedCapped）で出るよう
		// カウンタを合わせてから抑止を外す。
		st.GaveUp = abortGaveUpCapped
		setAutoResumeAttempts(m.Name, st.Attempts)
		log.Printf("abort-resume: %s の自動再開を打ち切る（%d 回連続で中断）", m.Name, st.Attempts)
		_ = abortResumeStates.Write(m.Name, st)
		return
	}
	if !abortResumeDue(st, now) {
		return // バックオフ中
	}
	if !abortResumeReady(m.Name) {
		// 質問／プラン／許可の待ち、モーダル、走行中 — 今は送れない。届かない試行として
		// 数え、続くようなら打ち切る（人が操作している最中かもしれない）。
		st.DeliverTries++
		st.LastTry = now.Format(time.RFC3339)
		if st.DeliverTries >= abortResumeMaxDeliverTries {
			st.GaveUp = abortGaveUpUndeliverable
			log.Printf("abort-resume: %s へ再開プロンプトを届けられない — 打ち切る", m.Name)
		}
		_ = abortResumeStates.Write(m.Name, st)
		return
	}
	prompt := abortResumePrompt()
	// 送る前に記録する: 途中で落ちても回数が巻き戻らないようにして、撃ち続けない
	// （rateLimitRecover と同じ理由）。
	st.Attempts++
	st.LastTry = now.Format(time.RFC3339)
	_ = abortResumeStates.Write(m.Name, st)
	if err := abortResumeInject(m.Name, prompt); err != nil {
		st.Attempts--
		st.DeliverTries++
		if st.DeliverTries >= abortResumeMaxDeliverTries {
			st.GaveUp = abortGaveUpUndeliverable
		}
		_ = abortResumeStates.Write(m.Name, st)
		log.Printf("abort-resume: %s へ再開プロンプトを送れなかった: %v", m.Name, err)
		return
	}
	recordInjection(m.Name, prompt, turnSourceAutoResume)
	log.Printf("abort-resume: %s を自動再開した（%d/%d 回目）", m.Name, st.Attempts, maxAutoResumeAttempts)
}

// abortResumeReady reports whether a free-text prompt may be typed into the session right
// now. injectSessionPrompt 自身も待ち状態を弾くが、ここで先に見るのは「送れない理由」を
// エピソードに数えるため（弾かれ続けるなら打ち切って人へ渡す）。
//
// ペインの busy を弾くのが肝: 中断レコードは転写の末尾に残り続けるので、利用者が既に
// 手で再開していても末尾は中断のままに見える。走っているターンへ「続けて」を撃つと
// 割り込みの指示になってしまう。
func abortResumeReady(name string) bool {
	if promptBlocker(name) != "" {
		return false
	}
	pr := abortResumeReadingPane(name)
	return pr.OK && pr.Idle && !pr.Busy && !pr.RateLimitMenu
}

// abortResumeDue applies the backoff: 1回目は中断から abortResumeFirstDelay、2回目以降は
// 直近の試行から abortResumeBackoff。
func abortResumeDue(st abortResumeState, now time.Time) bool {
	if st.LastTry != "" {
		t, err := time.Parse(time.RFC3339, st.LastTry)
		return err != nil || !now.Before(t.Add(abortResumeBackoff))
	}
	t, err := time.Parse(time.RFC3339, st.At)
	return err != nil || !now.Before(t.Add(abortResumeFirstDelay))
}

// abortEpisodeStart is the episode's t0: 中断レコードの時刻（無ければ検知時刻）。
// レコードの時刻を使うのは、Agent の再起動をまたいでも「いつ止まったか」が動かないから。
func abortEpisodeStart(a claude.Abort, now time.Time) string {
	if !a.At.IsZero() {
		return a.At.Format(time.RFC3339)
	}
	return now.Format(time.RFC3339)
}

func abortEpisodeStale(st abortResumeState, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, st.At)
	return err != nil || now.After(t.Add(abortResumeEpisodeTTL))
}

// abortResumeHolds reports whether the automatic resume has taken responsibility for this
// cut-off — i.e. the reconciler must NOT deliver an aborted-turn report yet (docs/47 §4-6).
//
// エピソードのファイルが**まだ無い**場合も抑止する（sweep は 30 秒ごとなので、中断の
// 直後は必ずこの状態になる）。ただし中断が新しいうちだけ: 時刻が読めないか古い中断で
// エピソードも無いなら、watcher が動いていない（機能 OFF・Agent が旧版・ループが死んだ）
// ということなので、抑止せず従来どおり報告する。抑止が片道切符にならないための保険。
func abortResumeHolds(name string, a claude.Abort, now time.Time) bool {
	if !a.Retryable || !abortAutoResumeEnabled() {
		return false
	}
	st, ok := abortResumeStates.Read(name)
	if !ok {
		return !a.At.IsZero() && now.Sub(a.At) < abortResumeFirstDelay+abortResumeWatchInterval
	}
	if st.GaveUp != "" {
		return false
	}
	return !abortEpisodeStale(st, now)
}

// abortResumePrompt is the nudge itself — 一語で足りる。中断は数十秒前の出来事で、
// 会話も作業状態もそのまま残っているので、「続けて」以上の説明は文脈の重複でしかない
// （利用上限の再開文が長いのは、数時間後・ワークスペース再起動後に届くからで、事情が
// 違う）。括弧の一語だけ足しているのは2つの理由:
//
//	① 利用者が自分で打つ「続けて」と区別できる。注入元の照合キーは本文の完全一致
//	   （recordInjection）なので、素の一語だと利用者の入力が自動再開に見えてしまう。
//	② 転写にもミラーにも「これは自己修復であって新しい指示ではない」と残る。
//
// 言語は表示言語に合わせる（rateLimitResumePrompt と同じ理由 — セッションごとの言語を
// 持たない以上、その利用者が読み書きしている言語が最良の推定）。
func abortResumePrompt() string { return abortResumePromptFor(uiLocale()) }

func abortResumePromptFor(locale string) string {
	if locale == "en" {
		return "continue (auto-resume)"
	}
	return "続けて（自動再開）"
}
