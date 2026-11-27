package agy

// A quota-refused turn in an agy Terminal session.
//
// agy has no hook for a failed turn and its conversation DB records the refusal on no step
// (error_details stays empty), so the only per-session evidence is the line the TUI draws as the
// last thing of the turn. The sweep's usage-limit capability reads it from the pane, and the
// banner counts only when it is the LAST thing in the transcript above the composer: a newer
// successful turn draws its reply below an older refusal that is still on screen, and booking a
// resume against that refusal would send a prompt onto unrelated work. The refused turn is NOT
// reported as a failed turn: it ends as an ordinary idle and the session reads limited through
// the sweep's episode, which is also what keeps a queue run waiting instead of closing it.

import (
	"database/sql"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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

// quotaNow is the clock of the first sighting, read when the pane was actually captured (not the
// sweep's start time, which a slow earlier session can leave seconds behind).
var quotaNow = time.Now

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

// atQuotaLimit is agents.UsageLimitTraits.AtLimit: the quota is a window that waiting clears.
// newestTurnEnded says the screen may be read as the newest turn's outcome: the state source
// agrees that turn has ended, and it did not end on an executor error that is not a quota refusal
// (the conversation's own failure of the current turn wins over a banner a lagging pane still
// shows from the turn before).
func newestTurnEnded(m session.Meta) bool {
	if LiveState(m) != "idle" {
		return false
	}
	if r, ok := lastStep(m); ok && r.stepType == stepTypeError && !quotaReachedRe.MatchString(executorErrorPayload(r.payload)) {
		return false
	}
	return true
}

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
	// The screen is only evidence about the newest turn once the state source agrees that turn
	// has ended: a new input that reached the conversation before the pane repainted would
	// otherwise leave the previous turn's refusal looking current.
	if !newestTurnEnded(m) {
		return agents.LimitWindow, false
	}
	firstSeenQuota(m.Name, text, quotaNow())
	return agents.LimitWindow, true
}

// quotaResetAt is agents.UsageLimitTraits.ResetAt: the banner's countdown from its first
// sighting. ok=false when the newest banner names no countdown, so an older one is never read.
func quotaResetAt(m session.Meta, _ bool, _ time.Time) (time.Time, string, bool) {
	text, ok := paneQuotaBanner(m.Name)
	// The same newest-turn condition as AtLimit: a follow-up that books late (auto-resume turned on
	// after the episode opened) must not book from a frame AtLimit rejected.
	if !ok || !newestTurnEnded(m) {
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
	// One statement is one snapshot: the run's outcome and end position come from the same
	// executor_metadata row, and the newest step from the same read, so a run recorded between
	// two reads cannot mix an old success with a newer step list. An executor error is a run that
	// completed (field 1 = 4) with an error step last, which is a failed turn, not a successful one.
	var data []byte
	var last, lastType sql.NullInt64
	if db.QueryRow(`SELECT (SELECT data FROM executor_metadata ORDER BY idx DESC LIMIT 1),
		(SELECT idx FROM steps ORDER BY idx DESC LIMIT 1),
		(SELECT step_type FROM steps ORDER BY idx DESC LIMIT 1)`).Scan(&data, &last, &lastType) != nil ||
		len(data) == 0 || !last.Valid || !lastType.Valid {
		return false
	}
	kind, found, ok := protoVarintField(data, 1)
	if !ok || !found || kind != executorCompleted {
		return false
	}
	end, found3, ok := protoVarintField(data, 3)
	if !ok || end > math.MaxInt32 {
		return false
	}
	if !found3 {
		end = 0 // proto3 leaves a zero out of the wire (see turnEnd)
	}
	return int64(end) >= last.Int64 && int(lastType.Int64) != stepTypeError
}
