package sessionx

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
	TurnSourceOperator = "operator" // fleet operator injected (docs/log/30 ②)
	TurnSourceDiscord  = "discord"  // chat bridge — Discord thread reply (docs/log/37 P2a)
	TurnSourceSlack    = "slack"    // chat bridge — Slack (P2 follow-up)
	TurnSourceSchedule = "schedule" // scheduled execution fired it (docs/log/38 — CP scheduler create/reuse send)
	// TurnSourceScheduleManual is a schedule fired by run-now — same pipeline as "schedule"
	// but user-initiated, so the mirror can badge scheduled vs manual distinctly (docs/log/38).
	TurnSourceScheduleManual = "schedule-manual"
	// TurnSourceAutoResume is the Agent's own nudge after a retryable cut-off (docs/log/47
	// §4-6). It gets its own badge because it is not anyone's instruction but self-repair after
	// an interruption: a "continue" that neither the user nor the operator sent is the worst
	// thing to find unattributed in the mirror.
	TurnSourceAutoResume = "auto-resume"
	// turnSourcePeer is another SESSION's message (docs/log/58 / ADR 0041). It needs its own
	// badge for the same reason as auto-resume, only more urgently: when an instruction that
	// neither the user nor the operator sent appears in the mirror, without this badge there is
	// no way to tell it came from a session in a neighbouring worktree. This badge is the only
	// visualization of a peer arrival, so omitting it makes the path invisible to humans.
	//
	// The spelling lives in internal/transcript because the transcript parser sets this value
	// directly for arrivals over claude's own cross-session channel (those bypass AF, so this
	// injection store holds nothing for them; docs/log/58 §58.16). Writing "peer" separately on
	// each side would make the badge appear on some paths and not others.
	turnSourcePeer = transcript.SourcePeer
)

// injectionSource maps a caller-supplied source onto the recordable vocabulary. The
// /input and create_session wire fields are reachable by any client, so an unknown
// value degrades to "operator" (the pre-existing meaning of a report_to-carrying
// injection) instead of minting arbitrary badge strings in the Console.
func injectionSource(s string) string {
	switch s {
	case TurnSourceSchedule, TurnSourceScheduleManual:
		return s
	default:
		return TurnSourceOperator
	}
}

// scheduleInjectionSource returns the schedule origin a caller declared, or "" when the
// source is not a schedule at all.
//
// Why this is needed next to injectionSource(): that one falls back to operator for an
// unknown/empty value, so it cannot decide anything that must be independent of report_to
// (plain Console input would get an operator badge too). Separating them was necessary: while
// the origin was recorded only inside report_to != "", schedule injections with completion
// reporting OFF lost their badge entirely. report_to is set only when report=true (CP
// scheduleReportTo), the Console's completion-report checkbox defaults to OFF, and auto-resume
// after a usage limit always sends report=false. The source had been arriving all along and
// was discarded on the unrelated condition of whether a report was wanted.
func scheduleInjectionSource(s string) string {
	switch s {
	case TurnSourceSchedule, TurnSourceScheduleManual:
		return s
	default:
		return ""
	}
}

// badgeOriginOf decides in one place which badge an injection gets in the mirror. "" means
// the user typed it themselves, i.e. no badge.
//
// The per-path (TUI / managed) switches are merged here so the recording can be moved ahead of
// delivery (see the warning below). Moving only one path would make the same badge appear for
// some kinds and not others.
func badgeOriginOf(peerFrom, reportTo, source string) string {
	switch {
	case peerFrom != "":
		return turnSourcePeer
	case reportTo != "":
		return injectionSource(source)
	default:
		// Only scheduled runs with reporting OFF land here (anything else is "" = plain input).
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
// Always call this BEFORE the injection itself. Tagging re-reads this file on every request,
// so if the record lands after the transcript's user line, a poll arriving in that gap returns
// the same turn with no origin. The mirror never re-fetches a turn it already has (increments
// carry only what is newer than since), so a turn once delivered plain stays unbadged until
// the screen is reopened. A peer send always waits for delivery confirmation (i.e. for the
// user line to appear in the transcript), so recording afterwards opens that gap structurally
// — measured: 524ms.
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
func recordOperatorInjection(name, text string) { recordInjection(name, text, TurnSourceOperator) }

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
// `<command-name>/<command-message>` tag block (either tag first — measured on 2.1.215,
// skills are message-first), so an exact text compare never hits and the badge silently
// vanished for every injected slash command. commandSlashForm recovers "/name args" from the
// tag block so those turns tag too.
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
