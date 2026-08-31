// Auto session-title suggestion: once a session has had a couple of exchanges and
// still has no user-set title, a headless `claude -p` call proposes a short title, in
// the Console's display language at that moment (titleLang). The Console shows it as a
// dismissible banner; accepting or
// dismissing latches SuggestedTitleDismissed so a session is offered one at most
// once (v1 has no re-suggestion loop). Gated globally by the AgentsTab セッション
// "タイトル自動提案" toggle (autoTitleSuggestEnabled, ui_prefs.go).

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

const (
	// minTitleSuggestTurns ≈ "a couple of exchanges" (2 user + 2 assistant turns).
	minTitleSuggestTurns = 4
	// titleIdleThreshold: wait this long after the transcript's last write before
	// generating, so we capture the conversation's settled shape rather than a
	// mid-turn/mid-tool-call snapshot (claude appends the assistant line, then
	// tool_use/tool_result lines, in quick succession within one logical turn).
	titleIdleThreshold = 45 * time.Second
	// titleGenBackoff bounds how often a PERSISTENTLY failing generation (bad model
	// name, CLI hiccup, ...) is retried — without it, a poll every 1.2-3s would retry
	// on literally every tick forever.
	titleGenBackoff     = 5 * time.Minute
	titleSuggestTimeout = 60 * time.Second
)

// titleGenState tracks, per session name, whether a generation is currently running
// and (on failure) when the next attempt may start. In-memory only — reset on Agent
// restart, which just means one extra attempt after a restart; harmless.
var (
	titleGenMu    sync.Mutex
	titleGenState = map[string]titleGenEntry{}
)

type titleGenEntry struct {
	inFlight    bool
	nextAttempt time.Time
}

// titleGenReady is the cheap (no parse) pre-check called before the expensive full-
// transcript parse, so a session whose generation is running or in backoff never
// pays for a re-parse it can't use.
func titleGenReady(name string) bool {
	titleGenMu.Lock()
	defer titleGenMu.Unlock()
	e := titleGenState[name]
	return !e.inFlight && time.Now().After(e.nextAttempt)
}

// titleGenClaim atomically re-checks + claims, closing the race between two
// concurrent polls (the Console's 1.2s tick can overlap a slow LLM call) both
// passing titleGenReady and both spawning a generation.
func titleGenClaim(name string) bool {
	titleGenMu.Lock()
	defer titleGenMu.Unlock()
	e := titleGenState[name]
	if e.inFlight || !time.Now().After(e.nextAttempt) {
		return false
	}
	e.inFlight = true
	titleGenState[name] = e
	return true
}

func titleGenDone(name string, ok bool) {
	titleGenMu.Lock()
	defer titleGenMu.Unlock()
	e := titleGenState[name]
	e.inFlight = false
	if !ok {
		e.nextAttempt = time.Now().Add(titleGenBackoff)
	}
	titleGenState[name] = e
}

// maybeSuggestTitle is the shared trigger for both /messages paths (claude's line-
// cursor path and the generic codex/opencode path both already parse turns every
// poll — this reuses that instead of adding a server-side ticker; no periodic
// goroutine exists anywhere else in this package). Callers must have already
// checked the cheap session.Meta fields (Title == "", SuggestedTitle == "",
// !SuggestedTitleDismissed) and autoTitleSuggestEnabled() before computing turns.
func maybeSuggestTitle(name string, turns []transcript.Turn, idleFor time.Duration) {
	if len(turns) < minTitleSuggestTurns || idleFor < titleIdleThreshold {
		return
	}
	if !titleGenClaim(name) {
		return
	}
	go generateSessionTitle(name, turns)
}

