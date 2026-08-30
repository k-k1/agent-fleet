package main

// Injected prompts that did not come from the user's own keyboard land in the CLI
// transcript as ordinary user turns — indistinguishable from what the user typed in
// the composer or the raw terminal. Two sources inject this way:
//   - the fleet operator (docs/log/30 ②): an af_write assistant's create_session
//     initial_prompt / send_to_session, tagged Source="operator".
//   - the chat bridge (docs/log/37 P2a): a Discord (later Slack) thread reply routed back
//     into the session, tagged Source="discord" / "slack".
// To let the mirror tell them apart from self-typed input, we remember each injected
// prompt's text AND its origin per session and, when serving the transcript, tag the
// matching user turn with that origin (transcript.Turn.Source → a Console badge).

import (
	"regexp"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// Turn origins recorded here (transcript.Turn.Source values). "" = the user's own input.
const (
	turnSourceOperator = "operator" // fleet operator injected (docs/log/30 ②)
	turnSourceDiscord  = "discord"  // chat bridge — Discord thread reply (docs/log/37 P2a)
	turnSourceSlack    = "slack"    // chat bridge — Slack (P2 follow-up)
	turnSourceSchedule = "schedule" // scheduled execution fired it (docs/log/38 — CP scheduler create/reuse send)
	// turnSourceScheduleManual is a schedule fired by run-now（手動発火）— same pipeline as
	// "schedule" but user-initiated, so the mirror can badge 定期/手動 distinctly (docs/log/38).
	turnSourceScheduleManual = "schedule-manual"
	// turnSourceAutoResume is the Agent's own nudge after a retryable cut-off (docs/log/47
	// §4-6). バッジを分けるのは、これが「誰かの指示」ではなく**中断からの自己修復**
	// だから — 利用者がミラーを見たとき、自分もオペレーターも送っていない「続けて」が
	// 誰の仕業か分からないのが一番困る。
	turnSourceAutoResume = "auto-resume"
	// turnSourcePeer is another SESSION's message (docs/log/58 / ADR 0041). バッジを分ける
	// 理由は auto-resume と同じで、しかもこちらの方が切実 — 利用者もオペレーターも
	// 送っていない指示がミラーに現れたとき、それが「隣の worktree のセッションから来た」
	// と分からないと、誰の仕業か辿る手段が無い。**このバッジが peer 着信の唯一の可視化**
	// なので、付け忘れると人間から見えない経路になる。
	turnSourcePeer = "peer"
)

// injectionSource maps a caller-supplied source onto the recordable vocabulary. The
// /input and create_session wire fields are reachable by any client, so an unknown
// value degrades to "operator" (the pre-existing meaning of a report_to-carrying
// injection) instead of minting arbitrary badge strings in the Console.
func injectionSource(s string) string {
	switch s {
	case turnSourceSchedule, turnSourceScheduleManual:
		return s
	default:
		return turnSourceOperator
	}
}

// scheduleInjectionSource returns the schedule origin a caller declared, or "" when the
// source is not a schedule at all.
//
// なぜ injectionSource() と別に要るか: あれは未知/空を operator へ倒すので、由来の記録を
// report_to から切り離す判定には使えない（素の Console 入力まで operator バッジになる）。
// そして切り離しは必要だった — 由来の記録が report_to != "" の中にあった間、**完了報告
// OFF のスケジュール投入はバッジが丸ごと落ちていた**。report_to は report=true のときしか
// 付かず（CP scheduleReportTo）、Console の完了報告チェックは既定 OFF、利用上限の自動再開に
// 至っては常に report=false。source は最初から届いていたのに、報告の有無という無関係な条件で
// 捨てていたことになる。
func scheduleInjectionSource(s string) string {
	switch s {
	case turnSourceSchedule, turnSourceScheduleManual:
		return s
	default:
		return ""
	}
}

// badgeOriginOf は「この投入をミラーでどのバッジにするか」を1か所で決める。"" は
// 利用者が自分で打った入力＝バッジ無し。
//
// 投入経路（TUI / managed）ごとに switch を書き分けていたのを1つにまとめてあるのは、
// **記録を配達より前へ動かす**ため（下の警告を参照）。片方の経路だけ動かすと、同じ
// バッジが kind によって出たり出なかったりする。
func badgeOriginOf(peerFrom, reportTo, source string) string {
	switch {
	case peerFrom != "":
		return turnSourcePeer
	case reportTo != "":
		return injectionSource(source)
	default:
		// 報告 OFF の定時実行だけがここで拾われる（それ以外は ""＝素の入力）。
		return scheduleInjectionSource(source)
	}
}

// maxOperatorInjections caps the per-session record. Membership is all the tagging needs,
// so we keep the newest N distinct texts (a long-lived session steered many times stays
// bounded).
const maxOperatorInjections = 100

// injectedPrompt is one remembered injection: the prompt text (the tagging key) plus the
// origin to stamp onto the matching user turn.
type injectedPrompt struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

// injectionStore holds, per session, the distinct prompt texts injected into it and their
// origins. A SEPARATE file from Meta (same reasoning as the report link): several code
// paths touch session state and Meta is one clobber-prone blob. (Format note: this replaced
// an earlier []string operator-only store — an old file simply fails to decode into the new
// shape and is treated as empty, which only drops badges on in-flight sessions at upgrade.)
var injectionStore = fstore.JSON[[]injectedPrompt](paths.AgentConfigDir, "session-injections", ".json")

// injectionMu serializes recordInjection's read-modify-write — concurrent injections
// (operator + scheduler) would otherwise drop each other's record (= a missing badge).
var injectionMu sync.Mutex

// recordInjection remembers a prompt injected into a session, tagged with its origin, so the
// transcript can attribute the matching user turn. Deduped by text (a resend needn't
// duplicate; a re-injection from a different source updates the origin) and capped (newest
// kept).
//
// **必ず投入より前に呼ぶこと。** タグ付けは要求のたびにこのファイルを読み直すので、記録が
// 転写の user 行より後になると、その隙間に来たポーリングは同じターンを**由来なし**で返す。
// ミラーは既に持っているターンを取り直さない（増分は since 以降しか送らない）ので、一度
// 素で配ったターンは画面を開き直すまで永久にバッジ無しのままになる。peer 送信は配達確認
// （＝user 行が転写に現れるまで待つ）を必ず通るので、後ろに置くとこの隙間が構造的に開く
// — 実測 524ms（2026-08-27 sopx6gc 宛の着信）。
func recordInjection(name, text, source string) {
	text = strings.TrimSpace(text)
	if !session.ValidName(name) || text == "" {
		return
	}
	injectionMu.Lock()
	defer injectionMu.Unlock()
	list, _ := injectionStore.Read(name)
	for i, e := range list {
		if e.Text == text {
			list[i].Source = source // latest origin wins for the same text
			_ = injectionStore.Write(name, list)
			return
		}
	}
	list = append(list, injectedPrompt{Text: text, Source: source})
	if len(list) > maxOperatorInjections {
		list = list[len(list)-maxOperatorInjections:]
	}
	_ = injectionStore.Write(name, list)
}

// recordOperatorInjection is the operator-origin convenience wrapper (docs/log/30 ②), kept so
// the several operator-injection call sites read unchanged.
func recordOperatorInjection(name, text string) { recordInjection(name, text, turnSourceOperator) }

// operatorInjections returns the distinct prompt texts recorded for a session (nil when
// none). Used by tests.
func operatorInjections(name string) []string {
	list, _ := injectionStore.Read(name)
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.Text)
	}
	return out
}

