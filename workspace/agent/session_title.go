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

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
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

// titleSuggestPersona keeps the headless call laser-focused: no preamble, no
// quoting, a single short Japanese line. Third-person topic label, not a sentence:
// the model is prone to echoing the assistant's own reasoning ("〜が良さそう") if not
// pinned to "what is this session ABOUT" as a noun phrase.
const titleSuggestPersona = "あなたはセッションの会話ログを読み、セッション一覧に表示する短い件名を付ける専用ツールです。" +
	"会話で扱っている作業やトピックを、第三者が見て『何についてのセッションか』が分かる名詞句で表してください。" +
	"会話が複数のテーマにまたがる場合は直近で扱っている内容を優先します。" +
	"日本語18文字以内、1行のみ。文章にしない・語尾（〜する/〜したい/〜です/〜が良い 等）を付けない・" +
	"説明・前置き・引用符・記号・箇条書きは一切付けない。"

// titleModel: a cheap/fast model is enough for a short label; override deployment-
// wide with AF_TITLE_MODEL.
func titleModel() string { return envOr("AF_TITLE_MODEL", "haiku") }

func runTitleSuggestLLM(ctx context.Context, turns []transcript.Turn) (string, error) {
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
func titleSuggestPrompt(turns []transcript.Turn) string {
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
	b.WriteString("会話ログから件名を1つ出力してください。\n")
	b.WriteString("良い例: セッションタイトルの自動提案 / ログイン画面のバグ修正 / 請求APIのリファクタ\n")
	b.WriteString("悪い例（文章・語尾つき・視点が話者）: 短く確認するのが良さそう / メニュー変更を行いたい\n")
	b.WriteString("会話の途中でテーマが変わっている場合は、直近で話している内容を優先してください。\n\n")
	b.WriteString("--- 会話ログ ---\n")
	writeConversationWindow(&b, real)
	return b.String()
}

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
	if agentOf(m.Kind).caps().usesLabel {
		m.Label = sessionLabelFor(m.Dir, m.Title)
	}
	session.WriteMeta(m)
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, tmuxx.HasSession(session.TmuxName(name))))
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
		httpx.WriteErr(w, http.StatusBadRequest, "no_content", "not enough conversation yet to suggest a title")
	case errors.Is(err, errTitleGenBusy):
		httpx.WriteErr(w, http.StatusConflict, "busy", "a title generation is already in progress")
	default:
		httpx.WriteErr(w, http.StatusInternalServerError, "generation_failed", "title generation failed")
	}
}

// handleRegenerateSuggestedTitle lets the user explicitly ask for a fresh title
// suggestion at any time (a button in the chat header) — including for a session
// that already has a title, since the point is offering a better one as the
// conversation moves on; the banner's 採用/× still requires an explicit click
// before anything is overwritten. Bypasses the automatic trigger's turn-count/idle
// gating and any prior dismissal — the automatic path still only offers once, but
// the user can always ask again manually. Runs synchronously so the response
// carries the new suggestion directly; this is a rare, user-initiated action
// (unlike the poll-driven automatic path), so blocking the request on the LLM call
// is fine. Persists into SuggestedTitle (so it also surfaces as the header banner)
// — for a preview that doesn't touch session.Meta, see handleSuggestTitle.
func handleRegenerateSuggestedTitle(w http.ResponseWriter, r *http.Request) {
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

	// Re-read: the LLM call can take tens of seconds, during which the session
	// could have been archived/removed.
	m, found = session.ReadMeta(name)
	if !found {
		httpx.WriteErr(w, http.StatusConflict, "conflict", "session changed while generating")
		return
	}
	m.SuggestedTitle = title
	m.SuggestedTitleDismissed = false // an explicit re-ask overrides any earlier dismissal
	session.WriteMeta(m)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"suggestedTitle": title})
}

// handleSuggestTitle previews a title suggestion WITHOUT touching session.Meta —
// used by the manual rename dialog's "AIに提案してもらう" button, which just fills
// the text field for the user to edit/accept themselves. Unlike
// handleRegenerateSuggestedTitle, this works even when the session already has a
// title (renaming is exactly the case where one already exists) and never drives
// the accept/dismiss banner flow.
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
	if agentOf(m.Kind).caps().usesLabel {
		m.Label = sessionLabelFor(m.Dir, m.Title)
	}
	session.WriteMeta(m)
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, tmuxx.HasSession(session.TmuxName(name))))
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
	args := []string{"-p", "--output-format", "json", "--dangerously-skip-permissions",
		"--append-system-prompt", branchSuggestPersona, "--model", titleModel()}
	args = append(args, chatToolLimits()...)
	cmd := chatClaudeCmd(ctx, args...)
	defer func() { _, _ = ensureChatClaudeConfig() }()
	cmd.Stdin = strings.NewReader(branchSuggestPrompt(turns))

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("branch suggestion failed: %s", cliErr(err))
	}
	var r claudeResult
	if json.Unmarshal(out, &r) != nil || r.IsError {
		return "", fmt.Errorf("branch suggestion: bad/error response")
	}
	return cleanBranchName(r.Result), nil
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
		httpx.WriteErr(w, http.StatusBadRequest, "feature_disabled", "AI 提案が無効です（表示設定のタイトル自動提案をオンに）")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	turns := sessionTitleTurns(m)
	if len(turns) == 0 {
		httpx.WriteErr(w, http.StatusBadRequest, "no_content", "会話がまだ足りません（数往復してから試してください）")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), titleSuggestTimeout)
	defer cancel()
	branch, err := runBranchSuggestLLM(ctx, turns)
	if err != nil {
		// Surface the underlying reason (auth/CLI/timeout) instead of a generic string.
		httpx.WriteErr(w, http.StatusBadGateway, "generation_failed", "AI 提案に失敗しました: "+err.Error())
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
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, tmuxx.HasSession(session.TmuxName(name))))
}

// sessionTitleTurns fetches the full turn list for a session regardless of kind,
// for the manual regenerate action (which needs the whole conversation, not a
// poll window).
func sessionTitleTurns(m session.Meta) []transcript.Turn {
	if m.Kind == session.KindClaude {
		sid := session.UUID(m.Dir, m.Name)
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
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, tmuxx.HasSession(session.TmuxName(name))))
}
