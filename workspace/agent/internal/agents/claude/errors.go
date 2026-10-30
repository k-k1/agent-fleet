package claude

// How a turn collapsed by an API error is presented.
//
// claude writes a failure into the transcript as a synthetic assistant record (type=assistant,
// model=`<synthetic>`, `isApiErrorMessage:true`). Its content is a plain text block, so the
// mirror used to draw it in the same bubble as an ordinary answer: a turn that died on expired
// credentials looked exactly like something the agent had said, and its body was the CLI-facing
// "Please run /login", which tells a Console user nothing about where to go.
//
// codex and opencode emit the same failure as a `kind="error"` part (their own errors.go), and
// the Console's ErrorBlock (.mirror-error) draws that as an always-expanded red block. claude
// is brought to the same vocabulary. This is a separate axis from docs/log/47's classification
// (does a resend fix it) and decides only what appears on screen and how.
//
// Measured record (expired credentials, transcript corpus):
//
//	{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":401,
//	 "error":"authentication_failed",
//	 "message":{"model":"<synthetic>","content":[{"type":"text",
//	   "text":"Please run /login · API Error: 401 OAuth access token has expired. Re-authenticate to continue."}]}}

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// causeAuth marks a failure that clears by authenticating again. Only when the Console sees
// this does it offer the re-authentication route (Settings > Agents). The value is a
// machine-readable signal, not display text (the wording and language belong to the Console's
// i18n).
//
// Only auth is raised today: the other causes (usage limit, prompt overflow, server side) have
// no corresponding action in the Console, so more marks would have no use. Add them here when
// they do.
const causeAuth = transcript.CauseAuth

// apiError is one synthetic API-error record, normalized to the same label+detail shape
// codex/opencode use so all three render identically downstream.
type apiError struct {
	msg    string // body (claude's English wording; assume it is rewritten between versions)
	kind   string // the `error` field: authentication_failed / rate_limit / server_error / invalid_request
	status int    // apiErrorStatus; can be missing on a synthetic record (docs/log/47 §2)
}

// authKinds are claude's own machine-readable causes that mean "sign in again". Unlike the
// wording, this field is not rewritten between versions, so it leads the decision (the same
// policy as docs/log/47).
var authKinds = map[string]bool{
	"authentication_failed": true,
	"authentication_error":  true, // in case the API's own type name is carried through as is
}

// oauthExpiredFragment is the stable part of claude's "Failed to authenticate: OAuth session
// expired and could not be refreshed". Narrower than "failed to authenticate" on purpose: that
// phrase also covers failures a new sign-in does not fix. The Console keeps a twin in
// lib/claudeAuthError.ts.
const oauthExpiredFragment = "oauth session expired"

// authMarkers are the texts that say the same thing, for when `error` is empty or an unknown
// value. Held as stems, covering the measured wording ("Please run /login · API Error: 401
// OAuth access token has expired. Re-authenticate to continue.").
var authMarkers = []string{
	"run /login",
	"re-authenticate",
	"refresh your login", // a stuck token refresh; see blockedMarkers in abort.go
	"authentication_failed",
	"invalid api key",
	"unauthorized",
	oauthExpiredFragment,
}

