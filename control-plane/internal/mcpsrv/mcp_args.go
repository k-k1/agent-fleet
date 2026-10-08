package mcpsrv

import (
	"fmt"
	"sort"
	"strings"
)

// unknownArgsError holds a tools/call to the properties its tool advertised. Without it a
// misspelt key (create_session with `prompt` for `initial_prompt`) is read as absent and the
// call succeeds with no task. "" when every key is advertised, or when the schema lists no
// properties to hold the caller to.
func unknownArgsError(tool mcpTool, args map[string]any) string {
	props, ok := tool.schema["properties"].(map[string]any)
	if !ok {
		return ""
	}
	var bad []string
	for k := range args {
		if _, known := props[k]; !known {
			bad = append(bad, k)
		}
	}
	sort.Strings(bad)
	msgs := make([]string, 0, len(bad))
	for _, k := range bad {
		m := fmt.Sprintf("unknown argument %q for %s", k, tool.name)
		if guess := closestProp(k, props); guess != "" {
			m += fmt.Sprintf(" (did you mean %q?)", guess)
		}
		msgs = append(msgs, m)
	}
	return strings.Join(msgs, "; ")
}

// closestProp suggests the property a misspelt key most likely meant: one that contains it or
// is contained by it (`prompt` -> `initial_prompt`), else the nearest by edit distance when
// that is small. "" when nothing is an obvious match.
func closestProp(key string, props map[string]any) string {
	names := make([]string, 0, len(props))
	for p := range props {
		names = append(names, p)
	}
	sort.Strings(names)
	lk := strings.ToLower(key)
	best, bestScore := "", 1<<30
	for _, p := range names {
		lp := strings.ToLower(p)
		score := -1
		if len(lk) >= 3 && strings.Contains(lp, lk) || len(lp) >= 3 && strings.Contains(lk, lp) {
			score = max(len(lp)-len(lk), len(lk)-len(lp))
		} else if d := editDistance(lk, lp); d <= 2 && d*3 <= len(lk) {
			score = d
		}
		if score >= 0 && score < bestScore {
			best, bestScore = p, score
		}
	}
	return best
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}
