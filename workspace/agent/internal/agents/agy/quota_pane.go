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

// The banner is matched with every whitespace character removed: the TUI wraps it at a width
// of its own, and a break can fall between words, inside a word or inside a number. Detection and
// the reset instant read the same compact text, so they cannot disagree about a wrap.
//
// head: optional glyphs and an "error:" label, then the phrase, at the very start. A markdown
// quote or bullet (">", "-", "*"), a prompt echo and a sentence that merely mentions the phrase
// all have something else in front of it. tail: the upgrade hint and the countdown, either of
// them optional but not both; anything else is a newer line of the transcript.
var (
	quotaHeadRe  = regexp.MustCompile(`(?i)^(?:[^\p{L}\p{N}>\-*+•])*(?:(?:error|warning)[:]?)?individualquotareached`)
	quotaTailRe  = regexp.MustCompile(`(?i)^[.:,]*(?:pleaseupgradeyoursubscriptiontoincreaseyourlimits\.?)?(?:resetsin(?:\d+[a-z]+)+\.?)?$`)
	quotaResetRe = regexp.MustCompile(`(?i)resetsin((?:\d+[a-z]+)+)`)
)

func compact(s string) string {
	return strings.Join(strings.Fields(s), "")
}

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
	// Newest candidate first: the banner may be wrapped over several lines.
	for k := len(content) - 1; k >= 0; k-- {
		joined := strings.Join(content[k:], " ")
		c := compact(joined)
		loc := quotaHeadRe.FindStringIndex(c)
		if loc == nil {
			continue
		}
		if tail := c[loc[1]:]; quotaTailRe.MatchString(tail) && strings.Trim(tail, ".:,") != "" {
			return joined, true
		}
	}
	return "", false
}

// quotaResetIn is the countdown of a banner, ok=false when it names none (or a unit it does not
// know).
func quotaResetIn(text string) (time.Duration, bool) {
	m := quotaResetRe.FindStringSubmatch(compact(text))
	if m == nil {
		return 0, false
	}
	d := resetDuration(strings.Join(resetPartRe.FindAllString(m[1], -1), " "))
	return d, d > 0
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
	// Keyed by the compact text: a resize that wraps the same banner differently is the same refusal.
	key := compact(text)
	if e, ok := quotaSeen.m[name]; ok && e.text == key {
		return e.at
	}
	quotaSeen.m[name] = quotaSighting{text: key, at: now}
	return now
}

func forgetQuota(name string) {
	quotaSeen.Lock()
	delete(quotaSeen.m, name)
	quotaSeen.Unlock()
}

// quotaTurnFailure is the pane half of agents.LiveStateTraits.TurnFailure: the turn that just ended was refused for
// quota when the banner is the last thing it drew. Without it the poll reports a clean
// completion. The pane carries no turn identity, so a turn without a mark (a first input, one
// typed into the terminal) is judged too (TurnFailureUnmarked).
//
// One capture answers all three questions (refused, cleanly idle, not confirmable yet), and it
// never waits. When the pane is not captured or its idle footer is not painted while the
// conversation was written within quotaPaintGrace, a refusal may still be about to appear: it
// returns agents.TurnFailurePending, and the poll asks again. Past the grace the end completes
// as before, so an unreadable pane cannot hold a completion back for good.
func quotaTurnFailure(m session.Meta) string {
	frame := capturePaneFrame(session.TmuxName(m.Name))
	if text, ok := quotaBannerInFrame(frame); ok {
		return text
	}
	if hasIdleFooter(frame) {
		return ""
	}
	// No frame at all only counts as "not captured" while the tmux session is there: a session
	// without a pane has nothing to wait for.
	if strings.TrimSpace(frame) == "" && !paneSessionAlive(session.TmuxName(m.Name)) {
		return ""
	}
	captureConversation(m)
	conv := sids.Read(session.UUID(m.Dir, m.Name))
	if conv == "" {
		return ""
	}
	if mt, ok := dbModTime(conv); ok && quotaNow().Sub(mt) < quotaPaintGrace {
		return agents.TurnFailurePending
	}
	return ""
}

// atQuotaLimit is agents.UsageLimitTraits.AtLimit: the quota is a window that waiting clears.
func atQuotaLimit(m session.Meta) (agents.LimitKind, bool) {
	frame := capturePaneFrame(session.TmuxName(m.Name))
	text, ok := quotaBannerInFrame(frame)
	if !ok {
		// Forget the first sighting only on an idle frame that shows the banner gone: a failed
		// capture or an unpainted footer says nothing, and dropping it would move the reset.
		if hasIdleFooter(frame) {
			forgetQuota(m.Name)
		}
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
	d, ok := quotaResetIn(text)
	if !ok {
		return time.Time{}, "", false
	}
	return firstSeenQuota(m.Name, text, quotaNow()).Add(d), quotaRefusalSource, true
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
	if mt, ok := dbModTime(conv); !ok || !mt.After(at) {
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

// polledTurnFailure is agents.LiveStateTraits.TurnFailure: the conversation's own executor error
// first (it needs the turn mark and names the turn), then the quota banner the TUI draws, which
// no step records.
func polledTurnFailure(m session.Meta, mark string) string {
	if failure := turnFailure(m, mark); failure != "" {
		return failure
	}
	return quotaTurnFailure(m)
}
