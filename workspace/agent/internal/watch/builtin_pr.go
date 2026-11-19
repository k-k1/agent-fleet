package watch

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
)

// PRCursor is what PRConditions keeps about one PR between observations. Neither provider names
// a change request, a conflict or a rise in unresolved threads, so each is counted here and the
// count is the event's version (ADR 0133 §5).
type PRCursor struct {
	CREpisode       int  `json:"cr_episode,omitempty"`
	CRWas           bool `json:"cr_was,omitempty"`
	ConflictEpisode int  `json:"conflict_episode,omitempty"`
	ConflictWas     bool `json:"conflict_was,omitempty"`
	// ThreadRise counts the times the unresolved threads rose; Threads is the last count seen. A
	// fall is no news, a rise after one is: two threads, one resolved, a new one is news again.
	ThreadRise int `json:"thread_rise,omitempty"`
	Threads    int `json:"threads,omitempty"`
	// Closed is the state of the close in progress ("merged", "closed", "" while open) and
	// ClosedAt the close time first seen for it: the version of pr.merged / pr.closed. Bitbucket
	// has no close time and reports its last update instead, which a comment on a merged PR
	// moves; pinned here, one close is one event.
	// ChecksPending is whether the last observation of the open PR saw checks still running: a
	// CI notice is on its way then, which is what keeps an idle owner waiting (Engine.holdsOwner).
	// Head is the head commit that observation was about and ChecksAt when it last saw the
	// checks running; a silent cache leaves them as they were, and holdsOwner stops waiting on a
	// running state older than checksPendingTTL.
	ChecksPending bool   `json:"checks_pending,omitempty"`
	Head          string `json:"head,omitempty"`
	// HeadAt is when Head was first seen: the optimistic hold of a head with no check reported
	// lasts checksUnreportedGrace from it, so a repository without CI is let go.
	HeadAt   time.Time `json:"head_at,omitempty"`
	ChecksAt time.Time `json:"checks_at,omitempty"`

	Closed   string `json:"closed,omitempty"`
	ClosedAt string `json:"closed_at,omitempty"`
}

