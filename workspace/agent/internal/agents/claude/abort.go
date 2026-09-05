package claude

// Aborted-turn detection (docs/log/47).
//
// claude does not fire the Stop hook when a turn dies on an API error. The working→idle
// transition is therefore recorded nowhere and only the pane returns to the ready prompt.
// The self-heal path (driveState / WireLive) then saw "not idle, yet back at the prompt"
// and silently removed the state file, so neither the response notification nor the
// docs/log/30 completion report was produced and the report arm stayed unconsumed
// (measured: session ssiw5kb, 2026-07-26).
//
// The failure does survive in the transcript: one synthetic record with type=assistant and
// isApiErrorMessage=true is written, and no real record follows it in that turn. Only that
// tail shape decides whether the turn ended in an abort, and the cause is split into "an
// abort a re-send fixes" and "re-sending fails the same way until the cause is gone". Only
// the former is eligible for auto-resume.

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// abortRecord is the subset of a transcript line the abort detector needs.
type abortRecord struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsAPIError  bool   `json:"isApiErrorMessage"`
	Status      int    `json:"apiErrorStatus"`
	Timestamp   string `json:"timestamp"`
	// Error is claude's own MACHINE-READABLE cause ("server_error" / "rate_limit" /
	// "invalid_request" — measured 2026-08-05). Unlike the English prose it is not reworded
	// from release to release, so it is the last clue left when a shape the text does not
	// cover arrives (docs/log/47 §5).
	Error string `json:"error"`
}

// Abort is a detected turn cut-off, with everything a caller needs to report it.
type Abort struct {
	Msg       string    // the error text (rides the report / chat bridge as the reason)
	Retryable bool      // a re-send fixes it (auto-resume eligible) vs pointless until fixed
	At        time.Time // when the abort was recorded; zero = the record carried no timestamp
}

// retryableOverrides are texts where claude ITSELF says the error is not the user's
// limit. They are checked before blockedMarkers because the sentence contains the words
// a naive substring match reads as a usage limit — "Server is temporarily limiting
// requests (NOT YOUR USAGE LIMIT) · Rate limited". Getting this backwards would make the
// most common retryable error (7 of 16 in the corpus) never auto-resume.
var retryableOverrides = []string{
	"temporarily limiting requests",
	"not your usage limit",
}

// blockedMarkers are error texts whose cause does NOT clear on its own: re-sending the
// same turn reproduces the same error, so the operator must fix the cause first (balance or
// limit, over-long prompt, authentication). From the measured corpus (docs/log/47 §2).
var blockedMarkers = []string{
	"reached your",       // "You've reached your <model> limit. Run /usage-credits …"
	"usage limit",        // another wording of the limit ("not a limit" already excluded by overrides)
	"session limit",      // "You've hit your session limit · resets 7:50pm (Asia/Tokyo)"
	"weekly limit",       // "You've hit your weekly limit · resets 9am (Asia/Tokyo)" (measured corpus)
	"spend limit",        // "You've hit your org's monthly spend limit · run /usage-credits …"
	"prompt is too long", // conversation too long — re-sending without /compact is pointless
	"credit balance",
	"invalid api key",
	"authentication",
	"unauthorized",
	// Measured expiry (2026-08-06 / apiErrorStatus 401 / error:"authentication_failed"):
	// "Please run /login · API Error: 401 OAuth access token has expired. Re-authenticate
	// to continue." — that does NOT hit "authentication" above (the text carries
	// "Re-authenticate"). Being a 401 it fell to the blocked default and the verdict was
	// right by accident, so name the stems explicitly.
	"re-authenticate",
	"run /login",
}

