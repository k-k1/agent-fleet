package agy

// A quota-refused turn in an agy Terminal session.
//
// agy has no hook for a failed turn and its conversation DB records the refusal on no step
// (error_details stays empty), so the only per-session evidence is the line the TUI draws as the
// last thing of the turn. The sweep's usage-limit capability and the polled turn end both read
// it from the pane, and both need the same attribution: the banner counts only when it is the
// LAST thing in the transcript above the composer, because a newer successful turn draws its
// reply below an older refusal that is still on screen, and booking a resume against that
// refusal would send a prompt onto unrelated work.

import (
	"database/sql"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// quotaBannerLines bounds how many transcript lines the banner may be wrapped over.
const quotaBannerLines = 4

// quotaTailRe is everything the banner may carry after "quota reached": the upgrade hint and
// the countdown, in either's absence. Anything else after it is a newer line of the transcript.
var quotaTailRe = regexp.MustCompile(`(?i)^[\s.:,]*(?:please upgrade your subscription to increase your limits\.?)?\s*(?:resets in (?:\d+\s*[a-z]+\s*)+\.?)?\s*$`)

// quotaHeadRe is how the banner starts: optional glyphs and an "error:" label, then the phrase
// itself, at the very start. A markdown quote or bullet (">", "-", "*"), a prompt echo and a
// sentence that merely mentions the phrase all have something else in front of it.
var quotaHeadRe = regexp.MustCompile(`(?i)^(?:[^\p{L}\p{N}>\-*+•]\s*)*(?:(?:error|warning)\b[:\s]*)?individual\s*quota\s*reached\b`)

// quotaRefusalSource is the episode's Source for a reset read off the banner.
const quotaRefusalSource = "agy:quota-banner"

// contentAboveComposer returns the non-blank transcript lines just above the idle composer,
// oldest first. The composer is the footer, its rules and exactly ONE input row: a second row
// starting with ">" above it is the echo of a user prompt, a turn boundary that ends the
// search for the refusal of the turn before it. ok=false without the idle footer.
func contentAboveComposer(frame string) ([]string, bool) {
	if !hasIdleFooter(frame) {
		return nil, false
	}
	lines := strings.Split(strings.TrimRight(frame, "\n "), "\n")
	i := len(lines) - 1
	skip := func() {
		for ; i >= 0; i-- {
			if t := strings.TrimSpace(lines[i]); t != "" && !isRule(t) {
				return
			}
		}
	}
	skip()
	if i >= 0 && strings.Contains(lines[i], "? for shortcuts") {
		i--
	}
	skip()
	if i >= 0 && strings.HasPrefix(strings.TrimSpace(lines[i]), ">") {
		i--
	}
	skip()
	var content []string
	for ; i >= 0 && len(content) < quotaBannerLines; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" && !isRule(t) {
			content = append([]string{t}, content...)
		}
	}
	return content, true
}

// quotaBannerInFrame returns the refusal text, and only that, when the frame's newest
// transcript content just above the idle composer is agy's quota banner. ok=false for a frame
// without the idle footer (a turn running, a prompt waiting), a banner with anything after it,
// a line that quotes the phrase, and no banner.
func quotaBannerInFrame(frame string) (text string, ok bool) {
	content, ok := contentAboveComposer(frame)
	if !ok {
		return "", false
	}
	// Newest candidate first: the banner may be wrapped over several lines, and a wrap that
	// splits a word or a number joins without the space.
	for k := len(content) - 1; k >= 0; k-- {
		for _, join := range []func([]string) string{joinWrapped, joinWith(" "), joinWith("")} {
			joined := join(content[k:])
			loc := quotaHeadRe.FindStringIndex(joined)
			if loc == nil {
				continue
			}
			if tail := joined[loc[1]:]; quotaTailRe.MatchString(tail) && strings.Trim(tail, " .:,") != "" {
				return joined, true
			}
		}
	}
	return "", false
}

func joinWith(sep string) func([]string) string {
	return func(l []string) string { return strings.Join(l, sep) }
}

// joinWrapped joins wrapped lines with a space except where a digit meets a digit, which is a
// number the wrap cut ("1h3" / "9m46s"), so a banner wrapped between words and through a number
// reads whole.
func joinWrapped(lines []string) string {
	var b strings.Builder
	for i, l := range lines {
		if i > 0 {
			prev := lines[i-1]
			if !(prev[len(prev)-1] >= '0' && prev[len(prev)-1] <= '9' && l[0] >= '0' && l[0] <= '9') {
				b.WriteByte(' ')
			}
		}
		b.WriteString(l)
	}
	return b.String()
}

func isRule(t string) bool { return strings.Trim(t, "─━-═ ") == "" }

// paneSessionAlive is the tmux liveness probe, replaceable by tests.
var paneSessionAlive = tmuxx.HasSession

// quotaNow is the clock of the first sighting, read when the pane was actually captured (not the
// sweep's start time, which a slow earlier session can leave seconds behind).
var quotaNow = time.Now

// quotaPaintGrace is how long after the conversation's last write an unreadable or unpainted
// pane still counts as "not drawn yet" rather than as a clean end.
const quotaPaintGrace = 30 * time.Second

// paneQuotaBanner is quotaBannerInFrame on the session's live pane.
func paneQuotaBanner(name string) (string, bool) {
	return quotaBannerInFrame(capturePaneFrame(session.TmuxName(name)))
}

