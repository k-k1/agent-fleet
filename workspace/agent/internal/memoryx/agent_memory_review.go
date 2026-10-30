package memoryx

// The store-facing half of the background memory review (ADR 0117): what the review is allowed to
// read from AF memory, how a string is judged before it goes to a model or into a queue, and
// whether a proposed memory is acceptable to the store. The review engine itself (internal/
// memreview) calls no unexported helper of this package; everything it needs is here, and none of
// it writes.

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"unicode"
)

// The values of the memoryReview setting. Anything else reads as off. "auto" is not a value yet:
// it arrives with the budgets (ADR 0117 phase P3) and reads as off until then.
const (
	MemoryReviewOff     = "off"
	MemoryReviewPropose = "propose"
)

// ReviewTextProblem says why s must not be sent to a model or stored by the review, "" when it
// may. It is the input side's redaction rule (ADR 0117 decision 2): the 0022 scanner reports a
// masked hint per line and stops at 20 findings, so it cannot be used to cut secrets out of a
// text, only to judge one. A string is therefore used whole or not at all: any finding, a NUL
// byte, or the scanner's cap discards it. The answer names a rule or a reason, never a value.
func ReviewTextProblem(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		return "nul-byte"
	}
	fs := memoryScanContent("review", []byte(s))
	if len(fs) == 0 {
		return ""
	}
	if len(fs) >= memorySecretMaxPerFile {
		return "scan-cap" // a 21st secret would have been unreported; the string is not used
	}
	return fs[0].Rule
}

// ReviewEntry is one memory as the review sees it.
type ReviewEntry struct {
	Scope       string
	Name        string
	Description string
	Type        string
	Kinds       []string
	Revision    int
	Updated     string
	Pinned      bool
	Body        string
}

// IndexLine is the line memory_index prints for the entry, so the review's budget is measured on
// the same text a session sees.
func (e ReviewEntry) IndexLine() string {
	return agentMemIndexLine(agentMemEntry{Scope: e.Scope, Name: e.Name, Description: e.Description,
		Type: e.Type, Kinds: e.Kinds, Updated: e.Updated})
}

// ReviewSnapshot is the memory a reviewed session could see.
type ReviewSnapshot struct {
	Session string
	Kind    string
	// HasProject is false for a session outside any working copy: only user scope exists for it.
	// ProjectID names the project (the store's own id) so a proposal can be applied later without
	// the session.
	HasProject bool
	ProjectID  string
	// Entries are in memory_index order: pinned, then guidance types, then most used, then newest.
	Entries []ReviewEntry
	// Withheld counts files left out because they failed the store's own scan or could not be read.
	Withheld int
}

// ReviewSnapshotFor reads the memory of the project (and user scope) of the named session,
// filtered by the session's kind like the index is. It counts no use: a review reading a memory
// must not make it look popular. Files the store withholds are not returned.
func ReviewSnapshotFor(sessionName string) (ReviewSnapshot, error) {
	c, err := agentMemResolveCaller(sessionName)
	if err != nil {
		return ReviewSnapshot{}, err
	}
	if c.Session == agentMemUnknown {
		return ReviewSnapshot{}, errors.New("a review needs a named session")
	}
	agentMemWarmScan(c)
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	out := ReviewSnapshot{Session: c.Session, Kind: c.Kind, HasProject: c.Project != nil}
	if c.Project != nil {
		out.ProjectID = c.Project.ID
	}
	var all []agentMemEntry
	for _, scope := range agentMemScopes(c) {
		es, withheld, err := agentMemLoadScope(scope, c)
		if err != nil {
			return ReviewSnapshot{}, err
		}
		out.Withheld += withheld
		if rel, err := agentMemScopeDir(scope, c); err == nil {
			agentMemSetUses(es, rel)
		}
		for _, e := range es {
			if agentMemAppliesTo(e, c.Kind) {
				all = append(all, e)
			}
		}
	}
	agentMemRank(all)
	for _, e := range all {
		out.Entries = append(out.Entries, ReviewEntry{Scope: e.Scope, Name: e.Name, Description: e.Description,
			Type: e.Type, Kinds: e.Kinds, Revision: e.Revision, Updated: e.Updated, Pinned: e.Pinned, Body: e.Body})
	}
	return out, nil
}