// limitMarkers are the blockedMarkers that specifically mean A USAGE LIMIT — a quota that
// lifts on its own schedule — as opposed to the other blocked causes (over-long prompt,
// balance, authentication) which never lift by waiting. A limit episode
// (rate_limit_resume.go) is entered from this subset only, not from blockedMarkers as a
// whole: notifying "usage limit reached" for a prompt-length or authentication error leaves
// the user waiting for a reset that never comes.
var limitMarkers = []string{
	// Per-model limit. Shows no menu and folds the turn with a one-line error (measured
	// 2026-08-05 s6no6jv / claude 2.1.x) — "You've reached your Fable 5 limit. Run
	// /usage-credits …"
	"reached your",
	"usage limit",
	// Account window. Comes with the /rate-limit-options menu (measured 2026-07-31
	// s5jjqv4) — "You've hit your session limit · resets 7:50pm (Asia/Tokyo)"
	"session limit",
	// Weekly window (measured corpus 2026-08-20) — "You've hit your weekly limit · resets
	// 9am (Asia/Tokyo)". It matches none of the three above, so the weekly one alone opened
	// no episode: no notification, no resume booking, no chip (it fell to the blocked
	// default, so only the classification was right).
	"weekly limit",
}

// LimitKind splits a turn that ended on a limit into the two kinds whose next move is
// opposite. Both arrive as 429 / `error:"rate_limit"`, so code alone cannot tell them apart
// (docs/log/47 §4-10).
type LimitKind string

const (
	// LimitWindow is a window of time (5-hour / weekly / per-model). Waiting clears it, so
	// it is eligible for auto-resume.
	LimitWindow LimitKind = "window"
	// LimitSpend is a spend or balance cap. Waiting never clears it — raising the cap or
	// adding credit is a billing decision — so no auto-resume is armed and it must not call
	// itself "waiting for the limit to lift".
	LimitSpend LimitKind = "spend"
)

// spendMarkers are the usage-limit texts that mean a cap on money. Measured (2026-08-20,
// from a user-reported screenshot):
//
//	You've hit your org's monthly spend limit · run /usage-credits to raise it,
//	or visit claude.ai/admin-settings/usage
//
// Do not key on "/usage-credits": the per-model window limit ("You've reached your Fable 5
// limit. Run /usage-credits to continue or switch models…") points at the same command, so
// it would match both and turn a window limit into "a bigger cap is needed". Hold only the
// words that name the money itself (spend limit / credit balance).
var spendMarkers = []string{
	"spend limit",
	"credit balance", // "Your credit balance is too low …" (measured, via blockedMarkers)
}

// retryableMarkers are error texts that clear by themselves: the turn was cut off by a
// transport / capacity hiccup and simply re-running it continues the work.
var retryableMarkers = []string{
	"connection closed",
	"connection error",
	"temporarily limiting requests", // a 429 that is not a usage limit (the text says so)
	"overloaded",
	"timed out",
	"timeout",
	// "server error" is the broad form that also covers "internal server error". Measured
	// sp2qemx (2026-07-30): "API Error: Server error mid-response. The response above may
	// be incomplete." offers no other clue — the apiErrorStatus field is missing entirely,
	// so the 5xx fallback below misses it too and it fell to blocked (no auto-resume).
	"server error",
	"service unavailable",
	"bad gateway",
	// Stream watchdog (claude 2.1.x once its internal retries are used up). Measured
	// 2026-08-05: "API Error: Stream idle timeout - no chunks received". "timed out" /
	// "timeout" above already match it, but name the known shape explicitly, keyed on the
	// stem (stream idle) so it still lands if the wording drifts toward "no chunks
	// received".
	"stream idle",
}

// retryableErrorKinds maps claude's own `error` field onto the retryable side. It catches the
// shapes the message text does not cover and is consulted AFTER the text (the text can
// express a negation such as "not a usage limit"; this field only classifies).
//
// "rate_limit" is deliberately absent: 429 is the axis where a usage limit (blocked) and a
// temporary rate limit (retryable) coexist, and only the text tells them apart (docs/log/47
// §2). Only values seen in practice are listed — an unknown value falls to the blocked
// default (not auto-resuming is the safe side when the verdict is unclear), so do not widen
// the hole with guessed entries.
var retryableErrorKinds = map[string]bool{
	"server_error": true, // measured: 529 Overloaded / Connection closed / Server error mid-response
}

