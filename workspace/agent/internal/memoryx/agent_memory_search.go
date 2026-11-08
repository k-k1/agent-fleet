package memoryx

import (
	"net/http"
	"sort"
	"strings"
)

// Field weights of one matched query word: a word in the name says more about what a memory is
// than one in the description, and that more than one somewhere in the body.
const (
	agentMemWeightName = 4
	agentMemWeightDesc = 2
	agentMemWeightBody = 1

	// agentMemFallbackAny is the Fallback value of an answer built from memories that hold only
	// some of the query words.
	agentMemFallbackAny = "any"
)

// agentMemSearchTerms splits a query into distinct lowercase words. Matching stays substring,
// so a Japanese query written without spaces is one word that still matches.
func agentMemSearchTerms(query string) []string {
	var terms []string
	seen := map[string]bool{}
	for _, t := range strings.Fields(strings.ToLower(query)) {
		if !seen[t] {
			seen[t] = true
			terms = append(terms, t)
		}
	}
	return terms
}

// agentMemScored is a candidate memory with what the ranking needs.
type agentMemScored struct {
	e       agentMemEntry
	matched []string
	score   int
	src     agentMemSearchSrc
}

// agentMemScore matches the words against one entry: which words occur anywhere, and the sum of
// each word's best field weight.
func agentMemScore(e agentMemEntry, terms []string) agentMemScored {
	name, desc, body := strings.ToLower(e.Name), strings.ToLower(e.Description), strings.ToLower(e.Body)
	s := agentMemScored{e: e}
	for _, t := range terms {
		w := 0
		switch {
		case strings.Contains(name, t):
			w = agentMemWeightName
		case strings.Contains(desc, t):
			w = agentMemWeightDesc
		case strings.Contains(body, t):
			w = agentMemWeightBody
		}
		if w > 0 {
			s.matched = append(s.matched, t)
			s.score += w
		}
	}
	return s
}

// agentMemSearch is agentMemSearchRanked without the fallback marker.
func agentMemSearch(c agentMemCaller, query string, limit int) ([]agentMemHit, error) {
	hits, _, err := agentMemSearchRanked(c, query, limit)
	return hits, err
}

// agentMemSearchRanked finds the memories holding every whitespace-separated word of the query
// (case-insensitive substrings of name, description and body) and orders them by relevance:
// more matched words, then heavier fields (name over description over body), then pinned, most
// used and newest. When no memory holds every word of a multi-word query it answers with the
// memories holding some of them, ranked the same way, and returns fallback "any". No index: a
// pass over one project's files is what ADR 0108 measured as enough, and the ranking is a pure
// function of the entries, so equal input gives equal order.
func agentMemSearchRanked(c agentMemCaller, query string, limit int) (hits []agentMemHit, fallback string, err error) {
	terms := agentMemSearchTerms(query)
	if len(terms) == 0 {
		return nil, "", memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "query is required")
	}
	srcs := make([]agentMemSearchSrc, 0, 2)
	for _, scope := range agentMemScopes(c) {
		srcs = append(srcs, agentMemSearchSrc{c: c, scope: scope})
	}
	agentMemWarmScan(c)
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	found, fallback, err := agentMemRankLocked(srcs, c.Kind, terms, limit, true)
	if err != nil {
		return nil, "", err
	}
	hits = make([]agentMemHit, 0, len(found))
	for _, s := range found {
		hits = append(hits, agentMemHitOf(s, fallback))
	}
	// Counted after the limit, under the read lock: a hit the caller never saw was not used, and
	// a forget or re-create cannot slip between the search and the count.
	for _, s := range found {
		agentMemRecordUses(s.src.c, agentMemCounterHits, s.e)
	}
	return hits, fallback, nil
}

// agentMemSearchSrc is one scope directory to search and the caller it is resolved for.
type agentMemSearchSrc struct {
	c     agentMemCaller
	scope string
}

// agentMemWarmSrcs is agentMemWarmScan for an arbitrary set of scope directories.
func agentMemWarmSrcs(srcs []agentMemSearchSrc) {
	for _, src := range srcs {
		_, _, _ = agentMemLoadScope(src.scope, src.c)
	}
}

// agentMemRankLocked scores and orders the memories of srcs (the caller holds agentMemMu). It
// only reads: counting a use is the caller's choice, so a Console search can share the ranking
// without feeding it. Each result keeps the src it came from.
func agentMemRankLocked(srcs []agentMemSearchSrc, kind string, terms []string, limit int, forAgent bool) (found []agentMemScored, fallback string, err error) {
	if limit <= 0 {
		limit = agentMemSearchDefault
	}
	limit = min(limit, agentMemSearchMax)
	var all, partial []agentMemScored
	for _, src := range srcs {
		es, _, lerr := agentMemLoadScope(src.scope, src.c)
		if lerr != nil {
			return nil, "", lerr
		}
		if rel, derr := agentMemScopeDir(src.scope, src.c); derr == nil {
			agentMemSetUses(es, rel)
		}
		for _, e := range es {
			if !agentMemAppliesTo(e, kind) || forAgent && agentMemExcluded(e) {
				continue
			}
			s := agentMemScore(e, terms)
			s.src = src
			switch {
			case len(s.matched) == len(terms):
				all = append(all, s)
			case len(s.matched) > 0:
				partial = append(partial, s)
			}
		}
	}
	found = all
	if len(found) == 0 && len(terms) > 1 {
		found, fallback = partial, agentMemFallbackAny
	}
	sort.SliceStable(found, func(i, j int) bool {
		a, b := found[i], found[j]
		if len(a.matched) != len(b.matched) {
			return len(a.matched) > len(b.matched)
		}
		if a.score != b.score {
			return a.score > b.score
		}
		if a.e.Pinned != b.e.Pinned {
			return a.e.Pinned
		}
		if a.e.Uses != b.e.Uses {
			return a.e.Uses > b.e.Uses
		}
		return a.e.Updated > b.e.Updated
	})
	if len(found) > limit {
		found = found[:limit]
	}
	return found, fallback, nil
}

// agentMemHitOf is the answer row of a scored entry.
func agentMemHitOf(s agentMemScored, fallback string) agentMemHit {
	h := agentMemHit{agentMemEntry: s.e, Snippets: agentMemSnippets(s.e.Body, s.matched)}
	if fallback != "" {
		h.MatchedTerms = s.matched
	}
	h.Body, h.Uses = "", 0 // Uses is ranking input, not part of the answer (as in the index)
	return h
}

// agentMemSnippets returns the first body lines that contain any of the matched words.
func agentMemSnippets(body string, terms []string) []string {
	out := []string{}
	for _, line := range strings.Split(body, "\n") {
		low := strings.ToLower(line)
		for _, t := range terms {
			if strings.Contains(low, t) {
				out = append(out, agentMemTruncate(strings.TrimSpace(line), agentMemSnippetChars))
				break
			}
		}
		if len(out) >= agentMemSnippetLines {
			break
		}
	}
	return out
}