// generateSessionTitle runs off the request goroutine so it never blocks a poll. It
// re-reads the meta itself (not the caller's snapshot) because the LLM call can take
// tens of seconds, during which the user may have set a title / the suggestion may
// already have been resolved.
func generateSessionTitle(name string, turns []transcript.Turn) {
	ok := false
	defer func() { titleGenDone(name, ok) }()

	ctx, cancel := context.WithTimeout(context.Background(), titleSuggestTimeout)
	defer cancel()
	ctx = withUsageTag(ctx, usageTag{Feature: usageFeatureTitleSession, Trigger: usageTriggerAuto, Ref: name})
	title, err := runTitleSuggestLLM(ctx, turns)
	if err != nil || title == "" {
		return // ok stays false -> backoff before the next attempt
	}
	ok = true

	m, found := session.ReadMeta(name)
	if !found || m.Title != "" || m.SuggestedTitle != "" || m.SuggestedTitleDismissed {
		return // gone, or resolved by the user while we were generating
	}
	m.SuggestedTitle = title
	session.WriteMeta(m)
}

// titleLang picks the language the suggested title is WRITTEN in: the Console display
// language (ui-prefs "locale", 設定 > 表示言語), read live at generation time.
//
// A title is stored text read by this one user in their own session list, so per ADR
// 0033 the deciding axis is "who reads it" — hence the UI language, not the
// conversation's language (an English speaker debugging a Japanese codebase still wants
// an English list). Deliberately NOT retroactive: switching the language later leaves
// existing titles alone (they are the user's data, and re-suggesting is a user action —
// the rename dialog's 「AIに提案してもらう」 regenerates in the new language on demand).
func titleLang() string { return uiLocale() }

// titleSuggestPersona keeps the headless call laser-focused: no preamble, no quoting, a
// single short line. Third-person topic label, not a sentence: the model is prone to
// echoing the assistant's own reasoning ("〜が良さそう") if not pinned to "what is this
// session ABOUT" as a noun phrase. Each language's persona is written IN that language
// so it also steers the output language (same reasoning as langRuleJA/EN in chat.go — a
// Japanese instruction attached to an English request gives mixed signals).
func titleSuggestPersona(lang string) string {
	if lang == "en" {
		return titleSuggestPersonaEN
	}
	return titleSuggestPersonaJA
}

const titleSuggestPersonaJA = "あなたはセッションの会話ログを読み、セッション一覧に表示する短い件名を付ける専用ツールです。" +
	"会話で扱っている作業やトピックを、第三者が見て『何についてのセッションか』が分かる名詞句で表してください。" +
	"会話が複数のテーマにまたがる場合は直近で扱っている内容を優先します。" +
	"日本語18文字以内、1行のみ。文章にしない・語尾（〜する/〜したい/〜です/〜が良い 等）を付けない・" +
	"説明・前置き・引用符・記号・箇条書きは一切付けない。" +
	"『件名:』『セッション件名:』のようなラベルや『会話の内容から件名をお作りします：』のような" +
	"前置きの行も出力せず、件名そのものだけを出力すること。"

// titleSuggestPersonaEN: the conversation itself is frequently Japanese even for an
// English-speaking user (Japanese codebase, Japanese teammates), so the persona must ask
// for a TRANSLATED English label rather than assume the log's language — the same trap
// branchSuggestPersona documents.
const titleSuggestPersonaEN = "You read a session's conversation log and write the short subject line shown in a session list. " +
	"Describe the work or topic as a noun phrase, so a third party can tell what the session is about. " +
	"If the conversation spans several themes, prefer the most recent one. " +
	"English, at most 6 words, one line only — even when the conversation is in Japanese, translate the topic into English. " +
	"Not a sentence: no verb endings, no explanation, no preamble, no quotes, no bullets. " +
	"Never output a label line such as 'Title:' or a lead-in such as 'Here is the title:' — output only the subject line itself. " +
	"You never reply to the conversation and never continue it; you only label it."

// titleModel: a cheap/fast model is enough for a short label; override deployment-
// wide with AF_TITLE_MODEL.
func titleModel() string { return envOr("AF_TITLE_MODEL", "haiku") }

