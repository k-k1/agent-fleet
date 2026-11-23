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

// githubStatelessHead is a stateless token up to its JWT header's first characters: `gh?_`,
// an optional numeric APPID, and the start of a base64url JSON object: `{` (`e` then `y` or
// `w`) or one whitespace byte and `{` (`IH`, `CX`, `Cn`, `DX`). That start is what tells a
// token from a name with an APPID-like number and dotted parts
// (`ghs_202610_releasechecklist.productionrollout.<digest>.md`); more than one leading
// whitespace byte is not covered.
const githubStatelessHead = `gh[pousr]_(?:[0-9]+_)?(?:e[yw]|IH|CX|Cn|DX)`

// GitHubStatelessPattern matches a stateless token whole: `ghs_APPID_JWT` as GitHub documents
// it (about 520 characters, `.`, `-` and `_` in the body), and the same without the APPID. The
// minimums sit below the smallest real JWT (a 19-character `{"alg":"none"}` header, a
// 43-character HS256 signature).
//
// It is held to that structure rather than to GitHub's looser `ghs_[A-Za-z0-9._-]{36,}`, which
// also takes ordinary names such as `ghs_status_dashboard_for_enterprise_repositories.md`. It
// has no leading `\b`: the structure is specific enough on its own, and a boundary would miss a
// token glued to a word (`cache_ghs_…`) and leave it all in clear.
const GitHubStatelessPattern = githubStatelessHead + `[A-Za-z0-9_-]{14,}\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{32,}`

// GitHubStatelessRedactPattern is the redactors' form: a stateless token's head and the whole
// run of base64url characters and inner dots after it. It also takes a token cut short (the
// audit keeps a command's first 80 characters) and tokens run together, which the whole-token
// pattern would stop short in or miss; a redactor errs towards masking a little more.
const GitHubStatelessRedactPattern = githubStatelessHead + `[A-Za-z0-9_-]{6,}(?:\.?[A-Za-z0-9_-])*`

// GitHubClassicPattern is the classic token shape the detectors have always used: `gh?_` and
// at least 36 alphanumerics, between word boundaries. The trailing boundary is what keeps a
// name like `ghs_<36 letters>_enabled.md` out; do not drop it.
const GitHubClassicPattern = `\bgh[pousr]_[A-Za-z0-9]{36,}\b`

var (
	// GitHubToken matches a GitHub token of either shape, for detectors (they only ask
	// whether, never where).
	GitHubToken = regexp.MustCompile(`(?:` + GitHubStatelessPattern + `|` + GitHubClassicPattern + `)`)
	// GitHubStateless matches the stateless shape only, whole.
	GitHubStateless = regexp.MustCompile(GitHubStatelessPattern)
	// GitHubStatelessRedact is GitHubStatelessRedactPattern; redactors add it beside their own
	// classic rule and replace through RedactAll.
	GitHubStatelessRedact = regexp.MustCompile(GitHubStatelessRedactPattern)
)

// RedactAll replaces every match of every re in s with mark. Each re is matched against s as
// given, and overlapping or touching matches become one replacement. Replacing one pattern's
// matches before matching the next would let the first cut a later match short and leave its
// rest in clear: a JWT whose signature holds a GitHub-token-shaped run, a Bearer value that
// starts with one, a classic token running straight into a stateless one.
func RedactAll(s, mark string, res ...*regexp.Regexp) string {
	return ReplaceSpans(s, mark, Spans(s, res...))
}

// Spans lists every match of every re in s, as [start, end) byte offsets.
func Spans(s string, res ...*regexp.Regexp) [][2]int {
	var spans [][2]int
	for _, re := range res {
		for _, m := range re.FindAllStringIndex(s, -1) {
			spans = append(spans, [2]int{m[0], m[1]})
		}
	}
	return spans
}

// ReplaceSpans replaces each span of s with mark, merging overlapping or touching spans into
// one replacement first. Empty spans are ignored.
func ReplaceSpans(s, mark string, spans [][2]int) string {
	live := spans[:0:0]
	for _, sp := range spans {
		if sp[1] > sp[0] {
			live = append(live, sp)
		}
	}
	if len(live) == 0 {
		return s
	}
	sort.Slice(live, func(i, j int) bool { return live[i][0] < live[j][0] })
	var b strings.Builder
	last := 0
	for i := 0; i < len(live); {
		from, to := live[i][0], live[i][1]
		for i++; i < len(live) && live[i][0] <= to; i++ {
			to = max(to, live[i][1])
		}
		b.WriteString(s[last:from])
		b.WriteString(mark)
		last = to
	}
	b.WriteString(s[last:])
	return b.String()
}
