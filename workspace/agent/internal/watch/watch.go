// Package watch is the watch engine of ADR 0133: sessions subscribe to a subject held by an
// external provider (a pull request, later a ticket or a deployment), source adapters read the
// provider, and the engine schedules the reads, de-duplicates what they report and hands each
// new event to a sink once. The engine knows time; an adapter knows one provider.
//
// Everything here is provider-neutral. A source reports the conditions that hold now (level),
// with a Version that fingerprints the state behind each; the engine turns that into "new to
// this subscription" by remembering which (type, version) it already delivered. That is what
// lets a watch made after the checks finished still fire at once (ADR 0133 §5).
package watch

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// SubjectID names one watched thing across sources: "github:owner/repo#12". It is written into
// a notice header, so it is always a header token (TokenOK).
type SubjectID string

// Event is one condition a source saw. Summary is composed by the source from fields it
// controls (state, counts, numbers, the subject's reference); anything a third party wrote goes
// into Data and is rendered as quoted data (ADR 0133 rule 6.5).
type Event struct {
	Source  string     `json:"source"`
	Subject SubjectID  `json:"subject"`
	Type    string     `json:"type"`
	Version string     `json:"version"`
	Summary string     `json:"summary"`
	Data    []DataItem `json:"data,omitempty"`
	At      time.Time  `json:"at"`
	// Failed marks a pr.checks.completed whose result is a failure, so a session's watch on
	// completed checks is known to have told what a built-in's pr.checks.failed would.
	Failed bool `json:"failed,omitempty"`
	// Count is the number of unresolved review threads on a pr.review.threads_unresolved, so a
	// watch that told two threads stands in for a built-in's two and not for its three.
	Count int `json:"count,omitempty"`
}

// DataItem is one piece of provider text: a check name and its link, a reviewer's login.
type DataItem struct {
	Text string `json:"text"`
	URL  string `json:"url,omitempty"`
}

// Hint is the source's advice on when the subject is worth asking about again.
type Hint struct {
	After time.Duration
	// Settled marks a subject that will not change on its own any more (a merged PR); the
	// engine keeps reading it rarely, because a closed PR can be reopened.
	Settled bool
}

// Resolved is what Resolve made of a reference.
type Resolved struct {
	Subject SubjectID
	URL     string // the subject's web page, https only; "" when unknown
}

// Source is one provider's adapter.
type Source interface {
	Kind() string
	// Types are the event types this source can produce. watch refuses any other, so a watch
	// never waits for something its source cannot say (ADR 0133 §3).
	Types() []string
	// Resolve normalises a reference and checks that the member's own connection can read it.
	// It is the only place a source decides access. defaultRepo qualifies a bare "#12".
	Resolve(ctx context.Context, ref, defaultRepo string) (Resolved, error)
	// Poll reads one subject. cursor is what the previous Poll returned for it (nil the first
	// time); the events are every condition that holds now. want are the type patterns the
	// subject's subscriptions ask for, so a source can skip a costly read nobody needs.
	Poll(ctx context.Context, subject SubjectID, cursor json.RawMessage, want []string) ([]Event, json.RawMessage, Hint, error)
}

// ErrRefused is a Resolve answer the caller should show as is: the reference is malformed, the
// subject does not exist, or the member's connection cannot read it.
type ErrRefused struct{ Reason string }

func (e *ErrRefused) Error() string { return e.Reason }

// ErrRateLimited makes the engine leave the source alone until Until.
type ErrRateLimited struct{ Until time.Time }

func (e *ErrRateLimited) Error() string {
	return "rate limited until " + e.Until.UTC().Format(time.RFC3339)
}

// Until says when a subscription ends besides its deadline.
const (
	UntilFirst    = "first"    // after the first delivered event (the default)
	UntilDeadline = "deadline" // only at its deadline or with its owner
)

// Limits and defaults of ADR 0133 rule 6.2 / 6.3.
const (
	MaxPerOwner     = 20
	MaxTotal        = 200
	DefaultLifetime = 7 * 24 * time.Hour
	MaxLifetime     = 30 * 24 * time.Hour
)