func runTitleSuggestLLM(ctx context.Context, turns []transcript.Turn) (string, error) {
	// Backend-agnostic one-shot (oneShotHeadless): runs on the first available of
	// claude → codex → opencode, so claude-less workspaces get suggestions too.
	lang := titleLang()
	reply, err := oneShotHeadless(ctx, titleSuggestPersona(lang), titleSuggestPrompt(turns, lang), titleModel())
	if err != nil {
		return "", fmt.Errorf("title generation failed: %w", err)
	}
	return cleanSuggestedTitle(reply), nil
}

const (
	// titleHeadTurns/titleTailTurns: feed the opening (original intent) plus a larger
	// recent window (current topic) rather than only the first few turns — a long
	// session that drifted off its opening subject was otherwise stuck suggesting that
	// stale opening topic on every regenerate.
	titleHeadTurns = 2
	titleTailTurns = 6
	// titlePerTurnRunes caps each turn's text so one giant paste can't blow up the
	// one-shot prompt (and its cost).
	titlePerTurnRunes = 400
)

// titleSuggestPrompt feeds the opening and the most recent real exchanges (skipping
// sidechain/compaction/tool-only turns), weighting the recent topic — so the title
// tracks where the conversation is now, not just where it started.
func titleSuggestPrompt(turns []transcript.Turn, lang string) string {
	real := make([]transcript.Turn, 0, len(turns))
	for _, t := range turns {
		if t.Sidechain || t.Compact || t.Text == "" {
			continue
		}
		real = append(real, t)
	}

	var b strings.Builder
	// Few-shot anchors the output as a noun-phrase topic label rather than a sentence
	// or the assistant's own reasoning.
	b.WriteString(titleSuggestInstructions(lang))
	writeConversationWindow(&b, real)
	b.WriteString(titleSuggestFooter(lang))
	return b.String()
}

// titleSuggestFooter repeats the instruction AFTER the log. Measured need (AF_TITLE_LIVE,
// haiku): with the instruction only at the top, an English instruction wrapped around a
// Japanese log made the model read the log as a live chat and CONTINUE it — it answered
// the user at length and appended the title only at the very end, so cleanSuggestedTitle
// took the first prose line. The trailing reminder (recency, plus an explicit "do not
// continue") pins it back to labelling. Applied to both languages: the same failure is
// latent in Japanese, it just did not reproduce there.
func titleSuggestFooter(lang string) string {
	if lang == "en" {
		return "--- end of conversation log ---\n" +
			"Now output ONLY the subject line for the log above, on one line. " +
			"Do not reply to the conversation and do not continue it.\n"
	}
	return "--- 会話ログここまで ---\n" +
		"上の会話に付ける件名だけを1行で出力してください。会話への返信や続きは書かないこと。\n"
}

// titleSuggestInstructions is the instruction block shared by the session and chat
// title prompts, including the conversation-log header. The 悪い例 / Bad list carries
// the two shapes actually observed being adopted as titles — a lead-in sentence and a
// "件名:" label — because the persona's 前置き禁止 alone did not stop them.
func titleSuggestInstructions(lang string) string {
	if lang == "en" {
		return titleSuggestInstructionsEN
	}
	return titleSuggestInstructionsJA
}

const titleSuggestInstructionsJA = "会話ログから件名を1つ出力してください。\n" +
	"良い例: セッションタイトルの自動提案 / ログイン画面のバグ修正 / 請求APIのリファクタ\n" +
	"悪い例（文章・語尾つき・視点が話者）: 短く確認するのが良さそう / メニュー変更を行いたい\n" +
	"悪い例（前置き・ラベル）: 会話の内容から、件名をお作りします： / セッション件名: / 件名は以下の通りです\n" +
	"件名の文字列だけを1行で出力し、前置き行・ラベル・見出しは付けないでください。\n" +
	"会話の途中でテーマが変わっている場合は、直近で話している内容を優先してください。\n\n" +
	"--- 会話ログ ---\n"

