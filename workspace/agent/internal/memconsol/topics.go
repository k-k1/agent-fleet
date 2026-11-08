package memconsol

import (
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memengine"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

// Topic is a group of observations about one memory (or, for an exported proposal that names no
// existing memory, about the memory it proposed), with the suspect memories of that cluster.
// Grouping is deterministic: the target name, then the ranking of memory_search's scorer
// (memoryx.ReviewRank); no model decides what belongs together.
type Topic struct {
	ID        string
	Scope     string
	ProjectID string
	Memory    string // the primary memory's name (may not exist: an exported create)
	// Observations are every observation of the topic, oldest first, covered ones included.
	Observations []Observation
	// Memories are the primary memory (when it exists) and the few ranked next to it, which is
	// what a classification call is shown.
	Memories []memoryx.ConsolMemory
}

// topicID is stable for a scope and a primary memory name.
func topicID(scope, projectID, memory string) string {
	return obsID("topic", ScopeKey(scope, projectID), memory)
}

func findMem(ms []memoryx.ConsolMemory, name string) (memoryx.ConsolMemory, bool) {
	for _, m := range ms {
		if m.Name == name {
			return m, true
		}
	}
	return memoryx.ConsolMemory{}, false
}

func reviewEntries(ms []memoryx.ConsolMemory) []memoryx.ReviewEntry {
	out := make([]memoryx.ReviewEntry, len(ms))
	for i, m := range ms {
		out[i] = memoryx.ReviewEntry{Scope: "", Name: m.Name, Description: m.Description, Type: m.Type,
			Revision: m.Revision, Pinned: m.Pinned, Body: m.Body}
	}
	return out
}

// BuildTopics groups observations. An observation's topic is its suspect memory when that memory
// exists in the scope; an exported create that names a memory that does not exist is attached to
// the best-ranked existing memory when one shares words with it, and otherwise is a topic of its
// own. The result is ordered by topic id, so the same inputs always give the same topics.
func BuildTopics(obs []Observation, memories map[string][]memoryx.ConsolMemory) []Topic {
	byID := map[string]*Topic{}
	for _, o := range obs {
		key := ScopeKey(o.Scope, o.ProjectID)
		mems := memories[key]
		primary := o.Memory
		if _, ok := findMem(mems, primary); !ok && o.Source == SourceExport {
			if ranked := memoryx.ReviewRank(reviewEntries(mems), o.Memory+" "+o.Note); len(ranked) > 0 {
				primary = ranked[0].Name
			}
		}
		id := topicID(o.Scope, o.ProjectID, primary)
		t := byID[id]
		if t == nil {
			t = &Topic{ID: id, Scope: o.Scope, ProjectID: o.ProjectID, Memory: primary}
			byID[id] = t
		}
		t.Observations = append(t.Observations, o)
	}
	out := make([]Topic, 0, len(byID))
	for _, t := range byID {
		sort.SliceStable(t.Observations, func(i, j int) bool {
			a, b := t.Observations[i], t.Observations[j]
			if !a.FirstSeen.Equal(b.FirstSeen) {
				return a.FirstSeen.Before(b.FirstSeen)
			}
			return a.ID < b.ID
		})
		mems := memories[ScopeKey(t.Scope, t.ProjectID)]
		if m, ok := findMem(mems, t.Memory); ok {
			t.Memories = append(t.Memories, m)
		}
		var window []string
		for _, o := range t.Observations {
			window = append(window, o.Memory, o.Note)
		}
		for _, r := range memoryx.ReviewRank(reviewEntries(mems), strings.Join(window, " ")) {
			if len(t.Memories) >= memengine.MaxSnippets {
				break
			}
			if r.Name == t.Memory {
				continue
			}
			if m, ok := findMem(mems, r.Name); ok {
				t.Memories = append(t.Memories, m)
			}
		}
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Delay causes recorded in a run report and the status.
const (
	CauseBudget      = "budget"            // the run's token budget was spent
	CauseInterrupted = "interrupted"       // a turn started during the run; the rest was deferred
	CauseStopped     = "workspace-stopped" // the workspace was stopped when a run was due
	CauseNeverIdle   = "never-idle"        // no idle window came since the last run
	CauseWaiting     = "next-run"          // ripe, simply waiting for the next run
	CauseRunFailed   = "run-failed"        // the run ended on a model or runner error
)

// open reports whether an observation counts: it has not been looked at, or the memory it is about
// has changed since it was (then it is evidence about a text that is no longer the one judged),
// and the engine has not given up on it. memRev is the memory's current revision, 0 when it does
// not exist.
func open(st *ObsState, memRev int) bool {
	if st == nil {
		return true
	}
	if st.Dropped >= maxDropped {
		return false
	}
	if st.Covered == nil {
		return true
	}
	return memRev != st.Covered.Revision
}

// OpenObservations returns the topic's observations that count right now.
func (t Topic) OpenObservations(s *State) []Observation {
	var out []Observation
	for _, o := range t.Observations {
		rev := 0
		if m, ok := memoryRev(t, o.Memory); ok {
			rev = m
		}
		if open(s.Observations[o.ID], rev) {
			out = append(out, o)
		}
	}
	return out
}

// memoryRev is the current revision of the named memory when the topic carries it.
func memoryRev(t Topic, name string) (int, bool) {
	if m, ok := findMem(t.Memories, name); ok {
		return m.Revision, true
	}
	return 0, false
}

// Ripe applies decision 6: one wrong/stale report is enough, or Config.NewObservations open
// observations, or an open observation that has waited Config.MaxWait. A topic that already has a
// proposal waiting for the member is not ripe: it would only pile a second proposal on the first.
func Ripe(open []Observation, hasPending bool, c Config, now time.Time) bool {
	if hasPending || len(open) == 0 {
		return false
	}
	if len(open) >= c.NewObservations {
		return true
	}
	for _, o := range open {
		if o.IsReport() || now.Sub(o.FirstSeen) >= c.MaxWait {
			return true
		}
	}
	return false
}

// RipeTopic is a ripe topic with the observations a run would take and its cost estimate.
type RipeTopic struct {
	Topic    Topic
	Open     []Observation // all open observations
	Take     []Observation // the oldest MaxObservationsPerTopic of them
	Estimate int           // estimated tokens of the calls this topic costs
	Oldest   time.Time
}

// estimateTokens is bytes/3, the ADR's own scale for mixed Japanese/English text.
func estimateTokens(prompt string) int { return len(prompt)/3 + 1 }

// classifyRequest is the engine request for one observation of a topic. A snippet that fails the
// secret scan is left out whole.
func classifyRequest(t Topic, o Observation) memengine.ClassifyRequest {
	req := memengine.ClassifyRequest{Observation: observationText(o)}
	for _, m := range t.Memories {
		if memoryx.ReviewTextProblem(m.Name+"\n"+m.Description+"\n"+m.Body) != "" {
			continue
		}
		req.Memories = append(req.Memories, memengine.Snippet{Name: m.Name, Description: m.Description, Body: m.Body})
	}
	return req
}

func observationText(o Observation) string {
	var b strings.Builder
	switch o.Source {
	case SourceFeedback:
		b.WriteString("A reader reported the note `" + o.Memory + "` as " + o.Rule + ": ")
	case SourceLint:
		b.WriteString("A check on the note `" + o.Memory + "` flagged (" + o.Rule + "): ")
	default:
		b.WriteString("An earlier suggestion about `" + o.Memory + "` (" + o.Rule + ") was left unanswered: ")
	}
	b.WriteString(o.Note)
	if o.Candidate != nil {
		b.WriteString("\n\nThe suggested new note, " + o.Candidate.Name + ": " + o.Candidate.Description + "\n" + o.Candidate.Body)
	}
	return b.String()
}

// Select orders the ripe topics oldest observation first (a busy topic cannot starve a quiet one)
// and takes them while their estimates fit the budget. Topics that do not fit are returned as
// delayed with the cause, in the same order. A topic whose estimate alone is over the whole
// budget is delayed too (flagged), never run half-way.
func Select(ripe []RipeTopic, budget int) (run []RipeTopic, delayed []Delay) {
	sort.SliceStable(ripe, func(i, j int) bool {
		if !ripe[i].Oldest.Equal(ripe[j].Oldest) {
			return ripe[i].Oldest.Before(ripe[j].Oldest)
		}
		return ripe[i].Topic.ID < ripe[j].Topic.ID
	})
	spent := 0
	for _, r := range ripe {
		if spent+r.Estimate > budget {
			d := Delay{Topic: r.Topic.ID, Memory: r.Topic.Memory, Scope: ScopeKey(r.Topic.Scope, r.Topic.ProjectID), Cause: CauseBudget, Estimate: r.Estimate}
			if r.Estimate > budget {
				d.Detail = "the topic's estimate is over the whole per-run budget"
			}
			delayed = append(delayed, d)
			continue
		}
		spent += r.Estimate
		run = append(run, r)
	}
	return run, delayed
}

// Delay is one ripe topic a run did not take, and why.
type Delay struct {
	Topic    string `json:"topic"`
	Scope    string `json:"scope"`
	Memory   string `json:"memory"`
	Cause    string `json:"cause"`
	Detail   string `json:"detail,omitempty"`
	Estimate int    `json:"estimate,omitempty"`
}
