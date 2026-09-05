package claude

// Pinning down when a usage limit lifts (docs/log/47 §4-4).
//
// When a turn is cut off by the limit, claude leaves one banner line in the transcript:
//
//	You've hit your session limit · resets 7:50pm (Asia/Tokyo)
//
// That is an interruption that clears once a given time arrives, but abort.go's
// classification is binary (retryable / blocked), so it is filed as blocked (a resend before
// that time gets the same result). This file answers only "when does it lift" for that third
// class. The answer is built from two independent materials:
//
//   - The banner's wall clock (from the transcript, i.e. the wording that session actually
//     received). No date is written in it.
//   - resets_at from the statusline capture (af-usage.json). Being a unix epoch it is
//     unambiguous, but it is per account and holds the value from the last render, so it can
//     be stale.
//
// The wall clock alone cannot be told apart from "the same time tomorrow", and the epoch
// alone does not say which window was hit, the five-hour or the weekly one. So the banner
// picks the window and the epoch fixes the date.

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// resetClockRe pulls the reset wall-clock out of the banner. The only form measured is
// "resets 7:50pm (Asia/Tokyo)", so it takes that reliably while also tolerating a form
// carrying a date ("resets Aug 3 at 9am (…)") and a missing minute or timezone. The am/pm is
// mandatory: matching bare digits would hit unrelated numbers in the body.
var resetClockRe = regexp.MustCompile(`(?i)resets\s+(?:[a-z]{3,9}\.?\s+\d{1,2},?\s+)?(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)\b\s*(?:\(([^)]+)\))?`)

// weeklyBannerRe recognises the weekly window's banner — "You've hit your weekly limit ·
// resets 9am (Asia/Tokyo)" (measured corpus). A form carrying a date ("resets Aug 3 at 9am")
// is excluded because it has none of the wall-clock-only ambiguity.
var weeklyBannerRe = regexp.MustCompile(`(?i)weekly limit.*resets\s+(?:at\s+)?\d{1,2}(?::\d{2})?\s*(?:am|pm)\b`)

// parseResetClock reads the banner's reset time. loc is the banner's own zone when it
// names one (claude prints the IANA name), else the container's local zone — which is
// the same zone claude renders in, so the fallback is not a guess about the user.
func parseResetClock(msg string) (hour, min int, loc *time.Location, ok bool) {
	m := resetClockRe.FindStringSubmatch(msg)
	if m == nil {
		return 0, 0, nil, false
	}
	h, err := strconv.Atoi(m[1])
	if err != nil || h < 1 || h > 12 {
		return 0, 0, nil, false
	}
	if m[2] != "" {
		if min, err = strconv.Atoi(m[2]); err != nil || min > 59 {
			return 0, 0, nil, false
		}
	}
	// 12-hour to 24-hour: 12am = 0, 12pm = 12.
	h %= 12
	if strings.EqualFold(m[3], "pm") {
		h += 12
	}
	loc = time.Local
	if m[4] != "" {
		if l, err := time.LoadLocation(strings.TrimSpace(m[4])); err == nil {
			loc = l
		}
	}
	return h, min, loc, true
}

// firstAfter returns the first instant whose wall clock in loc is hour:min and which is
// strictly after base. base is the moment the banner was written, i.e. the abort record's
// timestamp. It is the reference rather than now because the menu can sit on screen for hours
// before anyone finds it (measured: about 16 hours), and against now a reset that has already
// passed would be read as "tomorrow".
func firstAfter(base time.Time, hour, min int, loc *time.Location) time.Time {
	b := base.In(loc)
	t := time.Date(b.Year(), b.Month(), b.Day(), hour, min, 0, 0, loc)
	if !t.After(b) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// capturedResets returns the reset instants of the last statusline capture (five-hour
// and seven-day), oldest first. Zero/absent windows are dropped.
func capturedResets() []time.Time {
	c, _ := readCapturedUsage()
	if c == nil {
		return nil
	}
	var out []time.Time
	for _, w := range []*capturedWindow{c.FiveHour, c.SevenDay} {
		if w != nil && w.ResetsAt > 0 {
			out = append(out, time.Unix(w.ResetsAt, 0))
		}
	}
	if len(out) == 2 && out[1].Before(out[0]) {
		out[0], out[1] = out[1], out[0]
	}
	return out
}

// resetMatchWindow is how far a captured epoch may sit from the banner's wall clock and
// still be considered the same reset. claude rounds the banner to the minute, so this is
// slack for rounding, not for a different window (the two windows are hours apart).
const resetMatchWindow = 2 * time.Minute

// resolveResetAt is the pure decision: when does the limit behind msg lift?
//
//	abortedAt — the abort record's time (the moment the banner was written); now when zero.
//	captured  — the resets_at values from the statusline capture.
//
// source labels which material decided it (for logs). ok=false means "could not decide", and
// the caller then arms no auto-resume: waking at a guessed time only hits the limit again.
func resolveResetAt(msg string, abortedAt time.Time, captured []time.Time, now time.Time) (at time.Time, source string, ok bool) {
	base := abortedAt
	if base.IsZero() {
		base = now
	}
	if h, m, loc, parsed := parseResetClock(msg); parsed {
		want := firstAfter(base, h, m, loc)
		// When a captured epoch points at the same wall clock, it wins (that fixes the date).
		for _, c := range captured {
			if c.After(base) && absDur(c.Sub(want)) <= resetMatchWindow {
				return c, "banner+capture", true
			}
		}
		// The weekly window alone is never decided from the banner (docs/log/47 §4-10). The
		// banner carries only a wall clock, so "resets 9am" can only be read as "9am today or
		// tomorrow", while a weekly reset can be days away. Waking at the 9am tomorrow that
		// firstAfter returns hits the same 429, which opens a fresh episode and books again —
		// burning one turn a day until the real reset.
		//
		// The match above asks "is it the same instant", so it never lands on a weekly reset
		// days out. Here only the wall clock is compared and the date is left to the captured
		// epoch. The newest is tried first because of the two windows the capture returns
		// (five-hour and weekly) the weekly one is always the later.
		if weeklyBannerRe.MatchString(msg) {
			for i := len(captured) - 1; i >= 0; i-- {
				c := captured[i]
				if l := c.In(loc); c.After(base) && l.Hour() == h && l.Minute() == m {
					return c, "banner+capture", true
				}
			}
			return time.Time{}, "", false
		}
		return want, "banner", true
	}
	// The banner is unreadable (a version changed the wording, etc.). Bet on the earliest
	// captured window still in the future: being at a limit, the next lift is one of them.
	for _, c := range captured {
		if c.After(now) {
			return c, "capture", true
		}
	}
	return time.Time{}, "", false
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// ResetAt answers when the session's usage limit lifts, reading the session's own
// transcript tail (the banner) and the shared statusline capture. ok=false when neither
// material yields a time.
func ResetAt(sid string, now time.Time) (time.Time, string, bool) {
	a, _ := AbortInfo(sid)
	return resolveResetAt(a.Msg, a.At, capturedResets(), now)
}
