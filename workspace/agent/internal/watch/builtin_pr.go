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
}

// PRConditions are the conditions that hold now for one PR the session list's caches answered,
// in the event vocabulary, with versions that keep the notices of the features they replace:
//   - pr.checks.failed once per head commit, and only when every check of it finished, so the
//     notice names every failure;
//   - pr.review.changes_requested once each time a change request is made anew;
//   - pr.review.threads_unresolved each time the unresolved threads rise;
//   - pr.conflict once per conflict episode;
//   - pr.ready once per head commit;
//   - pr.merged / pr.closed once per close time, so a PR reopened and closed again is news.
//
// The summaries are composed from fields the cache controls; check names are provider text and
// go into Data (rule 6.5).
func PRConditions(subject SubjectID, pr *branchpr.PR, raw json.RawMessage, now time.Time) ([]Event, json.RawMessage) {
	var cur PRCursor
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cur)
	}
	if pr == nil {
		return nil, raw
	}
	open := pr.State == "open"
	cr := open && pr.ChangesRequested
	if cr && !cur.CRWas {
		cur.CREpisode++
	}
	cur.CRWas = cr
	conflict := open && pr.Conflict
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

	source := sourceOf(subject)
	var events []Event
	ev := func(typ, version, summary string, data []DataItem) {
		events = append(events, Event{Source: source, Subject: subject, Type: typ, Version: version, Summary: summary, Data: data, At: now.UTC()})
	}
	switch pr.State {
	case "merged":
		ev("pr.merged", pr.ClosedAt, "The PR was merged.", nil)
	case "closed":
		ev("pr.closed", pr.ClosedAt, "The PR was closed without merging.", nil)
	case "open":
		if pr.Checks == "failure" && !pr.ChecksPending && pr.Head != "" {
			data := make([]DataItem, 0, len(pr.FailingChecks))
			for _, c := range pr.FailingChecks {
				data = append(data, DataItem{Text: c.Name, URL: c.URL})
			}
			ev(typeChecksFailed, pr.Head, "CI failed on head commit "+shortHead(pr.Head)+".", data)
		}
		if cr {
			ev("pr.review.changes_requested", strconv.Itoa(cur.CREpisode), "A reviewer requested changes.", nil)
		}
		if threads > 0 {
			ev("pr.review.threads_unresolved", strconv.Itoa(cur.ThreadRise), fmt.Sprintf("%d review thread(s) are unresolved.", threads), nil)
		}
		if conflict {
			ev("pr.conflict", strconv.Itoa(cur.ConflictEpisode), "The PR has a merge conflict with its base.", nil)
		}
		if pr.Ready() && pr.Head != "" {
			ev("pr.ready", pr.Head, "The PR is ready to merge.", nil)
		}
	}
	out, _ := json.Marshal(cur)
	return events, out
}

// shortHead is the 12-character head the PR feedback notice has always shown.
func shortHead(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