const titleSuggestInstructionsEN = "Output ONE subject line for the conversation log below.\n" +
	"Good: Session title auto-suggestion / Login screen bugfix / Billing API refactor\n" +
	"Bad (a sentence, or the speaker's point of view): Probably worth checking briefly / I want to change the menu\n" +
	"Bad (lead-in or label): Here is the title: / Title: / Sure, here's a name\n" +
	"Output only the subject line itself, on one line — no lead-in line, no label, no heading.\n" +
	"The log is often in Japanese; translate the topic into English rather than copying it.\n" +
	"If the topic changed mid-conversation, prefer what is being discussed most recently.\n\n" +
	"--- conversation log ---\n"

// writeConversationWindow appends the opening + most recent real turns (head/tail
// windowing, per-turn length cap), shared by the title and branch-name prompts.
func writeConversationWindow(b *strings.Builder, real []transcript.Turn) {
	writeTurn := func(t transcript.Turn) {
		text := t.Text
		if r := []rune(text); len(r) > titlePerTurnRunes {
			text = string(r[:titlePerTurnRunes]) + "…"
		}
		fmt.Fprintf(b, "%s: %s\n", t.Role, text)
	}
	if len(real) <= titleHeadTurns+titleTailTurns {
		for _, t := range real {
			writeTurn(t)
		}
		return
	}
	for _, t := range real[:titleHeadTurns] {
		writeTurn(t)
	}
	b.WriteString("…（中略）…\n")
	for _, t := range real[len(real)-titleTailTurns:] {
		writeTurn(t)
	}
}

// branchSuggestPrompt builds an ENGLISH prompt for a git branch name. Crucially it does
// NOT reuse titleSuggestPrompt (which is Japanese and asks for a Japanese 件名 — that
// steered the model to reply in Japanese, which cleanBranchName then stripped to ""):
// it instructs an English kebab-case name even when the conversation is Japanese, with
// English few-shot anchors.
func branchSuggestPrompt(turns []transcript.Turn) string {
	real := make([]transcript.Turn, 0, len(turns))
	for _, t := range turns {
		if t.Sidechain || t.Compact || t.Text == "" {
			continue
		}
		real = append(real, t)
	}
	var b strings.Builder
	b.WriteString("Read the conversation log and output ONE git branch name for the task.\n")
	b.WriteString("Rules: English only, lowercase kebab-case (words joined by hyphens), ")
	b.WriteString("ASCII letters/digits/hyphens only, max 40 chars, no prefixes like 'feature/', no quotes.\n")
	b.WriteString("The conversation is often in Japanese — TRANSLATE the topic into a concise English name. ")
	b.WriteString("Never output Japanese or non-ASCII characters.\n")
	b.WriteString("Good: fix-login-redirect / refactor-billing-api / session-branch-rename\n")
	b.WriteString("If the conversation drifted, prefer the most recent topic. Output ONLY the name.\n\n")
	b.WriteString("--- conversation log ---\n")
	writeConversationWindow(&b, real)
	return b.String()
}

// cleanSuggestedTitle reduces the model's reply to one usable title.
//
// It scans lines rather than taking the first one verbatim: the model does not
// reliably obey "1行のみ" and often emits a lead-in ("会話の内容から、件名をお作りしま
// す：") or a label ("セッション件名:") first and the real title on a following line —
// taking line 1 adopted the preamble AS the title (observed on session sicoxqh). Each
// line is stripped of list/emphasis markers and any "件名:"-style label; pure lead-in
// lines are dropped, and the first survivor wins. Returns "" when nothing usable
// remains, which the callers already treat as "no suggestion" (auto path backs off,
// manual path reports a failure) — better than latching a meaningless title.
func cleanSuggestedTitle(s string) string {
	for i, line := range strings.Split(s, "\n") {
		if i >= titleReplyScanLines {
			break
		}
		cand := titleCandidateLine(line)
		if cand == "" {
			continue
		}
		title, ok := cleanTitle(cand)
		if !ok || title == "" {
			continue
		}
		// TrimSpace: a cut that lands mid-space would leave a trailing blank.
		return strings.TrimSpace(truncateToWidth(title, titleWidthCap))
	}
	return ""
}

