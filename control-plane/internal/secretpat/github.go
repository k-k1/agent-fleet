// Package secretpat holds the credential shapes that more than one detector or redactor in
// its module matches on, so a vendor changing a token's shape is fixed in one place.
// control-plane/internal/secretpat and workspace/agent/internal/secretpat are byte-identical
// copies, because the two modules cannot import each other; secretpat_test.go compares them.
package secretpat

import (
	"regexp"
	"sort"
	"strings"
)

// GitHubStatelessPattern matches a stateless token whole: `ghs_APPID_JWT` as GitHub documents
// it (about 520 characters, `.`, `-` and `_` in the body), and the same without the APPID.
//
// It is held to that structure rather than to GitHub's looser `ghs_[A-Za-z0-9._-]{36,}`, which
// also takes ordinary names such as `ghs_status_dashboard_for_enterprise_repositories.md`.
// With a numeric APPID the JWT is any three base64url segments, so no particular header bytes
// are assumed; without one, the header must start as a JSON object does (`eyJ`), the only
// thing that tells it from a name. The minimums sit below the smallest real JWT (a 19-character
// `{"alg":"none"}` header, a 43-character HS256 signature).
//
// It has no leading `\b`: the structure is specific enough on its own, and a boundary would
// miss a token glued to a word (`cache_ghs_…`) and leave it all in clear.
const GitHubStatelessPattern = `gh[pousr]_(?:[0-9]+_[A-Za-z0-9_-]{16,}|eyJ[A-Za-z0-9_-]{13,})\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{32,}`

// GitHubClassicPattern is the classic token shape the detectors have always used: `gh?_` and
// at least 36 alphanumerics, between word boundaries. The trailing boundary is what keeps a
// name like `ghs_<36 letters>_enabled.md` out; do not drop it.
const GitHubClassicPattern = `\bgh[pousr]_[A-Za-z0-9]{36,}\b`

var (
	// GitHubToken matches a GitHub token of either shape, for detectors (they only ask
	// whether, never where).
	GitHubToken = regexp.MustCompile(`(?:` + GitHubStatelessPattern + `|` + GitHubClassicPattern + `)`)
	// GitHubStateless matches the stateless shape only; redactors add it beside their own
	// classic rule and replace through RedactAll.
	GitHubStateless = regexp.MustCompile(GitHubStatelessPattern)
)

// RedactAll replaces every match of every re in s with mark. Each re is matched against s as
// given, and overlapping or touching matches become one replacement. Replacing one pattern's
// matches before matching the next would let the first cut a later match short and leave its
// rest in clear: a JWT whose signature holds a GitHub-token-shaped run, a Bearer value that
// starts with one, a classic token running straight into a stateless one.
func RedactAll(s, mark string, res ...*regexp.Regexp) string {
	var spans [][2]int
	for _, re := range res {
		for _, m := range re.FindAllStringIndex(s, -1) {
			if m[1] > m[0] {
				spans = append(spans, [2]int{m[0], m[1]})
			}
		}
	}
	if len(spans) == 0 {
		return s
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	var b strings.Builder
	last := 0
	for i := 0; i < len(spans); {
		from, to := spans[i][0], spans[i][1]
		for i++; i < len(spans) && spans[i][0] <= to; i++ {
			to = max(to, spans[i][1])
		}
		b.WriteString(s[last:from])
		b.WriteString(mark)
		last = to
	}
	b.WriteString(s[last:])
	return b.String()
}
