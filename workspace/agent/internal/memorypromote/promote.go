// Package memorypromote picks the memories that have proven themselves and are worth proposing to
// the project's own documents (ADR 0130 decision 11). It decides from signals only — the memory's
// kind, who confirmed which revision, open reasons, anchors and their lint result, and how long the
// text has stood — never from a model's taste. It writes nothing and proposes no text: it only
// says which memories qualify and, for the ones of a promotable kind that do not, what is missing.
//
// It imports no other Agent Fleet package (TestImportsNoAFPackage), like memorylint: the engine of
// ADR 0130 decision 8 is meant to move to its own module.
package memorypromote

import (
	"fmt"
	"sort"
	"time"
)

const (
	// DefaultMinSessions is decision 11's "at least two different sessions".
	DefaultMinSessions = 2
	// DefaultQuiet is how long the current revision must have stood. The record says "some
	// days"; a week keeps a text that is still being corrected out of the list.
	DefaultQuiet = 7 * 24 * time.Hour
)

// Verdict of a confirmation record that counts; the other verdicts never confirm.
const VerdictConfirmed = "confirmed"

// Rules a held memory can miss, as they appear in Miss.Rule.
const (
	RuleAboutMember  = "about-member"     // 0108's type says it is about the member, not the repository
	RuleOpenReasons  = "open-reasons"     // it is suspect
	RuleDisputed     = "disputed"         // a session reported the current revision stale or wrong
	RuleNoAnchors    = "no-anchors"       // nothing ties it to the repository
	RuleLint         = "lint"             // lint reports something about it (a missing anchor, a retired term, its size)
	RuleUnverified   = "unverified"       // the default branch or the retired-term list could not be read, so nothing is checked
	RuleAnchor       = "anchor-not-found" // a stored anchor was not found on the default branch
	RuleMoved        = "moved"            // it changed while it was being checked
	RuleUnpublished  = "unpublished"      // the file differs from its last published change
	RuleConfirmation = "confirmations"    // fewer distinct sessions confirmed the current revision than needed
	RuleRecent       = "changed-recently" // the current revision is younger than the quiet period
)

// Kinds whose knowledge can live in a document. A state expires and a log is history, so neither
// is ever proposed.
var promotable = map[string]bool{"lesson": true, "procedure": true, "preference": true}

// Promotable says whether a memory kind is one decision 11 considers.
func Promotable(kind string) bool { return promotable[kind] }

// Confirmation is one feedback record: which session reported which revision, and how.
type Confirmation struct {
	Session  string
	Revision int
	Verdict  string
}

// Memory is one memory of a repository's scope as the selection sees it.
type Memory struct {
	Name     string
	Kind     string // decision 3's kind: preference, lesson, procedure, state, log
	Type     string // 0108's type: user (about the member), feedback, project, reference
	Revision int
	// Updated is when the current revision was written; zero when it is unknown.
	Updated time.Time
	Anchors []string
	// Reasons is how many open reasons it carries.
	Reasons int
	// Excluded is set for an archived or superseded memory: agents no longer see it, so it is
	// never a candidate and is not listed at all.
	Excluded bool
	Reports  []Confirmation
	// Findings are the lint check ids that fired for this memory on the default branch.
	Findings []string
	// Missing are anchors that were not found on the default branch (memorylint.VerifyAnchors).
	Missing []string
	// Also are misses the caller found itself (RuleMoved, RuleUnpublished).
	Also []Miss
}

// Options tunes a run. Now is required; the zero value of the rest means the defaults.
type Options struct {
	Now         time.Time
	MinSessions int
	Quiet       time.Duration
	// Unverified, when set, says why the default branch or the retired-term list could not be
	// read. Then no memory qualifies: an anchor nobody checked is not one that exists.
	Unverified string
}

// Candidate is a memory that qualifies.
type Candidate struct {
	Name     string   `json:"name"`
	Revision int      `json:"revision"`
	Kind     string   `json:"kind"`
	Sessions int      `json:"sessions"`
	Anchors  []string `json:"anchors"`
	Updated  string   `json:"updated"`
}

// Miss is one requirement a held memory does not meet.
type Miss struct {
	Rule   string `json:"rule"`
	Detail string `json:"detail,omitempty"`
}

// Held is a memory of a promotable kind that does not qualify yet, with everything it misses.
type Held struct {
	Name     string `json:"name"`
	Revision int    `json:"revision"`
	Kind     string `json:"kind"`
	Missing  []Miss `json:"missing"`
}