const (
	// titleReplyScanLines bounds how far into a chatty reply we look for the title;
	// past a few lines it is commentary, not a forgotten title.
	titleReplyScanLines = 8
	// titleMarkerChars are decoration the model wraps a title in (markdown emphasis,
	// headings, code spans). Titles never legitimately contain them, so drop them
	// outright rather than trying to match pairs.
	titleMarkerChars = "*#`"
	// titleQuoteChars wrap an otherwise fine title.
	titleQuoteChars = "\"'「」『』【】《》 　\t"
	// titleWidthCap keeps the applied title well under the session-list label width so
	// it isn't ellipsised in the left pane. Measured in DISPLAY COLUMNS, not runes: the
	// prompt targets ~18 Japanese chars (= 36 columns), and a rune cap of 24 tuned for
	// that chopped English titles mid-word at 24 columns ("Session title auto-sugge").
	titleWidthCap = 48
)

// truncateToWidth cuts s to at most w display columns, counting East-Asian wide runes
// (kana/kanji/fullwidth/emoji) as 2 and everything else as 1.
func truncateToWidth(s string, w int) string {
	used := 0
	for i, r := range s {
		cw := 1
		if isWideRune(r) {
			cw = 2
		}
		if used+cw > w {
			return s[:i]
		}
		used += cw
	}
	return s
}

// isWideRune covers the ranges that actually show up in titles (CJK, kana, Hangul,
// fullwidth forms, emoji) — enough for a display cap, and not worth a new dependency.
func isWideRune(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E, // CJK radicals, kana punctuation
		r >= 0x3041 && r <= 0x33FF, // kana, CJK compatibility
		r >= 0x3400 && r <= 0x4DBF, // CJK ext A
		r >= 0x4E00 && r <= 0x9FFF, // CJK unified
		r >= 0xA000 && r <= 0xA4CF, // Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE6F, // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1F9FF, // emoji
		r >= 0x20000 && r <= 0x3FFFD: // CJK ext B+
		return true
	}
	return false
}

// titleLabelWords mark the text before a colon as a LABEL for the title rather than
// part of it ("件名: 〜", "セッション件名：〜", "Title: 〜"). Matched on the pre-colon
// segment only, so a title that merely contains one of these words
// （例: セッションタイトルの自動提案）survives untouched.
var titleLabelWords = []string{"件名", "タイトル", "title", "subject", "見出し"}

// titleAnnounceTails end a sentence ABOUT producing a title ("〜件名をお作りします。").
// Only rejected in combination with a label word, so a plain noun-phrase title is never
// caught by them.
var titleAnnounceTails = []string{"ます", "ます。", "です", "です。", "ください", "ください。", "した", "した。"}

// titleLeadInPrefixes catch an ENGLISH announcement with no colon and no label word
// ("Here is a concise name for this session"). The persona asks for Japanese, but a
// wholly-English conversation still pulls English framing out of the model, and the
// Japanese tails above cannot see it. Matched case-insensitively at line start only, so
// an English title is only dropped when it literally opens with a chat filler.
var titleLeadInPrefixes = []string{
	"here is", "here's", "here are", "sure", "certainly", "of course",
	"okay", "ok,", "i suggest", "i'd suggest", "i would suggest", "based on",
}