// Subscription is one watch a session asked for.
type Subscription struct {
	ID      string    `json:"id"`
	Owner   string    `json:"owner"` // the session that owns the watch and receives its notices
	Source  string    `json:"source"`
	Subject SubjectID `json:"subject"`
	Ref     string    `json:"ref"` // the reference as the session gave it, for list_watches
	// Requester is the session that asked when it is not the owner: a child that subscribed
	// with notify parent. The subscription belongs to the owner (ADR 0133 §5).
	Requester string    `json:"requester,omitempty"`
	URL       string    `json:"url,omitempty"`
	Types     []string  `json:"types"`
	Until     string    `json:"until"`
	Created   time.Time `json:"created"`
	Expires   time.Time `json:"expires"`
	// Told is the version last delivered per event type. Sources report only what holds now,
	// so the last version per type is all de-duplication needs, and it cannot grow.
	Told map[string]string `json:"told,omitempty"`
	// Pending are matched events not yet delivered, at most one per type (the newest state
	// replaces an older one that was never delivered, ADR 0133 §5).
	Pending []Event `json:"pending,omitempty"`
	// Done marks a first-match subscription whose notice is being injected. It is claimed on
	// disk before the injection, so a crash in between loses that notice instead of repeating
	// it; a load drops it.
	Done bool `json:"done,omitempty"`

	// Built-in subscriptions only (builtin.go); a session's watch leaves them empty.
	// Builtin is the purpose that keeps the subscription.
	Builtin string `json:"builtin,omitempty"`
	// Notify is NotifyMember for one delivered to the member; Fallback NotifyMember sends to the
	// member when the owner is archived or takes no prompt.
	Notify   string `json:"notify,omitempty"`
	Fallback string `json:"fallback,omitempty"`
	// Max caps the deliveries, Sent counts them; SentAt is the last one into the owner session.
	Max    int       `json:"max,omitempty"`
	Sent   int       `json:"sent,omitempty"`
	SentAt time.Time `json:"sent_at,omitempty"`
	// Combine delivers every pending event in one notice.
	Combine bool `json:"combine,omitempty"`
	// Baseline types take the first observed state as told; Observed marks that it was seen.
	Baseline []string `json:"baseline,omitempty"`
	Observed bool     `json:"observed,omitempty"`
	// Tag is the feature's own key for the subject, for the sink.
	Tag string `json:"tag,omitempty"`
}

// tokenRE is the header token set of ADR 0133 §5: nothing in it can close a notice header or
// start a new line.
var tokenRE = regexp.MustCompile(`^[A-Za-z0-9.:/#_-]{1,200}$`)

// TokenOK reports whether s may be written into a notice header.
func TokenOK(s string) bool { return tokenRE.MatchString(s) }

// typeRE is an event type pattern: "pr.checks.completed", "pr.checks.*", "pr.*".
var typeRE = regexp.MustCompile(`^[a-z][a-z_]*(\.[a-z][a-z_]*)*(\.\*)?$`)

// MatchType reports whether pattern selects typ. A pattern ends either in a name or in ".*".
func MatchType(pattern, typ string) bool {
	if p, ok := strings.CutSuffix(pattern, ".*"); ok {
		return strings.HasPrefix(typ, p+".")
	}
	return pattern == typ
}

// checkTypes refuses patterns that are malformed or select nothing the source can produce.
func checkTypes(patterns, available []string) error {
	if len(patterns) == 0 {
		return &ErrRefused{"types is required, e.g. [\"pr.checks.completed\"]; this source offers " + strings.Join(available, ", ")}
	}
	if len(patterns) > 20 {
		return &ErrRefused{"at most 20 types"}
	}
	for _, p := range patterns {
		if !typeRE.MatchString(p) {
			return &ErrRefused{"bad type " + quoteShort(p) + ": use a name such as pr.checks.completed, or a prefix such as pr.checks.*"}
		}
		ok := false
		for _, t := range available {
			if MatchType(p, t) {
				ok = true
				break
			}
		}
		if !ok {
			return &ErrRefused{"this source cannot report " + quoteShort(p) + "; it offers " + strings.Join(available, ", ")}
		}
	}
	return nil
}

func quoteShort(s string) string {
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60]) + "…"
	}
	b, _ := json.Marshal(s)
	return string(b)
}