// isAuth reports whether this failure clears by signing in again. status 401 is also an entry
// point, but 403 (permission) and 400 (balance, prompt overflow) are not: telling someone to
// re-authenticate for a failure that re-authenticating does not fix does the same real harm as
// making them wait for a reset that never comes.
func (e apiError) isAuth() bool {
	if authKinds[strings.ToLower(strings.TrimSpace(e.kind))] {
		return true
	}
	if e.status == 401 {
		return true
	}
	low := strings.ToLower(e.msg)
	for _, m := range authMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// cause is the machine-readable hint the Console keys its guidance off. "" = no route.
func (e apiError) cause() string {
	if e.isAuth() {
		return causeAuth
	}
	return ""
}

// label renders the short error identity ("authentication_failed (HTTP 401)").
func (e apiError) label() string {
	name := strings.TrimSpace(e.kind)
	if name == "" {
		name = "error"
	}
	if e.status > 0 {
		return name + " (HTTP " + strconv.Itoa(e.status) + ")"
	}
	return name
}

// detail is the human-facing message. claude's wording is passed through as is (the Console
// draws it verbatim). A record with an empty body falls back to label so at least the heading
// survives.
func (e apiError) detail() string {
	if m := strings.TrimSpace(e.msg); m != "" {
		return m
	}
	return e.label()
}

// summary is the one-line form used where a turn is flattened to text: copy,
// get_session_output, the chat bridge. It carries the same `[error] ` tag opencode/codex use
// so a reader (human or operator) can tell it apart from the agent's own prose.
func (e apiError) summary() string {
	d := e.detail()
	if d == e.label() {
		return "[error] " + d
	}
	return "[error] " + e.label() + ": " + d
}

// part renders the failure as the ordered part the Console draws as an error block.
func (e apiError) part() transcript.Part {
	return transcript.Part{Kind: "error", Info: e.label(), Text: e.detail(), Cause: e.cause()}
}

// cliWarning is a warning the CLI wrote as a system record instead of sending anything to the
// model. The one producer seen so far is a mistyped slash command, which never becomes a user
// turn: without this record the Console's optimistic echo of what was sent has nothing to
// reconcile against and sits at "Pending" forever, while the reason is visible only in the
// terminal. An error part logged after the send is what releases that echo (pendingEcho.ts
// echoLanded).
//
// Measured (claude 2.1.311), one record per line:
//
//	{"type":"system","subtype":"informational","level":"warning","isMeta":false,
//	 "content":"Unknown command: /issue_to_pr. Did you mean /issue-to-pr?"}
//	{"type":"system","subtype":"informational","level":"warning","isMeta":false,
//	 "content":"Args from unknown skill: 通しで2827"}
//
// Only level "warning" is surfaced. The same subtype at level "notice" carries routine status
// (a usage-limit wait, which instruction file was loaded) and is far more frequent; drawn as
// an error it would cry wolf.
type cliWarning struct{ msg string }

// cliWarningLabel is the code shown next to the error heading.
const cliWarningLabel = "warning"

// CauseCLIWarning marks a cliWarning part (transcript.Part.Cause). The Console resolves only a
// slash-command echo against it — a plain prompt sent alongside is still on its way — and the
// usage tally skips the turn, which is no model response.
const CauseCLIWarning = "cli_warning"

// IsCLIWarning reports whether a turn is a cliWarning record rather than a model response.
func IsCLIWarning(t transcript.Turn) bool {
	return len(t.Parts) == 1 && t.Parts[0].Kind == "error" && t.Parts[0].Cause == CauseCLIWarning
}

// CLIWarningText is the flattened form of a raw transcript line that is a cliWarning, "" for any
// other line. get_session_output reads raw lines, not turns, so without it a poller never learns
// that its slash command was refused.
func CLIWarningText(line []byte) string {
	var ev struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Level   string `json:"level"`
		IsMeta  bool   `json:"isMeta"`
		Content string `json:"content"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.Type != "system" || ev.IsMeta {
		return ""
	}
	if w, ok := cliWarningOf(ev.Subtype, ev.Level, ev.Content); ok {
		return w.summary()
	}
	return ""
}

func cliWarningOf(subtype, level, content string) (cliWarning, bool) {
	msg := strings.TrimSpace(content)
	if subtype != "informational" || level != "warning" || msg == "" {
		return cliWarning{}, false
	}
	return cliWarning{msg: msg}, true
}

// summary carries the same `[error] ` tag as apiError.summary for the flattened readers.
func (w cliWarning) summary() string { return "[error] " + cliWarningLabel + ": " + w.msg }

func (w cliWarning) part() transcript.Part {
	return transcript.Part{Kind: "error", Info: cliWarningLabel, Text: w.msg, Cause: CauseCLIWarning}
}