// titleCandidateLine turns one reply line into a title candidate, or "" if the line is
// decoration/preamble rather than a title.
func titleCandidateLine(line string) string {
	s := strings.TrimSpace(line)
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(titleMarkerChars, r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimLeft(s, "-–—>・•●▶ 　\t")
	s = trimListNumber(s)
	s = strings.Trim(s, titleQuoteChars)
	if s == "" {
		return ""
	}

	// "…件名: <title>" -> "<title>"; "…件名:" (nothing after) -> preamble, drop the line.
	if head, rest, ok := cutColon(s); ok && containsAnyFold(head, titleLabelWords) {
		s = strings.Trim(strings.TrimSpace(rest), titleQuoteChars)
		if s == "" {
			return ""
		}
	}
	// Language-independent backstop: a line that still ENDS in a colon is announcing
	// something that follows, never a title itself. This is what catches lead-ins whose
	// wording we don't enumerate — "以下の通りです：", "Here is a suitable name:".
	if strings.HasSuffix(s, ":") || strings.HasSuffix(s, "：") {
		return ""
	}
	// A lead-in with no colon at all ("以下が件名です", "Here is a concise title").
	if containsAnyFold(s, titleLabelWords) && hasAnySuffix(s, titleAnnounceTails) {
		return ""
	}
	if hasAnyPrefixFold(s, titleLeadInPrefixes) {
		return ""
	}
	return strings.TrimSpace(s)
}

// trimListNumber drops a leading "1." / "2)" / "3、" list marker (with its separator),
// leaving a title that legitimately starts with a number ("2段クォータの実装") alone.
func trimListNumber(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i > 2 {
		return s
	}
	rest := s[i:]
	for _, sep := range []string{".", ")", "）", "、", ":", "："} {
		if strings.HasPrefix(rest, sep) {
			return strings.TrimLeft(rest[len(sep):], " 　\t")
		}
	}
	return s
}

// cutColon splits on the first ASCII or full-width colon.
func cutColon(s string) (head, rest string, ok bool) {
	i := strings.IndexAny(s, ":：")
	if i < 0 {
		return "", "", false
	}
	sep := 1
	if s[i] != ':' {
		sep = len("：")
	}
	return s[:i], s[i+sep:], true
}

func containsAnyFold(s string, words []string) bool {
	low := strings.ToLower(s)
	for _, w := range words {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

func hasAnyPrefixFold(s string, prefixes []string) bool {
	low := strings.ToLower(s)
	for _, p := range prefixes {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return false
}

func hasAnySuffix(s string, tails []string) bool {
	for _, t := range tails {
		if strings.HasSuffix(s, t) {
			return true
		}
	}
	return false
}

// handleAcceptSuggestedTitle promotes the pending suggestion to the session's real
// title. Mirrors handleRecreateSession's read-meta/mutate/write-meta/return-wire
// pattern. Updates Label too (for a later recreate/relaunch's claude --name) but
// does NOT rename the already-running claude process — its --name was fixed at
// launch.
func handleAcceptSuggestedTitle(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if m.SuggestedTitle == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "no_suggestion", "no suggested title to accept")
		return
	}
	m.Title = m.SuggestedTitle
	m.SuggestedTitle = ""
	m.SuggestedTitleDismissed = true // resolved — v1 never re-suggests for this session
	if agentOf(m.Kind).Caps().UsesLabel {
		m.Label = sessionLabelFor(m.Dir, m.Title, m.Name)
	}
	session.WriteMeta(m)
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, sessionAlive(m)))
}

// errNoTitleContent/errTitleGenBusy are sentinels generateTitleNow returns so
// callers can translate them to the right HTTP status via writeTitleGenErr; any
// other error means the LLM call itself failed.
var (
	errNoTitleContent = errors.New("not enough conversation yet")
	errTitleGenBusy   = errors.New("a title generation is already in progress")
)

// generateTitleNow runs the headless LLM synchronously under the shared in-flight/
// backoff guard (titleGenClaim/titleGenDone) used by the automatic trigger too, so
// a manual request and a concurrent automatic one can't double-fire for the same
// session.
func generateTitleNow(ctx context.Context, name string, turns []transcript.Turn) (string, error) {
	if len(turns) == 0 {
		return "", errNoTitleContent
	}
	if !titleGenClaim(name) {
		return "", errTitleGenBusy
	}
	succeeded := false
	defer func() { titleGenDone(name, succeeded) }()

	ctx = withUsageTag(ctx, usageTag{Feature: usageFeatureTitleSession, Trigger: usageTriggerManual, Ref: name})
	title, err := runTitleSuggestLLM(ctx, turns)
	if err != nil {
		return "", fmt.Errorf("title generation failed: %w", err)
	}
	if title == "" {
		// The call succeeded but the reply was all preamble/label (cleanSuggestedTitle
		// dropped every line) — a plain failure, not a nil-wrapping %!w(<nil>).
		return "", errors.New("title generation produced no usable title")
	}
	succeeded = true
	return title, nil
}

func writeTitleGenErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNoTitleContent):
		httpx.WriteErr(w, http.StatusBadRequest, "no_content", "not enough conversation yet to suggest a title")
	case errors.Is(err, errTitleGenBusy):
		httpx.WriteErr(w, http.StatusConflict, "busy", "a title generation is already in progress")
	default:
		httpx.WriteErr(w, http.StatusInternalServerError, "generation_failed", "title generation failed")
	}
}

// handleSuggestTitle previews a title suggestion WITHOUT touching session.Meta —
// used by the manual rename dialog's "AIに提案してもらう" button, which just fills
// the text field for the user to edit/accept themselves. Works even when the
// session already has a title (renaming is exactly the case where one already
// exists) and never drives the accept/dismiss banner flow — the banner is offered
// only by the automatic trigger (maybeSuggestTitle).
func handleSuggestTitle(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	if !autoTitleSuggestEnabled() {
		httpx.WriteErr(w, http.StatusBadRequest, "feature_disabled", "auto title suggestion is turned off")
		return
	}
	m, found := session.ReadMeta(name)
	if !found {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), titleSuggestTimeout)
	defer cancel()
	title, err := generateTitleNow(ctx, name, sessionTitleTurns(m))
	if err != nil {
		writeTitleGenErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"suggestedTitle": title})
}

// handleSetTitle applies a user-typed title directly (the rename dialog's 保存
// button) — the only path that lets the Console set an arbitrary title on an
// EXISTING session (creation already accepts one; accept/regenerate only ever
// write an LLM-produced string). An empty title reverts to the auto label and
// re-opens the session to future auto-suggestions.
func handleSetTitle(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req) != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_json", "invalid request body")
		return
	}
	title, ok := cleanTitle(req.Title)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_title", "title too long or contains control characters")
		return
	}
	m, found := session.ReadMeta(name)
	if !found {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	m.Title = title
	m.SuggestedTitle = ""
	m.SuggestedTitleDismissed = title != "" // clearing the title re-opens auto-suggestion
	if agentOf(m.Kind).Caps().UsesLabel {
		m.Label = sessionLabelFor(m.Dir, m.Title, m.Name)
	}
	session.WriteMeta(m)
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, sessionAlive(m)))
}

// branchSuggestPersona pins the headless call to emit a git branch name, NOT a
// Japanese title: lowercase English kebab-case, git-safe, short. The conversation may
// be in Japanese but the branch name must be ASCII (folder/ref charset), so we ask for
// a translation-to-name, not a transcription.
const branchSuggestPersona = "You name git branches. Read the conversation log and output ONE short branch name " +
	"describing the task. Rules: English, lowercase kebab-case (words joined by hyphens), " +
	"ASCII letters/digits/hyphens only, max 40 chars, no leading verb like 'add'/'fix' unless natural, " +
	"no prefixes like 'feature/', no quotes, no explanation. Output only the name."

// runBranchSuggestLLM asks the title model for a git-safe branch name from the
// conversation, then hard-sanitizes the reply so a chatty model can't produce an
// invalid ref/folder segment.
func runBranchSuggestLLM(ctx context.Context, turns []transcript.Turn) (string, error) {
	reply, err := oneShotHeadless(ctx, branchSuggestPersona, branchSuggestPrompt(turns), titleModel())
	if err != nil {
		return "", fmt.Errorf("branch suggestion failed: %w", err)
	}
	return cleanBranchName(reply), nil
}

// cleanBranchName reduces an LLM reply to a git-safe kebab-case name: first line,
// lowercased, non-[a-z0-9] runs collapsed to a single hyphen, trimmed, capped at 40.
// "" when nothing usable remains.
func cleanBranchName(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.ToLower(s)
	var b strings.Builder
	lastHyphen := false
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
			lastHyphen = false
		} else if !lastHyphen && b.Len() > 0 {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	name := strings.Trim(b.String(), "-")
	if r := []rune(name); len(r) > 40 {
		name = strings.Trim(string(r[:40]), "-")
	}
	return name
}