// blockedErrorKinds are the `error` values whose cause never clears by re-sending (over-long
// prompt, invalid request, authentication). blocked is the default so the verdict is
// unchanged, but state it as an intended one rather than one that is only right by accident.
var blockedErrorKinds = map[string]bool{
	"invalid_request":       true, // measured: Prompt is too long
	"authentication_failed": true, // measured: 401 OAuth access token has expired (same until re-login)
}

// classifyAbort splits an API error message into fixed by a re-send (true) and pointless
// until the cause is fixed (false). An unclear verdict falls to false — not auto-resuming is
// the safe side. blocked is checked first because a usage limit also arrives as a 429
// ("You've reached your … limit"), so the status code alone cannot tell it from a temporary
// rate limit.
//
// The order is text → `error` → status. The text leads because only it can express a negation
// such as "not a usage limit" (retryableOverrides). `error` comes before status because a
// synthetic record sometimes lacks apiErrorStatus entirely while `error` survives.
func classifyAbort(msg string, status int, errKind string) bool {
	low := strings.ToLower(msg)
	for _, m := range retryableOverrides {
		if strings.Contains(low, m) {
			return true
		}
	}
	for _, m := range blockedMarkers {
		if strings.Contains(low, m) {
			return false
		}
	}
	for _, m := range retryableMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	switch k := strings.ToLower(strings.TrimSpace(errKind)); {
	case retryableErrorKinds[k]:
		return true
	case blockedErrorKinds[k]:
		return false
	}
	return status >= 500 && status <= 599
}

// AbortedTurn reports whether the session's transcript ENDS on an API error — i.e. the
// last turn was cut off and no Stop hook ever fired. msg is the error text (it rides the
// report / chat bridge as the reason), retryable says whether a plain re-run should work.
//
// Only real records (type=user / assistant) decide the tail. Bookkeeping records —
// custom-title, mode, last-prompt, file-history-*, system(turn_duration) — are written after
// an abort as well and their kinds come and go between releases, so they are filtered by an
// allow-list ("ignore anything that is not user/assistant") rather than a deny-list, which
// survives version drift. A subagent error (isSidechain) does not end the main turn and is
// ignored the same way.
//
// Once the user resumes by hand the tail becomes user/assistant again, so this function
// returns to false on its own: a session whose abort was reported once is never reported
// again after it resumes.
func AbortedTurn(sid string) (msg string, retryable, ok bool) {
	a, ok := AbortInfo(sid)
	return a.Msg, a.Retryable, ok
}

// AbortInfo is AbortedTurn plus WHEN the cut-off was recorded — what a level-driven
// reader needs (docs/log/51): the report reconciler compares that time against the
// instruction cursor to decide which instructions this terminal event covers.
//
// It reads only the tail of the live transcript (lastLineWhere), because unlike the
// heal path — which asks once, after the pane is seen at its prompt — the reconciler
// asks every tick for every armed session.
func AbortInfo(sid string) (Abort, bool) {
	for _, p := range jsonlByMtime(sid) {
		line, found := lastLineWhere(p, func(l []byte) bool { _, ok := terminalRecord(l); return ok })
		if !found {
			continue // nothing in this transcript can end a turn (a stub, say) — try the next
		}
		r, _ := terminalRecord(line)
		return abortFrom(line, r)
	}
	return Abort{}, false
}

// abortedTurnFrom is the pure form used by the corpus / table tests: same rule, applied
// to a whole set of lines instead of a file's tail.
func abortedTurnFrom(lines [][]byte) (msg string, retryable, ok bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		r, isTerminal := terminalRecord(lines[i])
		if !isTerminal {
			continue // bookkeeping record / subagent — cannot end a turn
		}
		a, ok := abortFrom(lines[i], r)
		return a.Msg, a.Retryable, ok
	}
	return "", false, false
}

// terminalRecord parses a line and reports whether it can END a turn: a real record
// (user/assistant) that is not a subagent's. An allow-list rather than a deny-list, because
// the set of bookkeeping record kinds grows and shrinks between releases (docs/log/47).
func terminalRecord(line []byte) (abortRecord, bool) {
	var r abortRecord
	if json.Unmarshal(line, &r) != nil {
		return r, false
	}
	if r.IsSidechain || (r.Type != "user" && r.Type != "assistant") {
		return r, false
	}
	return r, true
}