// tagInjectedTurns stamps each user turn whose text matches a recorded injection with that
// injection's origin (transcript.Turn.Source). A cheap no-op when nothing was injected (the
// common case: one file read, then return).
//
// Slash-command / skill injections need a second matching form: the injected text is the
// raw "/scout arg" the sender posted, but claude logs the turn as a
// `<command-name>/<command-message>` tag block (either tag first — 2.1.215 実測 skills are
// message-first), so an exact text compare never hits and the badge silently vanished for
// every injected slash command. commandSlashForm recovers "/name args" from the tag block
// so those turns tag too.
func tagInjectedTurns(name string, turns []transcript.Turn) {
	if len(turns) == 0 {
		return
	}
	list, ok := injectionStore.Read(name)
	if !ok || len(list) == 0 {
		return
	}
	bySource := make(map[string]string, len(list))
	for _, e := range list {
		bySource[e.Text] = e.Source
	}
	for i := range turns {
		if turns[i].Role != "user" {
			continue
		}
		text := strings.TrimSpace(turns[i].Text)
		src, hit := bySource[text]
		if !hit {
			if slash := commandSlashForm(text); slash != "" {
				src, hit = bySource[slash]
			}
		}
		if hit {
			turns[i].Source = src
		}
	}
}

var commandNameRe = regexp.MustCompile(`<command-name>([\s\S]*?)</command-name>`)
var commandArgsRe = regexp.MustCompile(`<command-args>([\s\S]*?)</command-args>`)

// commandSlashForm recovers the "/name args" a sender actually posted from claude's
// command-tag user turn. "" when the text is not a command block. The leading tag is
// required (not just a regex hit anywhere) so prose merely quoting the tags can't match.
func commandSlashForm(text string) string {
	if !strings.HasPrefix(text, "<command-name>") && !strings.HasPrefix(text, "<command-message>") {
		return ""
	}
	m := commandNameRe.FindStringSubmatch(text)
	if m == nil || strings.TrimSpace(m[1]) == "" {
		return ""
	}
	out := strings.TrimSpace(m[1])
	if a := commandArgsRe.FindStringSubmatch(text); a != nil && strings.TrimSpace(a[1]) != "" {
		out += " " + strings.TrimSpace(a[1])
	}
	return out
}