// handleSessionSuggestBranch proposes a branch name from THIS session's conversation —
// the LLM half of deferred naming (start on temp/<slug>, ask the AI for a real name
// once the task has a shape). Session-scoped (not repo-scoped) so the source is
// unambiguous even when several sessions share one worktree. Preview only: it returns
// the suggestion for the rename modal to fill; it never renames on its own.
func handleSessionSuggestBranch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	if !autoTitleSuggestEnabled() {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeTitleFeatureDisabled, "AI suggestions are disabled (enable title auto-suggestion in agent settings)")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	turns := sessionTitleTurns(m)
	if len(turns) == 0 {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeTitleNoContent, "not enough conversation yet (try after a few exchanges)")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), titleSuggestTimeout)
	defer cancel()
	ctx = withUsageTag(ctx, usageTag{Feature: usageFeatureBranchSuggest, Trigger: usageTriggerManual, Ref: name})
	branch, err := runBranchSuggestLLM(ctx, turns)
	if err != nil {
		// Surface the underlying reason (auth/CLI/timeout) instead of a generic string.
		// 例外的にカタログ化しない: detail（err）を保持したいため developer message を
		// そのまま表示する（"generation_failed" キーを i18n に足さない）。
		httpx.WriteErr(w, http.StatusBadGateway, "generation_failed", "AI title suggestion failed: "+err.Error())
		return
	}
	if branch == "" {
		httpx.WriteErr(w, http.StatusUnprocessableEntity, "empty_result",
			"AI が有効なブランチ名を返しませんでした。手入力してください。")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"branch": branch})
}

// handleSessionRenameBranch renames the branch of the session's working copy (its
// worktree) via git branch -m. Session-scoped so it pairs with the session-based AI
// suggestion; the rename targets the one shared branch and every session in that dir
// has its recorded start branch updated (so an intentional rename isn't read as drift).
// Refuses a name that collides with an existing local or past-remote branch.
func handleSessionRenameBranch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	dir := m.Dir
	if !isGitRepo(dir) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_dir", "session working copy is not a git repo")
		return
	}
	var req renameBranchReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	newName := strings.TrimSpace(req.Name)
	if newName == "" || strings.HasPrefix(newName, "-") {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_ref", "branch name is required and must not start with '-'")
		return
	}
	if cur, _ := gitStatus(dir); newName != cur.Branch {
		if local, remote := branchNameStatus(dir, newName); local || remote {
			where := "ローカル"
			if !local {
				where = "リモート"
			}
			httpx.WriteErr(w, http.StatusConflict, "branch_exists",
				fmt.Sprintf("%sに同名ブランチ %q が既にあります。別の名前にしてください。", where, newName))
			return
		}
	}
	if out, err := gitx.Combined(dir, "branch", "-m", newName); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "rename_failed", out)
		return
	}
	session.UpdateStartBranch(dir, newName)
	m, _ = session.ReadMeta(name)
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, sessionAlive(m)))
}

// sessionTitleTurns fetches the full turn list for a session regardless of kind,
// for the manual suggest action (which needs the whole conversation, not a
// poll window).
func sessionTitleTurns(m session.Meta) []transcript.Turn {
	if m.Kind == session.KindClaude {
		sid := session.UUID(m.Dir, m.Name)
		lines, _, _ := claude.TranscriptRead(sid)
		return claude.CollectTurns(lines, 0, len(lines))
	}
	td, ok := agentOf(m.Kind).Transcript(m)
	if !ok {
		return nil
	}
	return td.Turns
}

// handleDismissSuggestedTitle discards the pending suggestion without adopting it,
// and latches SuggestedTitleDismissed so it is never offered again for this session.
func handleDismissSuggestedTitle(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	m.SuggestedTitle = ""
	m.SuggestedTitleDismissed = true
	session.WriteMeta(m)
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, sessionAlive(m)))
}
