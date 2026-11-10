package memconsol

// Obsolescence evidence (ADR 0130 decision 6, #3788). A note or an observation that names an
// issue or pull request is about something that may be finished. The Agent looks the state up and
// hands it to the classifier as a fact; the model never fetches anything and is not asked to
// guess. Every rule here exists so that a failed lookup can only make the Agent say less:
//
//   - a lookup that fails, times out, or is refused is "unknown" and is never shown, so a network
//     outage cannot make a note look obsolete;
//   - lookups are bounded per run (a cap, a short timeout each, and a stop after a few failures in
//     a row), and successful answers are cached, closed ones longer than open ones.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RefState is what a lookup found out about an issue or pull request.
type RefState string

// The states a lookup can answer. A lookup that learned nothing returns an error, never a state.
const (
	RefOpen   RefState = "open"
	RefClosed RefState = "closed"
	RefMerged RefState = "merged"
)

// Finished says the work behind the reference is over: the issue is closed or the PR merged
// (a PR closed without merging counts as closed, which is the state GitHub reports).
func (s RefState) Finished() bool { return s == RefClosed || s == RefMerged }

// RefLookup asks the forge for one issue or pull request. repo is "owner/name".
type RefLookup func(ctx context.Context, repo string, number int) (RefState, error)

const (
	refCapPerRun    = 24               // lookups one run may start (cache hits are free)
	refMaxPerText   = 8                // references read from one note or observation
	refCallTimeout  = 5 * time.Second  // one lookup
	refFailuresStop = 3                // consecutive failures after which the run looks nothing more up
	refFinishedTTL  = 6 * time.Hour    // closed and merged rarely change back
	refOpenTTL      = 15 * time.Minute // an open one may close at any time
)

// RefChecker is the Agent's lookup of issue and PR state, shared by the runs of one process. The
// zero value looks nothing up (every reference is unknown).
type RefChecker struct {
	Lookup RefLookup
	// RepoOf is the repository a bare "#12" means in a project's notes ("" when unknown).
	RepoOf func(projectID string) string
	// Now is the clock of the cache; nil means time.Now.
	Now func() time.Time

	mu    sync.Mutex
	cache map[refKey]refEntry
}

type refKey struct {
	repo string
	n    int
}

type refEntry struct {
	state RefState
	at    time.Time
}

func (c *RefChecker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Begin starts the lookups of one run, with the run's own cap.
func (c *RefChecker) Begin() *RefRun {
	return &RefRun{c: c, left: refCapPerRun}
}

// RefRun is the lookup budget of one run.
type RefRun struct {
	c        *RefChecker
	left     int
	failures int // in a row
	Lookups  int // lookups actually started
}

// State returns the state of one reference, or "" when it is unknown for any reason: no checker,
// no lookup wired, the cap spent, the run's lookups failing, or this lookup failing.
func (r *RefRun) State(ctx context.Context, repo string, n int) RefState {
	if r == nil || r.c == nil || r.c.Lookup == nil || n < 1 {
		return ""
	}
	k := refKey{strings.ToLower(repo), n}
	c := r.c
	c.mu.Lock()
	if e, ok := c.cache[k]; ok {
		ttl := refOpenTTL
		if e.state.Finished() {
			ttl = refFinishedTTL
		}
		if c.now().Sub(e.at) < ttl {
			c.mu.Unlock()
			return e.state
		}
		delete(c.cache, k)
	}
	c.mu.Unlock()
	if r.left <= 0 || r.failures >= refFailuresStop || ctx.Err() != nil {
		return ""
	}
	r.left--
	r.Lookups++
	cctx, cancel := context.WithTimeout(ctx, refCallTimeout)
	defer cancel()
	st, err := c.Lookup(cctx, k.repo, n)
	if err != nil || (st != RefOpen && st != RefClosed && st != RefMerged) {
		r.failures++
		return ""
	}
	r.failures = 0
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[refKey]refEntry{}
	}
	c.cache[k] = refEntry{st, c.now()}
	c.mu.Unlock()
	return st
}

var (
	refURLRe  = regexp.MustCompile(`https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/(?:issues|pull)/([1-9][0-9]{0,8})\b`)
	refFullRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_./-])([A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9_.-]+)#([1-9][0-9]{0,8})\b`)
	// A bare number is short on purpose: "#123456" is as likely a colour as an issue.
	refBareRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_&/#-])#([1-9][0-9]{0,4})\b`)
)