// abortFrom is the verdict for one terminal record.
func abortFrom(line []byte, r abortRecord) (Abort, bool) {
	if r.Type != "assistant" || !r.IsAPIError {
		return Abort{}, false // the latest real record is an ordinary turn — not an abort
	}
	a := Abort{Msg: strings.TrimSpace(AssistantText(line))}
	a.Retryable = classifyAbort(a.Msg, r.Status, r.Error)
	if at, err := time.Parse(time.RFC3339, r.Timestamp); err == nil {
		a.At = at
	}
	return a, true
}

// UsageLimitAbort is AbortInfo narrowed to a turn that ended on a usage limit. Only when
// ok=true may a limit episode (rate_limit_resume.go) be opened.
//
// Why the transcript needs this verdict too: a limit has more than one shape. On an account
// window claude puts up the /rate-limit-options menu and waits for a keystroke (readable from
// the pane), but a per-model limit shows NO menu: it writes a one-line error, folds the turn
// as complete and returns to the ordinary composer. The latter leaves no clue in the pane
// (the error line on screen is transcript text, so it stays there forever and cannot say when
// it happened — the same trap as isCodexUpdateMenu), so only the tail of the transcript can
// answer what the state is right now.
//
// A retryable abort (dropped connection, temporary rate limit) is not a limit and is dropped.
// retryableOverrides runs first inside classifyAbort, so a record that calls itself "(not
// your usage limit)" never reaches the "usage limit" marker here.
//
// kind splits on whether waiting clears it (docs/log/47 §4-10). Both arrive as the same 429 /
// `error:"rate_limit"`, yet a window (time) and a spend cap (money) call for opposite next
// moves: wait for the first; the second never clears, and needs a raised cap or added credit.
func UsageLimitAbort(sid string) (Abort, LimitKind, bool) {
	a, ok := AbortInfo(sid)
	if !ok || a.Retryable {
		return Abort{}, "", false
	}
	return limitKindOf(a.Msg, a)
}

// limitKindOf is the pure form (for the corpus tests): it decides the kind of limit from the
// message text.
//
// Spend is checked first. When a text reads as both ("…spend limit… run /usage-credits…"),
// falling to the side that clears by waiting is the costlier error: the user waits for a
// reset that never comes and auto-resume keeps hitting the same 429. The opposite error
// (calling a window limit "needs a bigger cap") only costs someone a look at something that
// would have fixed itself.
func limitKindOf(msg string, a Abort) (Abort, LimitKind, bool) {
	low := strings.ToLower(msg)
	for _, m := range spendMarkers {
		if strings.Contains(low, m) {
			return a, LimitSpend, true
		}
	}
	for _, m := range limitMarkers {
		if strings.Contains(low, m) {
			return a, LimitWindow, true
		}
	}
	return Abort{}, "", false
}

// HealIdle is what the pane-based self-heal does once it has decided the session is
// really back at its ready prompt. It replaces the bare status.Remove that used to sit
// at both heal sites: if the transcript says the turn was CUT OFF, the turn end is a
// real terminal event and has to go through the shared notifier (notification plus the
// docs/log/30 report) — dropping it on the floor is exactly the bug docs/log/47 fixes.
// Anything else (killed+resumed, rejected permission, abandoned question) stays a silent
// heal as before.
//
// MarkTurnEndErr persists idle rather than removing the marker, so the heal condition
// (state != "idle") no longer holds afterwards and a second poll cannot re-report the
// same abort. A duplicate from two concurrent polls is absorbed by handleChatReport's
// disarm.
func HealIdle(sid string) {
	if msg, retryable, ok := AbortedTurn(sid); ok {
		st := agents.TurnFailed // re-sending repeats the failure until the cause is fixed
		if retryable {
			st = agents.TurnAborted
		}
		agents.MarkTurnEndErr(sid, st, msg)
		return
	}
	status.Remove(sid)
}
