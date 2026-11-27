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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// quotaBannerLines bounds how many transcript lines the banner may be wrapped over.
const quotaBannerLines = 4

// quotaTailRe is everything the banner may carry after "quota reached": the upgrade hint and
// the countdown, in either's absence. Anything else after it is a newer line of the transcript.
var quotaTailRe = regexp.MustCompile(`(?i)^[\s.:,]*(?:please upgrade your subscription to increase your limits\.?)?\s*(?:resets in (?:\d+\s*[a-z]+\s*)+\.?)?\s*$`)

// quotaPrefixRe is what may precede "quota reached" on its line: glyphs and the "error:" label
// agy puts there. A sentence that merely mentions the phrase has words in front of it.
var quotaPrefixRe = regexp.MustCompile(`(?i)^[^\p{L}\p{N}]*(?:(?:error|warning)\b[:\s]*)?(?:individual\s*)?$`)

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
// a sentence that quotes the phrase, and no banner.
func quotaBannerInFrame(frame string) (text string, ok bool) {
	content, ok := contentAboveComposer(frame)
	if !ok {
		return "", false
	}
	j := -1
	for k, l := range content {
		if quotaReachedRe.MatchString(l) {
			j = k
		}
	}
	if j < 0 {
		return "", false
	}
	first := j
	loc := quotaReachedRe.FindStringIndex(content[j])
	prefix := content[j][:loc[0]]
	if strings.TrimSpace(prefix) == "" && j > 0 && strings.HasSuffix(strings.ToLower(content[j-1]), "individual") {
		first = j - 1
	} else if !quotaPrefixRe.MatchString(prefix) {
		return "", false
	}
	// A wrap that splits a word or a number joins without the space.
	for _, sep := range []string{" ", ""} {
		joined := strings.Join(content[first:], sep)
		locs := quotaReachedRe.FindAllStringIndex(joined, -1)
		tail := joined[locs[len(locs)-1][1]:]
		if quotaTailRe.MatchString(tail) && strings.Trim(tail, " .:,") != "" {
			return joined, true
		}
	}
	return "", false
}

func isRule(t string) bool { return strings.Trim(t, "─━-═ ") == "" }

// Reading the pane is allowed to fail or to lag the state source: the DB can say idle before the
// footer is painted, and a capture can error out. Both read as "no banner", which would book a
// clean completion for good, so a turn-end read retries a few times first.
var (
	quotaRetryWait   = 200 * time.Millisecond
	quotaRetryRounds = 5
)

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
func TurnFailure(m session.Meta, _ string) string {
	for try := 0; ; try++ {
		frame := capturePaneFrame(session.TmuxName(m.Name))
		if text, ok := quotaBannerInFrame(frame); ok {
			firstSeenQuota(m.Name, text, time.Now())
			return text
		}
		// A frame without the idle footer is not yet painted (or not readable): ask again.
		if hasIdleFooter(frame) || try >= quotaRetryRounds {
			return ""
		}
		time.Sleep(quotaRetryWait)
	}
}

// atQuotaLimit is agents.UsageLimitTraits.AtLimit: the quota is a window that waiting clears.
func atQuotaLimit(m session.Meta) (agents.LimitKind, bool) {
	_, ok := paneQuotaBanner(m.Name)
	if !ok {
		forgetQuota(m.Name)
	}
	return agents.LimitWindow, ok
}

// quotaResetAt is agents.UsageLimitTraits.ResetAt: the banner's countdown from its first
// sighting. ok=false when the newest banner names no countdown, so an older one is never read.
func quotaResetAt(m session.Meta, _ bool, now time.Time) (time.Time, string, bool) {
	text, ok := paneQuotaBanner(m.Name)
	if !ok {
		return time.Time{}, "", false
	}
	at, ok := LimitInMessage(text, firstSeenQuota(m.Name, text, now))
	if !ok || at.IsZero() {
		return time.Time{}, "", false
	}
	return at, quotaRefusalSource, true
}

// ranTurnSince is agents.UsageLimitTraits.RanTurnSince: the episode is only followed up once the
// banner has left the tail of the transcript, so a conversation write after the episode opened is
// a newer turn, and a booked "continue" would land on unrelated work.
func ranTurnSince(m session.Meta, at time.Time) bool {
	conv := sids.Read(session.UUID(m.Dir, m.Name))
	if conv == "" {
		return false
	}
	mt, ok := dbModTime(conv)
	return ok && mt.After(at.Add(2*time.Second))
}