// PRConditions are the conditions that hold now for one PR the session list's caches answered,
// in the event vocabulary, with versions that keep the notices of the features they replace:
//   - pr.checks.failing once per head commit, from the first failed check while others still run;
//   - pr.checks.failed once per head commit, and only when every check of it finished, so the
//     notice names every failure;
//   - pr.review.changes_requested once each time a change request is made anew;
//   - pr.review.threads_unresolved each time the unresolved threads rise;
//   - pr.conflict once per conflict episode;
//   - pr.ready once per head commit;
//   - pr.merged / pr.closed once per close, versioned by the close time first seen for it, so a
//     PR reopened and closed again is news and a later update to a closed PR is not.
//
// The summaries are composed from fields the cache controls; check names are provider text and
// go into Data (rule 6.5).
// A nil pr (the cache has not answered) is no observation: ok is false, and the caller's
// ObserveBuiltin then changes nothing, so a baseline is not taken from an unanswered read.
func PRConditions(subject SubjectID, pr *branchpr.PR, raw json.RawMessage, now time.Time) (events []Event, next json.RawMessage, ok bool) {
	var cur PRCursor
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cur)
	}
	if pr == nil {
		return nil, raw, false
	}
	open := pr.State == "open"
	cr := open && pr.ChangesRequested
	if cr && !cur.CRWas {
		cur.CREpisode++
	}
	cur.CRWas = cr
	conflict := open && pr.Conflict
	if open && pr.Mergeable == "UNKNOWN" {
		// UNKNOWN is no observation of the conflict (GitHub answers it for a while after the base
		// moves, a failed Bitbucket read does too), so the last state holds. Ending the episode
		// here would start a new one on the next answering read and repeat the notice after every
		// merge to the base.
		conflict = cur.ConflictWas
	}
	if conflict && !cur.ConflictWas {
		cur.ConflictEpisode++
	}
	cur.ConflictWas = conflict
	threads := 0
	if open {
		threads = pr.UnresolvedThreads
	}
	if threads > cur.Threads {
		cur.ThreadRise++
	}
	cur.Threads = threads
	switch pr.State {
	case "merged", "closed":
		if cur.Closed != pr.State {
			cur.Closed, cur.ClosedAt = pr.State, pr.ClosedAt
		}
	default:
		cur.Closed, cur.ClosedAt = "", ""
	}

	if pr.Head != "" && pr.Head != cur.Head {
		cur.HeadAt = now.UTC()
	}
	cur.Head = pr.Head
	cur.ChecksPending, cur.ChecksAt = cursorChecksRunning(cur, pr, open, now)
	source := sourceOf(subject)
	ev := func(typ, version, summary string, data []DataItem) {
		events = append(events, Event{Source: source, Subject: subject, Type: typ, Version: version, Summary: summary, Data: data, At: now.UTC()})
	}
	switch pr.State {
	case "merged":
		ev("pr.merged", cur.ClosedAt, "The PR was merged.", nil)
	case "closed":
		ev("pr.closed", cur.ClosedAt, "The PR was closed without merging.", nil)
	case "open":
		if pr.Checks == "failure" && pr.Head != "" {
			data := make([]DataItem, 0, len(pr.FailingChecks))
			for _, c := range pr.FailingChecks {
				data = append(data, DataItem{Text: c.Name, URL: c.URL})
			}
			if !pr.ChecksPending {
				ev(typeChecksFailed, pr.Head, "CI failed on head commit "+shortHead(pr.Head)+".", data)
			} else if len(data) > 0 {
				ev(typeChecksFailing, pr.Head, "CI is failing on head commit "+shortHead(pr.Head)+", other checks still run.", data)
			}
		}
		if cr {
			ev("pr.review.changes_requested", strconv.Itoa(cur.CREpisode), "A reviewer requested changes.", nil)
		}
		if threads > 0 {
			ev("pr.review.threads_unresolved", strconv.Itoa(cur.ThreadRise), fmt.Sprintf("%d review thread(s) are unresolved.", threads), nil)
			events[len(events)-1].Count = threads
		}
		if conflict {
			ev("pr.conflict", strconv.Itoa(cur.ConflictEpisode), "The PR has a merge conflict with its base.", nil)
		}
		if pr.Ready() && pr.Head != "" {
			ev("pr.ready", pr.Head, "The PR is ready to merge.", nil)
		}
	}
	out, _ := json.Marshal(cur)
	return events, out, true
}

// shortHead is the 12-character head the PR feedback notice has always shown.
func shortHead(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

const (
	// checksPendingTTL bounds how long an unrefreshed "checks running" holds an idle owner.
	checksPendingTTL = 30 * time.Minute
	// checksRunningRefresh is how often an unchanged running state is re-stamped: a stamp per
	// one-minute sweep would rewrite the state file every sweep, and the slowest PR cache
	// answers every five minutes.
	checksRunningRefresh = 5 * time.Minute
	// checksUnreportedGrace is how long a head with no check reported yet is taken as running:
	// the slowest cache (5 minutes) plus the Control Plane's two closing reads. GitHub reports no
	// checks at all for a commit of a repository without CI, which must not hold for ever.
	checksUnreportedGrace = 15 * time.Minute
)

// cursorChecksRunning decides whether a result is still on its way for the open PR. Running
// checks say so. A head first seen less than checksUnreportedGrace ago with no check reported
// counts too: a push whose checks are not created yet must not read as settled, or the owner is
// let go before the result and nobody is left to tell. Known limits, kept on purpose: a stale
// cache still showing the old head as finished cannot be told from a settled PR, and a
// provider-confirmed running head whose read goes empty is released after the grace.
func cursorChecksRunning(cur PRCursor, pr *branchpr.PR, open bool, now time.Time) (bool, time.Time) {
	if !open {
		return false, time.Time{}
	}
	running := pr.ChecksPending ||
		(pr.Head != "" && pr.Checks == "" && now.Sub(cur.HeadAt) < checksUnreportedGrace)
	if !running {
		return false, time.Time{}
	}
	if cur.ChecksPending && now.Sub(cur.ChecksAt) < checksRunningRefresh {
		return true, cur.ChecksAt
	}
	return true, now.UTC()
}
