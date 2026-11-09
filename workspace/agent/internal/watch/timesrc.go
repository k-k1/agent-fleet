package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The time source of ADR 0133 §7: a timer that lives in the engine and fires time.fired through
// the same sinks as any other event. It reads no provider and needs no connection.
//
// The due instant is part of the subject ("time:20261109T150000Z", UTC), so two watches for the
// same second share one read and nothing about the timer has to be kept beside the subscription.
// Poll reports the condition that holds now, as every source does: once the instant has passed,
// time.fired holds, and the engine's once-per-version record keeps a restart from telling it
// twice.

// TimeEvent is the one type the time source produces.
const TimeEvent = "time.fired"

const (
	timeSubjectPrefix = "time:"
	timeSubjectLayout = "20060102T150405Z"
	// timeLateAfter is how far past its instant a timer may fire before the summary says so. The
	// engine ticks every few seconds and a wake takes a minute or more, so shorter is no news.
	timeLateAfter = 2 * time.Minute
	// timeMaxPoll bounds the gap between two reads of a timer that is not due yet. The read costs
	// nothing, and a short gap is what keeps a wall clock that jumped (a suspended host, a clock
	// step) from delaying a timer by the whole wait.
	timeMaxPoll = time.Minute
)

// Time is the time source. Now is replaceable by tests.
type Time struct {
	Now func() time.Time
}

func (t *Time) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Time) Kind() string    { return "time" }
func (t *Time) Types() []string { return []string{TimeEvent} }

// Resolve reads ref as an instant: an RFC 3339 time with an offset or Z ("2026-11-09T15:00:00+09:00"),
// or a wait from now ("in 20m", "2h30m", "3d"). A time of day without a date and zone is refused:
// the engine keeps UTC, and guessing the member's zone would fire at the wrong hour.
func (t *Time) Resolve(_ context.Context, ref, _ string) (Resolved, error) {
	now := t.now()
	at, err := parseWhen(ref, now)
	if err != nil {
		return Resolved{}, err
	}
	// Whole seconds, rounded up: the subject names the instant to the second, and a timer must
	// never fire before the time that was asked for.
	if at = at.UTC(); at.Nanosecond() != 0 {
		at = at.Truncate(time.Second).Add(time.Second)
	}
	if !at.After(now) {
		return Resolved{}, &ErrRefused{"that time has already passed (" + at.UTC().Format(time.RFC3339) + ")"}
	}
	if at.After(now.Add(MaxLifetime)) {
		return Resolved{}, &ErrRefused{fmt.Sprintf("a timer can be at most %d days ahead", int(MaxLifetime/(24*time.Hour)))}
	}
	return Resolved{Subject: timeSubject(at), At: at.UTC()}, nil
}

// Poll answers for one timer: nothing until its instant, time.fired from then on.
func (t *Time) Poll(_ context.Context, subject SubjectID, _ json.RawMessage, _ []string) ([]Event, json.RawMessage, Hint, error) {
	due, err := timeSubjectInstant(subject)
	if err != nil {
		return nil, nil, Hint{}, err
	}
	now := t.now()
	if now.Before(due) {
		return nil, nil, Hint{After: min(due.Sub(now), timeMaxPoll)}, nil
	}
	summary := "timer due " + due.UTC().Format(time.RFC3339)
	if late := now.Sub(due); late >= timeLateAfter {
		summary += fmt.Sprintf(" (fired %d min late: the workspace was stopped or busy)", int(late/time.Minute))
	}
	ev := Event{Source: "time", Subject: subject, Type: TimeEvent, Version: due.UTC().Format(timeSubjectLayout), Summary: summary, At: due.UTC()}
	return []Event{ev}, nil, Hint{Settled: true, After: time.Hour}, nil
}

func timeSubject(at time.Time) SubjectID {
	return SubjectID(timeSubjectPrefix + at.UTC().Format(timeSubjectLayout))
}

func timeSubjectInstant(s SubjectID) (time.Time, error) {
	rest, ok := strings.CutPrefix(string(s), timeSubjectPrefix)
	if !ok {
		return time.Time{}, fmt.Errorf("not a time subject: %q", s)
	}
	return time.ParseInLocation(timeSubjectLayout, rest, time.UTC)
}

// parseWhen understands the two forms Resolve documents.
func parseWhen(ref string, now time.Time) (time.Time, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return time.Time{}, &ErrRefused{"ref is required: an RFC 3339 time such as 2026-11-09T15:00:00+09:00, or a wait such as \"in 20m\""}
	}
	if at, err := time.Parse(time.RFC3339, ref); err == nil {
		return at, nil
	}
	if d, ok := parseWait(ref); ok {
		return now.Add(d), nil
	}
	return time.Time{}, &ErrRefused{"cannot read " + quoteShort(ref) + " as a time: use an RFC 3339 time with an offset such as 2026-11-09T15:00:00+09:00, or a wait such as \"in 20m\", \"2h30m\" or \"3d\""}
}

// parseWait reads "in 20m", "20m", "1h30m", "3d" (days of 24 h, optionally with a Go duration
// after them: "1d12h"). It accepts only a positive wait.
func parseWait(s string) (time.Duration, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "in "))
	var days time.Duration
	if i := strings.IndexByte(s, 'd'); i > 0 {
		var n int
		if _, err := fmt.Sscanf(s[:i], "%d", &n); err != nil || fmt.Sprint(n) != s[:i] || n <= 0 || n > 400 {
			return 0, false
		}
		days = time.Duration(n) * 24 * time.Hour
		s = s[i+1:]
	}
	var rest time.Duration
	if s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return 0, false
		}
		rest = d
	} else if days == 0 {
		return 0, false
	}
	return days + rest, true
}