// extractRefs lists the issue and PR references of text in order of first appearance, at most
// refMaxPerText of them. A bare "#12" needs defaultRepo; without one it is ignored.
func extractRefs(text, defaultRepo string) []refKey {
	type hit struct {
		at int
		k  refKey
	}
	var hits []hit
	for _, m := range refURLRe.FindAllStringSubmatchIndex(text, -1) {
		hits = append(hits, hit{m[0], refKey{strings.ToLower(text[m[2]:m[3]]), atoiSafe(text[m[4]:m[5]])}})
	}
	for _, m := range refFullRe.FindAllStringSubmatchIndex(text, -1) {
		hits = append(hits, hit{m[2], refKey{strings.ToLower(text[m[2]:m[3]]), atoiSafe(text[m[4]:m[5]])}})
	}
	if defaultRepo != "" {
		for _, m := range refBareRe.FindAllStringSubmatchIndex(text, -1) {
			hits = append(hits, hit{m[2], refKey{strings.ToLower(defaultRepo), atoiSafe(text[m[2]:m[3]])}})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].at < hits[j].at })
	seen := map[refKey]bool{}
	var out []refKey
	for _, h := range hits {
		if h.k.n < 1 || seen[h.k] {
			continue
		}
		seen[h.k] = true
		out = append(out, h.k)
		if len(out) == refMaxPerText {
			break
		}
	}
	return out
}

func atoiSafe(s string) int { n, _ := strconv.Atoi(s); return n }

func (k refKey) String() string { return fmt.Sprintf("%s#%d", k.repo, k.n) }

// checked looks up every reference and returns the ones with a known state, in order, plus whether
// all of them were known (a note is called finished only when no reference of it is a guess).
func (r *RefRun) checked(ctx context.Context, refs []refKey) (known []checkedRef, all bool) {
	all = len(refs) > 0
	for _, k := range refs {
		if st := r.State(ctx, k.repo, k.n); st != "" {
			known = append(known, checkedRef{k, st})
		} else {
			all = false
		}
	}
	return known, all
}

type checkedRef struct {
	refKey
	state RefState
}

func refList(cs []checkedRef) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = c.String() + " " + string(c.state)
	}
	return strings.Join(parts, ", ")
}

// evidenceFor builds the checked facts of one classification call: what the observation names,
// whether an exported suggestion cites finished work, and what each shown note names. A fact with
// nothing known is left out.
func (r *RefRun) evidenceFor(ctx context.Context, t Topic, o Observation, shown map[string]string) []string {
	if r == nil || r.c == nil || r.c.Lookup == nil {
		return nil
	}
	def := ""
	if t.Scope == "project" && r.c.RepoOf != nil {
		def = r.c.RepoOf(t.ProjectID)
	}
	var out []string
	if known, _ := r.checked(ctx, extractRefs(observationText(o), def)); len(known) > 0 {
		out = append(out, "The observation names "+refList(known)+".")
		if o.Candidate != nil {
			cand, _ := r.checked(ctx, extractRefs(o.Candidate.Name+"\n"+o.Candidate.Description+"\n"+o.Candidate.Body, def))
			var done []checkedRef
			for _, c := range cand {
				if c.state.Finished() {
					done = append(done, c)
				}
			}
			if len(done) > 0 {
				out = append(out, "The suggested new note cites finished work ("+refList(done)+"). It was exported from an older review and may record an incident that is over: if it only tells readers how to deal with that work, answer no-op; add it only when its lesson still holds without it.")
			}
		}
	}
	names := make([]string, 0, len(shown))
	for n := range shown {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		known, all := r.checked(ctx, extractRefs(shown[n], def))
		switch {
		case len(known) == 0:
		case all && allFinished(known):
			out = append(out, "Note `"+n+"` names only finished work ("+refList(known)+").")
		default:
			out = append(out, "Note `"+n+"` names "+refList(known)+".")
		}
	}
	return out
}

func allFinished(cs []checkedRef) bool {
	for _, c := range cs {
		if !c.state.Finished() {
			return false
		}
	}
	return len(cs) > 0
}
