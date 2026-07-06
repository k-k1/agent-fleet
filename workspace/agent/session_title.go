// Auto session-title suggestion: once a session has had a couple of exchanges and
// still has no user-set title, a headless `claude -p` call proposes a short (~40
// char) Japanese title. The Console shows it as a dismissible banner; accepting or
// dismissing latches SuggestedTitleDismissed so a session is offered one at most
// once (v1 has no re-suggestion loop). Gated globally by the DisplayTab セッション
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
// checked the cheap sessionMeta fields (Title == "", SuggestedTitle == "",
// !SuggestedTitleDismissed) and autoTitleSuggestEnabled() before computing turns.
func maybeSuggestTitle(name string, turns []chatTurn, idleFor time.Duration) {
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
func generateSessionTitle(name string, turns []chatTurn) {
	ok := false
	defer func() { titleGenDone(name, ok) }()

	ctx, cancel := context.WithTimeout(context.Background(), titleSuggestTimeout)
	defer cancel()
	title, err := runTitleSuggestLLM(ctx, turns)
	if err != nil || title == "" {
		return // ok stays false -> backoff before the next attempt
	}
	ok = true

	m, found := readSessionMeta(name)
	if !found || m.Title != "" || m.SuggestedTitle != "" || m.SuggestedTitleDismissed {
		return // gone, or resolved by the user while we were generating
	}
	m.SuggestedTitle = title
	writeSessionMeta(m)
}

// titleSuggestPersona keeps the headless call laser-focused: no preamble, no
// quoting, a single short Japanese line. Third-person topic label, not a sentence:
// the model is prone to echoing the assistant's own reasoning ("〜が良さそう") if not
// pinned to "what is this session ABOUT" as a noun phrase.
const titleSuggestPersona = "あなたはセッションの会話ログを読み、セッション一覧に表示する短い件名を付ける専用ツールです。" +
	"会話で扱っている作業やトピックを、第三者が見て『何についてのセッションか』が分かる名詞句で表してください。" +
	"日本語18文字以内、1行のみ。文章にしない・語尾（〜する/〜したい/〜です/〜が良い 等）を付けない・" +
	"説明・前置き・引用符・記号・箇条書きは一切付けない。"

// titleModel: a cheap/fast model is enough for a short label; override deployment-
// wide with AF_TITLE_MODEL.
func titleModel() string { return envOr("AF_TITLE_MODEL", "haiku") }

func runTitleSuggestLLM(ctx context.Context, turns []chatTurn) (string, error) {
	args := []string{"-p", "--output-format", "json", "--dangerously-skip-permissions",
		"--append-system-prompt", titleSuggestPersona, "--model", titleModel()}
	args = append(args, chatToolLimits()...) // no subagents/file/bash — pure text in/out
	cmd := chatClaudeCmd(ctx, args...)
	defer func() { _, _ = ensureChatClaudeConfig() }() // reconcile any credential refresh (chat.go pattern)
	cmd.Stdin = strings.NewReader(titleSuggestPrompt(turns))

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("title generation failed: %s", cliErr(err))
	}
	var r claudeResult
	if json.Unmarshal(out, &r) != nil || r.IsError {
		return "", fmt.Errorf("title generation: bad/error response")
	}
	return cleanSuggestedTitle(r.Result), nil
}

// titleSuggestPrompt feeds only the first couple of real exchanges' text (skipping
// sidechain/compaction/tool-only turns) — enough context for a topic without
// bloating a one-shot call with a growing transcript.
func titleSuggestPrompt(turns []chatTurn) string {
	var b strings.Builder
	// Few-shot anchors the output as a noun-phrase topic label rather than a sentence
	// or the assistant's own reasoning.
	b.WriteString("会話ログから件名を1つ出力してください。\n")
	b.WriteString("良い例: セッションタイトルの自動提案 / ログイン画面のバグ修正 / 請求APIのリファクタ\n")
	b.WriteString("悪い例（文章・語尾つき・視点が話者）: 短く確認するのが良さそう / メニュー変更を行いたい\n\n")
	b.WriteString("--- 会話ログ ---\n")
	n := 0
	for _, t := range turns {
		if t.Sidechain || t.Compact || t.Text == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", t.Role, t.Text)
		if n++; n >= 4 {
			break
		}
	}
	return b.String()
}

// cleanSuggestedTitle trims the model's reply to one line, strips wrapping quotes,
// and reuses cleanTitle (same control-char/length gate a user-typed title gets),
// then caps at the ~40-char prompt target.
func cleanSuggestedTitle(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.Trim(s, "\"'「」『』")
	title, ok := cleanTitle(s)
	if !ok || title == "" {
		return ""
	}
	// Hard cap well under the session-list label width so the applied title stays
	// readable (not truncated) in the left pane; the prompt targets ~18.
	if r := []rune(title); len(r) > 24 {
		title = string(r[:24])
	}
	return title
}