// quotaSeen remembers when each session's current banner was first read. The countdown is
// relative to when the refusal was drawn, which nothing records (the conversation DB's writes
// say nothing about it), so the first sighting is the base: never earlier than the refusal, so
// a resume booked from it is late by at most one sweep, never early.
var quotaSeen = struct {
	sync.Mutex
	m map[string]quotaSighting
}{m: map[string]quotaSighting{}}

type quotaSighting struct {
	text string
	at   time.Time
}

func firstSeenQuota(name, text string, now time.Time) time.Time {
	quotaSeen.Lock()
	defer quotaSeen.Unlock()
	if e, ok := quotaSeen.m[name]; ok && e.text == text {
		return e.at
	}
	quotaSeen.m[name] = quotaSighting{text: text, at: now}
	return now
}

func forgetQuota(name string) {
	quotaSeen.Lock()
	delete(quotaSeen.m, name)
	quotaSeen.Unlock()
}

// TurnFailure is agents.LiveStateTraits.TurnFailure: the turn that just ended was refused for
// quota when the banner is the last thing it drew. Without it the poll reports a clean
// completion. The mark is not used: the pane carries no turn identity, and a turn without a mark
// (a first input, one typed into the terminal) must still be judged (TurnFailureUnmarked).
// It reads the pane once and never waits: a poll that blocks here can outlive the turn it judges.
func TurnFailure(m session.Meta, _ string) string {
	if text, ok := paneQuotaBanner(m.Name); ok {
		return text
	}
	return ""
}

// TurnEndPending is agents.LiveStateTraits.TurnEndPending: the state source says the turn ended,
// but the pane cannot confirm it yet (not captured, or the idle footer not painted) and the
// conversation was written within quotaPaintGrace, so a refusal may still be about to appear.
// The poll leaves the end unrecorded and asks again; past the grace it completes as before, so
// an unreadable pane cannot hold a completion back for good.
func TurnEndPending(m session.Meta) bool {
	frame := capturePaneFrame(session.TmuxName(m.Name))
	if hasIdleFooter(frame) {
		return false
	}
	// No frame at all only counts as "not captured" while the tmux session is there: a session
	// without a pane has nothing to wait for.
	if strings.TrimSpace(frame) == "" && !paneSessionAlive(session.TmuxName(m.Name)) {
		return false
	}
	captureConversation(m)
	conv := sids.Read(session.UUID(m.Dir, m.Name))
	if conv == "" {
		return false
	}
	mt, ok := dbModTime(conv)
	return ok && quotaNow().Sub(mt) < quotaPaintGrace
}

// atQuotaLimit is agents.UsageLimitTraits.AtLimit: the quota is a window that waiting clears.
func atQuotaLimit(m session.Meta) (agents.LimitKind, bool) {
	text, ok := paneQuotaBanner(m.Name)
	if !ok {
		forgetQuota(m.Name)
		return agents.LimitWindow, false
	}
	firstSeenQuota(m.Name, text, quotaNow())
	return agents.LimitWindow, true
}

// quotaResetAt is agents.UsageLimitTraits.ResetAt: the banner's countdown from its first
// sighting. ok=false when the newest banner names no countdown, so an older one is never read.
func quotaResetAt(m session.Meta, _ bool, _ time.Time) (time.Time, string, bool) {
	text, ok := paneQuotaBanner(m.Name)
	if !ok {
		return time.Time{}, "", false
	}
	at, ok := LimitInMessage(text, firstSeenQuota(m.Name, text, quotaNow()))
	if !ok || at.IsZero() {
		return time.Time{}, "", false
	}
	return at, quotaRefusalSource, true
}

// ranTurnSince is agents.UsageLimitTraits.RanTurnSince: the session finished a turn successfully
// after the episode opened, so a booked "continue" would land on unrelated work. The evidence is
// the newest executor run ending COMPLETED (field 1 = 4, an Esc cancel is 2) at the last step,
// the conversation written after the episode, and the idle composer on screen. A write alone
// (an input that has just landed, a turn still running, a checkpoint at exit) is not evidence;
// neither is a run that was interrupted.
func ranTurnSince(m session.Meta, at time.Time) bool {
	conv := sids.Read(session.UUID(m.Dir, m.Name))
	if conv == "" || !hasIdleFooter(capturePaneFrame(session.TmuxName(m.Name))) {
		return false
	}
	if mt, ok := dbModTime(conv); !ok || !mt.After(at.Add(2*time.Second)) {
		return false
	}
	return lastRunCompleted(conv)
}

// executorCompleted is the executor_metadata field 1 of a run that ended normally (measured,
// v1.2.14: 4 completed, 2 canceled).
const executorCompleted = 4

// lastRunCompleted: the newest executor run completed and ended at the newest step.
func lastRunCompleted(conv string) bool {
	db, err := sql.Open("sqlite", "file:"+conversationDBPath(conv)+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return false
	}
	defer db.Close()
	var data []byte
	if db.QueryRow(`SELECT data FROM executor_metadata ORDER BY idx DESC LIMIT 1`).Scan(&data) != nil {
		return false
	}
	kind, found, ok := protoVarintField(data, 1)
	if !ok || !found || kind != executorCompleted {
		return false
	}
	var last int
	if db.QueryRow(`SELECT MAX(idx) FROM steps`).Scan(&last) != nil {
		return false
	}
	end, state := turnEnd(db)
	return state == turnLogRead && end >= last
}
