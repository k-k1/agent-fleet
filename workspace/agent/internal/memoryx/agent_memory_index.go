package memoryx

// The budgeted memory_index (ADR 0108 decision 5): the Agent decides what fits, so the MCP
// tool, any other client and the tests all see the same answer.

import (
	"encoding/json"
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
	// agentMemIndexTailOverhead is reserved out of the tail budget for what the formatter adds
	// around the names: the explanatory header line, the "and N more" line and the pinned count. The mcpx test
	// measures the real tail against the full budget.
	agentMemIndexTailOverhead = 320
	// agentMemIndexTailScan bounds the quadratic grouping below; whatever lies past it is counted.
	agentMemIndexTailScan = 2000
	// agentMemIndexDescRunes is the description length in an index line only; memory_read and
	// memory_search keep the full text.
	agentMemIndexDescRunes = 80
	agentMemIndexNameBytes = 32
)

// agentMemClampBudget maps a caller's budget to the allowed range; only 0 (absent) is the
// default, so a negative value is raised to the minimum like any other too-small one.
func agentMemClampBudget(b int) int {
	if b == 0 {
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

// agentMemRank orders entries for the index: pinned first, then tier, then most used, then
// newest. Stable, so the caller's scope order (project before user) breaks ties.
func agentMemRank(es []agentMemEntry) {
	sort.SliceStable(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if a.Pinned != b.Pinned {
			return a.Pinned
		}
		if ti, tj := agentMemRankTier(a), agentMemRankTier(b); ti != tj {
			return ti < tj
		}
		if a.Uses != b.Uses {
			return a.Uses > b.Uses
		}
		return a.Updated > b.Updated
	})
}

// agentMemGroupNames renders names grouped by their first hyphen segment, `adr-{a,b}`. A name
// without a hyphen and a lone hyphenated member are printed whole, so no name is ever rewritten
// into another. Tokens are sorted so the output is stable.
func agentMemGroupNames(names []string) []string {
	groups := map[string][]string{}
	var out []string
	for _, n := range names {
		p, _, ok := strings.Cut(n, "-")
		if !ok {
			out = append(out, n)
			continue
		}
		groups[p] = append(groups[p], n)
	}
	for p, ms := range groups {
		if len(ms) == 1 {
			out = append(out, ms[0])
			continue
		}
		sort.Strings(ms)
		var b strings.Builder
		b.WriteString(p + "-{")
		for i, m := range ms {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(m[len(p)+1:])
		}
		b.WriteByte('}')
		out = append(out, b.String())
	}
	sort.Strings(out)
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
//
// Pins rank first, so they fill the described part before anything else. The budget still
// holds: when the pins alone exceed it, the ones that do not fit fall to the names-only tail
// like any other entry, and pinnedOmitted says how many (the member's cue to unpin some).
// Measured on lines, not on a pin count, because one pinned memory can be as long as ten.
func agentMemBudgetIndex(ranked []agentMemEntry, budget int) (described []agentMemEntry, more []string, omitted, pinnedOmitted int) {
	budget = agentMemClampBudget(budget)
	used, i := 0, 0
	described = []agentMemEntry{}
	for ; i < len(ranked); i++ {
		e := ranked[i]
		e.Pinned, e.Uses = false, 0 // ranking input, not part of the answer
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
		return described, nil, 0, 0
	}
	for _, e := range rest {
		if e.Pinned {
			pinnedOmitted++
		}
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
		if agentMemTailSize(cand) > agentMemIndexTailBudget-agentMemIndexTailOverhead {
			break
		}
		seen[n] = true
		names = append(names, n)
		more = cand
		taken++
	}
	return described, more, len(rest) - taken, pinnedOmitted
}

// IndexJSONFor is memory_index's answer for a working copy and an agent kind, as the JSON the
// route would send, for a caller that has no session to name: lcpp builds its system prompt per
// session in-process (ADR 0108 decision 5). It honours the same budget clamp and the same secret
// scan; the switch is the caller's to check.
func IndexJSONFor(dir, kind string, budget int) (string, error) {
	c := agentMemCaller{Session: agentMemUnknown, Kind: kind, Project: agentMemProjectFor(dir)}
	out, err := agentMemListIndex(c, budget)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(out)
	return string(b), err
}