// ValidateReviewMemory applies the store's own limits (ADR 0108 decision 3) to a memory a review
// proposes, without writing. The error names the field and the rule, never the value, so it can
// be logged and shown. Scope is "project" or "user".
func ValidateReviewMemory(scope, name, description, memType string, kinds []string, body string) error {
	if scope != agentMemScopeProject && scope != agentMemScopeUser {
		return errors.New("scope must be project or user")
	}
	req := agentMemSaveReq{Scope: scope, Name: name, Description: description, Type: memType, Kinds: kinds, Body: body}
	if err := agentMemValidate(&req); err != nil {
		return errors.New(userMessage(err))
	}
	// An empty type is legal for the store (imports) but a proposal states what it is.
	if req.Type == "" {
		return errors.New("type is required")
	}
	return nil
}

// ValidReviewName says whether name may name a memory.
func ValidReviewName(name string) bool { return agentMemValidName(name) }

// userMessage is the message of a store refusal; an error of another shape gets a fixed text so
// that nothing unexpected is echoed.
func userMessage(err error) string {
	var ue *memoryUserErr
	if errors.As(err, &ue) && ue.Status < http.StatusInternalServerError {
		return ue.Msg
	}
	return "the memory was refused by the store"
}

// Bounds of the term set ReviewRank builds from a window.
const (
	reviewTermCandidates = 200
	reviewTermsMax       = 64
	// reviewUbiquity: a term found in more than this share of the memories separates none of them
	// from the rest ("that", a particle, the project's own name).
	reviewUbiquity = 0.5
)

// ReviewRank returns the entries that share words with window, best first: the store's
// memory_search scorer (agentMemScore, with the same field weights - a word in the name outweighs
// one in the description, and that one in the body) over terms taken from the window, ordered by
// matched terms, then score. An entry that matches only some terms counts, as in the search's
// any-term fallback; ties keep the order of entries, which is the index order (pinned, guidance,
// most used, newest). A deterministic function of its input; it reads nothing and counts no use.
func ReviewRank(entries []ReviewEntry, window string) []ReviewEntry {
	terms := reviewTerms(entries, window)
	if len(terms) == 0 {
		return nil
	}
	type hit struct {
		e       ReviewEntry
		matched int
		score   int
	}
	var hits []hit
	for _, e := range entries {
		sc := agentMemScore(agentMemEntry{Name: e.Name, Description: e.Description, Body: e.Body}, terms)
		if len(sc.matched) > 0 {
			hits = append(hits, hit{e, len(sc.matched), sc.score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].matched != hits[j].matched {
			return hits[i].matched > hits[j].matched
		}
		return hits[i].score > hits[j].score
	})
	out := make([]ReviewEntry, len(hits))
	for i, h := range hits {
		out[i] = h.e
	}
	return out
}

// reviewTerms picks the words of window worth matching: lower-cased runs of letters and digits of
// at least four characters, plus overlapping pairs of characters inside a run of Han, hiragana or
// katakana (Japanese has no spaces to split on). The most frequent come first, those that occur in
// most memories are dropped, and the rest is capped.
func reviewTerms(entries []ReviewEntry, window string) []string {
	freq := map[string]int{}
	var order []string
	add := func(t string) {
		if freq[t] == 0 {
			order = append(order, t)
		}
		freq[t]++
	}
	for _, tok := range strings.FieldsFunc(strings.ToLower(window), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		rs := []rune(tok)
		for i := 0; i < len(rs); {
			if isCJK(rs[i]) {
				j := i
				for j < len(rs) && isCJK(rs[j]) {
					j++
				}
				for k := i; k+1 < j; k++ {
					add(string(rs[k : k+2]))
				}
				i = j
				continue
			}
			j := i
			for j < len(rs) && !isCJK(rs[j]) {
				j++
			}
			if j-i >= 4 {
				add(string(rs[i:j]))
			}
			i = j
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return freq[order[i]] > freq[order[j]] })
	if len(order) > reviewTermCandidates {
		order = order[:reviewTermCandidates]
	}
	if len(entries) >= 10 {
		texts := make([]string, len(entries))
		for i, e := range entries {
			texts[i] = strings.ToLower(e.Name + "\n" + e.Description + "\n" + e.Body)
		}
		kept := order[:0:0]
		for _, t := range order {
			n := 0
			for _, tx := range texts {
				if strings.Contains(tx, t) {
					n++
				}
			}
			if float64(n) <= reviewUbiquity*float64(len(entries)) {
				kept = append(kept, t)
			}
		}
		order = kept
	}
	if len(order) > reviewTermsMax {
		order = order[:reviewTermsMax]
	}
	return order
}

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana)
}
