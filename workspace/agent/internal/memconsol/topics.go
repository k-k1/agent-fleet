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

// An exported proposal naming a memory that does not exist joins an existing memory's topic only
// when the best-ranked one is about the same thing: a score of attachMinScore or more (two name
// words, or one name word and two description words, or eight body words) of which attachMinHead
// comes from the name and description. Measured on the first live run: creates about a flaky
// contract test and about PR workflow were attached to notes that shared only body words ("under",
// "load", "branch") with them, and were then shown under that note's name.
const (
	attachMinScore = 8
	attachMinHead  = 4
)

// BuildTopics groups observations. An observation's topic is its suspect memory when that memory
// exists in the scope; an exported proposal (of any op) that names a memory that does not exist is
// attached to the best-ranked existing memory when it clears attachMinScore, and otherwise is a
// topic of its own, keyed by the name it proposed. The result is ordered by topic id, so the same
// inputs always give the same topics.
func BuildTopics(obs []Observation, memories map[string][]memoryx.ConsolMemory) []Topic {
	byID := map[string]*Topic{}
	for _, o := range obs {
		key := ScopeKey(o.Scope, o.ProjectID)
		mems := memories[key]
		primary := o.Memory
		if _, ok := findMem(mems, primary); !ok && o.Source == SourceExport {
			if ranked := memoryx.ReviewRankScored(reviewEntries(mems), o.Memory+" "+o.Note); len(ranked) > 0 &&
				ranked[0].Score >= attachMinScore && ranked[0].HeadScore >= attachMinHead {
				primary = ranked[0].Entry.Name
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
	// CauseTooLarge is terminal, not a delay: even one observation with the smallest excerpts and
	// one note costs more than the whole per-run budget (a patch must send the whole note), so no
	// run can take the topic until the memory shrinks or the budget is raised.
	CauseTooLarge = "too-large"
)

// open reports whether an observation counts: it has not been looked at, or the memory it is about
// has changed since it was (then it is evidence about a text that is no longer the one judged),
// and the engine has not given up on it. memRev is the memory's current revision, 0 when it does
// not exist.
func open(st *ObsState, memRev int) bool {
	if st == nil {
		return true
	}
	// The engine gave up on it at one revision; a changed memory is a new question.
	if st.Dropped >= maxDropped && memRev == st.DroppedRev {
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
	// Fit is what the classification calls show; TooLarge marks a topic that does not fit the
	// whole budget at the smallest fit.
	Fit      Fit
	TooLarge bool
}

// estimate is the tokens the calls of one topic cost at a fit: one classification per taken
// observation, and one patch call that sends the whole of the largest note shown (any shown note
// may be the patch target, and a patch must carry its whole text). A supersede or merge answer
// costs a second classification, which is not counted: it is the exception, and the call gate
// still stops a run that spends past its budget.
func estimate(t Topic, take []Observation, fit Fit) int {
	est := 0
	for _, o := range take {
		est += estimateTokens(classifyRequest(t, o, fit).Prompt()) + 200
	}
	shown := t.Memories
	if len(shown) > fit.Notes {
		shown = shown[:fit.Notes]
	}
	largest, found := memoryx.ConsolMemory{}, false
	for _, m := range shown {
		if !found || len(m.Body) > len(largest.Body) {
			largest, found = m, true
		}
	}
	if found {
		var note []string
		for _, o := range take {
			note = append(note, observationText(o))
		}
		preq := memengine.PatchRequest{Observation: strings.Join(note, "\n\n- "), Name: largest.Name, Description: largest.Description, Body: largest.Body}
		est += estimateTokens(preq.Prompt()) + 300
	}
	return est
}

// PlanTopic prices a ripe topic and, when it is over the whole budget, shrinks it. Every
// observation is kept while the context can still shrink (shorter excerpts, then fewer notes);
// only then is the newest observation dropped and the ladder walked again (dropped ones stay open
// for the next run), down to one observation with one note at 150 runes. A topic still over the budget there is TooLarge.
func PlanTopic(t Topic, open []Observation, c Config) RipeTopic {
	take := open
	if len(take) > c.MaxObservationsPerTopic {
		take = take[:c.MaxObservationsPerTopic]
	}
	r := RipeTopic{Topic: t, Open: open, Take: take, Oldest: open[0].FirstSeen, Fit: fullFit}
	r.Estimate = estimate(t, take, fullFit)
	if r.Estimate <= c.TokenBudget {
		return r
	}
	for n := len(take); n >= 1; n-- {
		for _, fit := range shrinkLadder() {
			if e := estimate(t, take[:n], fit); e <= c.TokenBudget {
				r.Take, r.Fit, r.Estimate = take[:n], fit, e
				return r
			}
		}
	}
	ladder := shrinkLadder()
	r.Take, r.Fit = take[:1], ladder[len(ladder)-1]
	r.Estimate, r.TooLarge = estimate(t, r.Take, r.Fit), true
	return r
}

// estimateTokens is bytes/3, the ADR's own scale for mixed Japanese/English text.
func estimateTokens(prompt string) int { return len(prompt)/3 + 1 }

// Fit is how much of a topic one classification call is shown: the first Notes of the topic's
// memories (the primary first), each cut to Runes of body. fullFit is what the engine sends anyway.
type Fit struct {
	Notes int
	Runes int
}

var fullFit = Fit{Notes: memengine.MaxSnippets, Runes: memengine.SnippetRunes}

// shrinkLadder lists the fits tried, largest first: shorter excerpts before fewer notes.
func shrinkLadder() []Fit {
	var out []Fit
	for _, n := range []int{memengine.MaxSnippets, 2, 1} {
		for _, r := range []int{memengine.SnippetRunes, 300, 150} {
			out = append(out, Fit{Notes: n, Runes: r})
		}
	}
	return out
}

func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// classifyRequest is the engine request for one observation of a topic. A snippet that fails the
// secret scan is left out whole.
func classifyRequest(t Topic, o Observation, fit Fit) memengine.ClassifyRequest {
	req := memengine.ClassifyRequest{Observation: observationText(o)}
	shown := t.Memories
	if len(shown) > fit.Notes {
		shown = shown[:fit.Notes]
	}
	for _, m := range shown {
		if memoryx.ReviewTextProblem(m.Name+"\n"+m.Description+"\n"+m.Body) != "" {
			continue
		}
		req.Memories = append(req.Memories, memengine.Snippet{Name: m.Name, Description: m.Description, Body: cutRunes(m.Body, fit.Runes)})
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
