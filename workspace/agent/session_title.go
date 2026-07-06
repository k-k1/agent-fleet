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
	"fmt"
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
// quoting, a single short Japanese line.
const titleSuggestPersona = "あなたは会話の内容から、チャット一覧に表示する短いタイトルを作る専用のアシスタントです。" +
	"説明・前置き・引用符・箇条書きは一切付けず、日本語で40文字以内の短いタイトルを1行だけ出力してください。"

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
	if r := []rune(title); len(r) > 40 {
		title = string(r[:40])
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
