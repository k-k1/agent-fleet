package memoryx

// The budgeted memory_index (ADR 0108 decision 5): the Agent decides what fits, so the MCP
// tool, any other client and the tests all see the same answer.

import (
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// agentMemIndexBudgetDefault is the rendered size of the described part, in the range of
	// claude's own MEMORY.md load limit (25 KB).
	agentMemIndexBudgetDefault = 24 << 10
	agentMemIndexBudgetMin     = 4 << 10
	agentMemIndexBudgetMax     = 64 << 10
	// agentMemIndexTailBudget bounds the abbreviated names of what did not fit; it is separate
	// so a small described budget does not also shrink the safety net of names.
	agentMemIndexTailBudget = 8 << 10
	// agentMemIndexTailScan bounds the quadratic grouping below; whatever lies past it is counted.
	agentMemIndexTailScan = 2000
	// agentMemIndexDescRunes is the description length in an index line only; memory_read and
	// memory_search keep the full text.
	agentMemIndexDescRunes = 80
	agentMemIndexNameBytes = 32
)

// agentMemClampBudget maps a caller's budget to the allowed range; 0 (absent) is the default.
func agentMemClampBudget(b int) int {
	if b <= 0 {
		return agentMemIndexBudgetDefault
	}
	return min(max(b, agentMemIndexBudgetMin), agentMemIndexBudgetMax)
}

// agentMemCutRunes cuts s to n runes with "…" at a rune boundary; a shorter s is unchanged.
func agentMemCutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for ; n > 0; n-- {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return s[:i] + "…"
}

// agentMemAbbrevName cuts a name to agentMemIndexNameBytes with "…". Names are ASCII slugs, but
// the cut still backs up to a rune boundary rather than assume it.
func agentMemAbbrevName(n string) string {
	if len(n) <= agentMemIndexNameBytes {
		return n
	}
	i := agentMemIndexNameBytes
	for i > 0 && !utf8.RuneStart(n[i]) {
		i--
	}
	return n[:i] + "…"
}

// agentMemIndexLine is the line memory_index prints for a described entry. The MCP formatter
// (mcpx.mcpMemoryFormatIndex) prints the same text; both packages pin it with the same literal,
// because the budget is measured on this string.
func agentMemIndexLine(e agentMemEntry) string {
	var tags []string
	if e.Type != "" {
		tags = append(tags, e.Type)
	}
	if len(e.Kinds) > 0 {
		tags = append(tags, "for "+strings.Join(e.Kinds, ","))
	}
	if len(e.Updated) >= 10 {
		tags = append(tags, e.Updated[:10])
	}
	t := ""
	if len(tags) > 0 {
		t = " (" + strings.Join(tags, "; ") + ")"
	}
	return "- [" + e.Scope + "] " + e.Name + " — " + e.Description + t + "\n"
}

// agentMemRankTier puts behavioural guidance (feedback, user) ahead of everything else.
func agentMemRankTier(e agentMemEntry) int {
	if e.Type == "feedback" || e.Type == "user" {
		return 0
	}
	return 1
}

// agentMemRank orders entries for the index: tier first, then newest first. Stable, so the
// caller's scope order (project before user) breaks ties.
func agentMemRank(es []agentMemEntry) {
	sort.SliceStable(es, func(i, j int) bool {
		if ti, tj := agentMemRankTier(es[i]), agentMemRankTier(es[j]); ti != tj {
			return ti < tj
		}
		return es[i].Updated > es[j].Updated
	})
}

// agentMemGroupNames renders names grouped by their first hyphen segment, `adr-{a,b}`, a lone
// member whole. Groups and members are sorted so the output is stable.
func agentMemGroupNames(names []string) []string {
	groups := map[string][]string{}
	for _, n := range names {
		p, rest, ok := strings.Cut(n, "-")
		if !ok {
			groups[n] = append(groups[n], "")
			continue
		}
		groups[p] = append(groups[p], rest)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		rests := groups[k]
		sort.Strings(rests)
		if len(rests) == 1 {
			if rests[0] == "" {
				out = append(out, k)
			} else {
				out = append(out, k+"-"+rests[0])
			}
			continue
		}
		var b strings.Builder
		b.WriteString(k + "-{")
		for i, r := range rests {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(r)
		}
		b.WriteByte('}')
		out = append(out, b.String())
	}
	return out
}

// agentMemTailSize is the rendered size of grouped tokens as the formatter joins them (one space).
func agentMemTailSize(groups []string) int {
	n := 0
	for _, g := range groups {
		n += len(g) + 1
	}
	return n
}

// agentMemBudgetIndex splits ranked entries into the described part (rendered lines within
// budget, in rank order, stopping at the first that does not fit so rank is never skipped),
// the grouped names of what follows (within the tail budget) and the count beyond that.
func agentMemBudgetIndex(ranked []agentMemEntry, budget int) (described []agentMemEntry, more []string, omitted int) {
	budget = agentMemClampBudget(budget)
	used, i := 0, 0
	described = []agentMemEntry{}
	for ; i < len(ranked); i++ {
		e := ranked[i]
		e.Description = agentMemCutRunes(e.Description, agentMemIndexDescRunes)
		n := len(agentMemIndexLine(e))
		if used+n > budget {
			break
		}
		used += n
		described = append(described, e)
	}
	rest := ranked[i:]
	if len(rest) == 0 {
		return described, nil, 0
	}
	var names []string
	seen := map[string]bool{}
	taken := 0
	for _, e := range rest {
		if taken >= agentMemIndexTailScan {
			break
		}
		n := agentMemAbbrevName(e.Name)
		if seen[n] {
			taken++
			continue
		}
		cand := agentMemGroupNames(append(append([]string(nil), names...), n))
		if agentMemTailSize(cand) > agentMemIndexTailBudget {
			break
		}
		seen[n] = true
		names = append(names, n)
		more = cand
		taken++
	}
	return described, more, len(rest) - taken
}