// handleAcceptSuggestedTitle promotes the pending suggestion to the session's real
// title. Mirrors handleRecreateSession's read-meta/mutate/write-meta/return-wire
// pattern. Updates Label too (for a later recreate/relaunch's claude --name) but
// does NOT rename the already-running claude process — its --name was fixed at
// launch.
func handleAcceptSuggestedTitle(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := readSessionMeta(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if m.SuggestedTitle == "" {
		writeErr(w, http.StatusBadRequest, "no_suggestion", "no suggested title to accept")
		return
	}
	m.Title = m.SuggestedTitle
	m.SuggestedTitle = ""
	m.SuggestedTitleDismissed = true // resolved — v1 never re-suggests for this session
	if agentOf(m.Kind).caps().usesLabel {
		m.Label = sessionLabelFor(m.Dir, m.Title)
	}
	writeSessionMeta(m)
	writeJSON(w, http.StatusOK, wireSession(m, tmuxHasSession(tmuxName(name))))
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
func generateTitleNow(ctx context.Context, name string, turns []chatTurn) (string, error) {
	if len(turns) == 0 {
		return "", errNoTitleContent
	}
	if !titleGenClaim(name) {
		return "", errTitleGenBusy
	}
	succeeded := false
	defer func() { titleGenDone(name, succeeded) }()

	title, err := runTitleSuggestLLM(ctx, turns)
	if err != nil || title == "" {
		return "", fmt.Errorf("title generation failed: %w", err)
	}
	succeeded = true
	return title, nil
}

func writeTitleGenErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNoTitleContent):
		writeErr(w, http.StatusBadRequest, "no_content", "not enough conversation yet to suggest a title")
	case errors.Is(err, errTitleGenBusy):
		writeErr(w, http.StatusConflict, "busy", "a title generation is already in progress")
	default:
		writeErr(w, http.StatusInternalServerError, "generation_failed", "title generation failed")
	}
}

// handleRegenerateSuggestedTitle lets the user explicitly ask for a fresh title
// suggestion at any time (a button in the chat header), bypassing the automatic
// trigger's turn-count/idle gating and any prior dismissal — the automatic path
// still only offers once, but the user can always ask again manually. Runs
// synchronously so the response carries the new suggestion directly; this is a
// rare, user-initiated action (unlike the poll-driven automatic path), so blocking
// the request on the LLM call is fine. Persists into SuggestedTitle (so it also
// surfaces as the header banner) — for a preview that doesn't touch sessionMeta,
// see handleSuggestTitle.
func handleRegenerateSuggestedTitle(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	if !autoTitleSuggestEnabled() {
		writeErr(w, http.StatusBadRequest, "feature_disabled", "auto title suggestion is turned off")
		return
	}
	m, found := readSessionMeta(name)
	if !found {
		writeErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if m.Title != "" {
		writeErr(w, http.StatusBadRequest, "already_titled", "session already has a title")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), titleSuggestTimeout)
	defer cancel()
	title, err := generateTitleNow(ctx, name, sessionTitleTurns(m))
	if err != nil {
		writeTitleGenErr(w, err)
		return
	}

	// Re-read: the LLM call can take tens of seconds, during which the user may
	// have set a title themselves.
	m, found = readSessionMeta(name)
	if !found || m.Title != "" {
		writeErr(w, http.StatusConflict, "conflict", "session changed while generating")
		return
	}
	m.SuggestedTitle = title
	m.SuggestedTitleDismissed = false // an explicit re-ask overrides any earlier dismissal
	writeSessionMeta(m)
	writeJSON(w, http.StatusOK, map[string]any{"suggestedTitle": title})
}

// handleSuggestTitle previews a title suggestion WITHOUT touching sessionMeta —
// used by the manual rename dialog's "AIに提案してもらう" button, which just fills
// the text field for the user to edit/accept themselves. Unlike
// handleRegenerateSuggestedTitle, this works even when the session already has a
// title (renaming is exactly the case where one already exists) and never drives
// the accept/dismiss banner flow.
func handleSuggestTitle(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	if !autoTitleSuggestEnabled() {
		writeErr(w, http.StatusBadRequest, "feature_disabled", "auto title suggestion is turned off")
		return
	}
	m, found := readSessionMeta(name)
	if !found {
		writeErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), titleSuggestTimeout)
	defer cancel()
	title, err := generateTitleNow(ctx, name, sessionTitleTurns(m))
	if err != nil {
		writeTitleGenErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestedTitle": title})
}

// handleSetTitle applies a user-typed title directly (the rename dialog's 保存
// button) — the only path that lets the Console set an arbitrary title on an
// EXISTING session (creation already accepts one; accept/regenerate only ever
// write an LLM-produced string). An empty title reverts to the auto label and
// re-opens the session to future auto-suggestions.
func handleSetTitle(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req) != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "invalid request body")
		return
	}
	title, ok := cleanTitle(req.Title)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_title", "title too long or contains control characters")
		return
	}
	m, found := readSessionMeta(name)
	if !found {
		writeErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	m.Title = title
	m.SuggestedTitle = ""
	m.SuggestedTitleDismissed = title != "" // clearing the title re-opens auto-suggestion
	if agentOf(m.Kind).caps().usesLabel {
		m.Label = sessionLabelFor(m.Dir, m.Title)
	}
	writeSessionMeta(m)
	writeJSON(w, http.StatusOK, wireSession(m, tmuxHasSession(tmuxName(name))))
}

// sessionTitleTurns fetches the full turn list for a session regardless of kind,
// for the manual regenerate action (which needs the whole conversation, not a
// poll window).
func sessionTitleTurns(m sessionMeta) []chatTurn {
	if m.Kind == kindClaude {
		sid := sessionUUID(m.Dir, m.Name)
		lines, _, _ := transcriptRead(sid)
		return collectTurns(lines, 0, len(lines))
	}
	td, ok := agentOf(m.Kind).transcript(m)
	if !ok {
		return nil
	}
	return td.turns
}

// handleDismissSuggestedTitle discards the pending suggestion without adopting it,
// and latches SuggestedTitleDismissed so it is never offered again for this session.
func handleDismissSuggestedTitle(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !nameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := readSessionMeta(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	m.SuggestedTitle = ""
	m.SuggestedTitleDismissed = true
	writeSessionMeta(m)
	writeJSON(w, http.StatusOK, wireSession(m, tmuxHasSession(tmuxName(name))))
}