// Result is the outcome of one run: candidates by the number of confirming sessions, most first,
// then by name; held memories by name.
type Result struct {
	Candidates []Candidate `json:"candidates"`
	Held       []Held      `json:"held"`
}

// Sessions counts the distinct sessions that confirmed exactly revision rev. A session that
// confirmed several revisions counts once for each, and a confirmation of another revision does
// not count for this one.
func Sessions(reports []Confirmation, rev int) int {
	seen := map[string]bool{}
	for _, r := range reports {
		if r.Verdict == VerdictConfirmed && r.Revision == rev && r.Session != "" {
			seen[r.Session] = true
		}
	}
	return len(seen)
}

// Disputed counts the distinct sessions that reported exactly revision rev as stale or wrong. The
// record is kept even when the reason it should have added was never published (the commit
// failed after the record was written, or the reason was removed outside AF), so it is read
// directly rather than trusted to show up as an open reason. A new revision starts clean.
func Disputed(reports []Confirmation, rev int) int {
	seen := map[string]bool{}
	for _, r := range reports {
		if r.Verdict != VerdictConfirmed && r.Revision == rev && r.Session != "" {
			seen[r.Session] = true
		}
	}
	return len(seen)
}

// Select applies decision 11's signals to every memory.
func Select(mems []Memory, opts Options) Result {
	minSessions := opts.MinSessions
	if minSessions < DefaultMinSessions {
		minSessions = DefaultMinSessions
	}
	quiet := opts.Quiet
	if quiet <= 0 {
		quiet = DefaultQuiet
	}
	res := Result{Candidates: []Candidate{}, Held: []Held{}}
	for _, m := range mems {
		if m.Excluded || !Promotable(m.Kind) {
			continue
		}
		var miss []Miss
		if m.Type == "user" {
			miss = append(miss, Miss{Rule: RuleAboutMember})
		}
		if m.Reasons > 0 {
			miss = append(miss, Miss{Rule: RuleOpenReasons, Detail: fmt.Sprintf("%d open", m.Reasons)})
		}
		if d := Disputed(m.Reports, m.Revision); d > 0 {
			miss = append(miss, Miss{Rule: RuleDisputed, Detail: fmt.Sprintf("%d session(s) on revision %d", d, m.Revision)})
		}
		if len(m.Anchors) == 0 {
			miss = append(miss, Miss{Rule: RuleNoAnchors})
		}
		if opts.Unverified != "" {
			miss = append(miss, Miss{Rule: RuleUnverified, Detail: opts.Unverified})
		} else {
			for _, a := range m.Missing {
				miss = append(miss, Miss{Rule: RuleAnchor, Detail: a})
			}
		}
		for _, f := range dedupe(m.Findings) {
			miss = append(miss, Miss{Rule: RuleLint, Detail: f})
		}
		miss = append(miss, m.Also...)
		n := Sessions(m.Reports, m.Revision)
		if n < minSessions {
			miss = append(miss, Miss{Rule: RuleConfirmation, Detail: fmt.Sprintf("%d of %d sessions on revision %d", n, minSessions, m.Revision)})
		}
		switch {
		case m.Updated.IsZero():
			miss = append(miss, Miss{Rule: RuleRecent, Detail: "when it last changed is unknown"})
		case opts.Now.Sub(m.Updated) < quiet:
			miss = append(miss, Miss{Rule: RuleRecent, Detail: "changed " + m.Updated.UTC().Format(time.RFC3339)})
		}
		if len(miss) > 0 {
			res.Held = append(res.Held, Held{Name: m.Name, Revision: m.Revision, Kind: m.Kind, Missing: miss})
			continue
		}
		res.Candidates = append(res.Candidates, Candidate{
			Name: m.Name, Revision: m.Revision, Kind: m.Kind, Sessions: n,
			Anchors: append([]string(nil), m.Anchors...), Updated: m.Updated.UTC().Format(time.RFC3339),
		})
	}
	sort.SliceStable(res.Candidates, func(i, j int) bool {
		a, b := res.Candidates[i], res.Candidates[j]
		if a.Sessions != b.Sessions {
			return a.Sessions > b.Sessions
		}
		return a.Name < b.Name
	})
	sort.SliceStable(res.Held, func(i, j int) bool { return res.Held[i].Name < res.Held[j].Name })
	return res
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
