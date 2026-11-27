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

// quotaRefusalSource is the episode's Source for a reset read off the banner.
const quotaRefusalSource = "agy:quota-banner"

// quotaBannerInFrame returns the refusal text when the frame's newest transcript content, just
// above the idle composer, is agy's quota banner. ok=false for a frame without the idle footer
// (a turn still running, a prompt waiting), for a banner with anything after it, and for no banner.
func quotaBannerInFrame(frame string) (text string, ok bool) {
	if !hasIdleFooter(frame) {
		return "", false
	}
	lines := strings.Split(strings.TrimRight(frame, "\n "), "\n")
	// Skip the composer: the footer, its rules and the input row.
	i := len(lines) - 1
	for ; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" || isRule(t) || strings.HasPrefix(t, ">") || strings.Contains(t, "? for shortcuts") {
			continue
		}
		break
	}
	var content []string
	for ; i >= 0 && len(content) < quotaBannerLines; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" || isRule(t) {
			continue
		}
		content = append([]string{t}, content...)
	}
	joined := strings.Join(content, " ")
	locs := quotaReachedRe.FindAllStringIndex(joined, -1)
	if len(locs) == 0 {
		return "", false
	}
	tail := joined[locs[len(locs)-1][1]:]
	if !quotaTailRe.MatchString(tail) {
		return "", false
	}
	return joined, true
}

func isRule(t string) bool { return strings.Trim(t, "─━-═ ") == "" }

// paneQuotaBanner is quotaBannerInFrame on the session's live pane.
func paneQuotaBanner(name string) (string, bool) {
	return quotaBannerInFrame(tmuxx.CapturePane(session.TmuxName(name)))
}

// TurnFailure is agents.LiveStateTraits.TurnFailure: the turn that just ended was refused for
// quota when the banner is the last thing it drew. Without it the poll reports a clean completion.
func TurnFailure(m session.Meta, _ string) string {
	if text, ok := paneQuotaBanner(m.Name); ok {
		return text
	}
	return ""
}

// atQuotaLimit is agents.UsageLimitTraits.AtLimit: the quota is a window that waiting clears.
func atQuotaLimit(m session.Meta) (agents.LimitKind, bool) {
	_, ok := paneQuotaBanner(m.Name)
	return agents.LimitWindow, ok
}

// quotaResetAt is agents.UsageLimitTraits.ResetAt. The countdown is relative to when the
// refusal was drawn, not to this poll, so it counts from the conversation DB's last write
// (never later than now); a later base only books the resume late, never early.
func quotaResetAt(m session.Meta, _ bool, now time.Time) (time.Time, string, bool) {
	text, ok := paneQuotaBanner(m.Name)
	if !ok {
		return time.Time{}, "", false
	}
	base := now
	if conv := sids.Read(session.UUID(m.Dir, m.Name)); conv != "" {
		if mt, ok := dbModTime(conv); ok && mt.Before(now) {
			base = mt
		}
	}
	at, ok := LimitInMessage(text, base)
	if !ok || at.IsZero() {
		return time.Time{}, "", false
	}
	return at, quotaRefusalSource, true
}
