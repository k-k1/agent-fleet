package codex

import (
	"strconv"
	"strings"
)

// NewestTierModel returns the newest "<prefix><version>-<tier>" id in ids — for codex,
// newestTierModel(ids, "gpt-", "luna") picks gpt-6-luna over gpt-5.6-luna. OpenAI keeps the tier
// names (astra / sol / terra / luna) across generations and moves only the version, so this
// follows a new generation without a source edit. "" when no id has that shape.
func NewestTierModel(ids []string, prefix, tier string) string {
	best, bestVer := "", []int(nil)
	for _, id := range ids {
		ver, ok := strings.CutPrefix(id, prefix)
		if !ok {
			continue
		}
		if ver, ok = strings.CutSuffix(ver, "-"+tier); !ok {
			continue
		}
		v, ok := parseVersion(ver)
		if !ok {
			continue
		}
		if best == "" || compareVersion(v, bestVer) > 0 {
			best, bestVer = id, v
		}
	}
	return best
}

// parseVersion reads "6" / "5.6" / "5.6.1"; anything else (a date, a word) is not a version.
func parseVersion(s string) ([]int, bool) {
	parts := strings.Split(s, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

// compareVersion compares numerically, a missing component counting as 0 (6 == 6.0 < 6.1).
func compareVersion(a, b []int) int {
	for i := 0; i < max(len(a), len(b)); i++ {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
