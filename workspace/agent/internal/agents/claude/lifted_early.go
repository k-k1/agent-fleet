package claude

import (
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/limitepoch"
)

// Source suffixes ResetAt appends when the instant came from a captured window, so
// LiftedEarly knows which window the booking waits for (the bare labels "banner+capture" /
// "capture" do not say).
const (
	sourceWindow5h     = ":5h"
	sourceWindowWeekly = ":weekly"
)

// captureFullPct is the used percentage from which a captured window counts as full. Below
// it a resume is not certain to land in a limited account, so "lifted" is only claimed
// under it (codex uses the same bound).
const captureFullPct = 95

// labelCaptureSource adds the window suffix to a source that was decided by a captured epoch.
// A banner-only source has no captured window behind it and stays bare, which LiftedEarly
// then refuses to judge.
func labelCaptureSource(source string, at time.Time) string {
	if !strings.HasSuffix(source, "capture") {
		return source
	}
	c, _ := readCapturedUsage()
	if c == nil {
		return source
	}
	switch {
	case c.FiveHour != nil && c.FiveHour.ResetsAt == at.Unix():
		return source + sourceWindow5h
	case c.SevenDay != nil && c.SevenDay.ResetsAt == at.Unix():
		return source + sourceWindowWeekly
	}
	return source
}

// LiftedEarly is agents.UsageLimitTraits.LiftedEarly for claude: the statusline capture says
// the window the booking waits for has restarted although the booking still waits for the
// natural reset (a stock reset or a plan change; neither is visible over OAuth, only their
// effect on the windows is). The capture only moves while some claude session renders its
// statusline, so a reading older than the episode, or none, answers false.
//
// The capture is one file for the whole config dir and names no account, and the booking
// records none either. So the account signed in now must have been signed in since before the
// episode opened (limitepoch's Since; zero for the first account ever recorded): after a login
// switch the capture describes another account's windows, which says nothing about the limit
// the booking waits on. An unknown identity answers false.
func LiftedEarly(source string, bookedAt, episodeAt time.Time) bool {
	id := accountID()
	if id == "" {
		return false
	}
	st, ok := limitepoch.Current("claude", id, time.Now())
	if !ok || st.Since.After(episodeAt) {
		return false
	}
	c, _ := readCapturedUsage()
	return liftedEarlyFrom(c, source, bookedAt, episodeAt)
}

// liftedEarlyFrom is the decision, apart from the file read. All of: the source names a
// captured window, THAT window's reading was observed after the episode opened, no captured
// window is at or above captureFullPct, and the window now resets LATER than the booked
// instant, which is the sign it restarted. Any other source (banner-only, or the already
// moved codex-style "reset-early") never counts.
func liftedEarlyFrom(c *capturedUsage, source string, bookedAt, episodeAt time.Time) bool {
	if c == nil {
		return false
	}
	var w *capturedWindow
	var fiveHour bool
	switch {
	case strings.HasSuffix(source, "capture"+sourceWindow5h):
		w, fiveHour = c.FiveHour, true
	case strings.HasSuffix(source, "capture"+sourceWindowWeekly):
		w = c.SevenDay
	default:
		return false
	}
	if w == nil || w.ResetsAt <= 0 {
		return false
	}
	if at := c.windowObservedAt(fiveHour); at.IsZero() || !at.After(episodeAt) {
		return false
	}
	for _, o := range []*capturedWindow{c.FiveHour, c.SevenDay} {
		if o != nil && o.UsedPercent >= captureFullPct {
			return false
		}
	}
	return time.Unix(w.ResetsAt, 0).After(bookedAt)
}
