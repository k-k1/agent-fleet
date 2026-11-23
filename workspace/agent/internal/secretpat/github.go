// Package secretpat holds the credential shapes that more than one detector or redactor in
// its module matches on, so a vendor changing a token's shape is fixed in one place.
// control-plane/internal/secretpat and workspace/agent/internal/secretpat are byte-identical
// copies, because the two modules cannot import each other; secretpat_test.go compares them.
package secretpat

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// githubStatelessHead is a stateless token up to its JWT header's first key: `gh?_`, an optional
// numeric APPID, and base64url of a JSON object's opening up to the key's quote: `{"` (`eyI`,
// `eyJ`), `{` and one whitespace byte before the quote (`eyAi`, `ewoi`, `ewog`, `ewki`, `ew0i`,
// `ew0K`), or one whitespace byte before the `{` (`IHsi`, `Cnsi`, `CXsi`, `DXsi`). That opening
// is what tells a token from a name with an APPID-like number and dotted parts
// (`ghs_202610_eyewashchecklist.productionrollout.<digest>.md`). The runtime also decodes the
// header and requires a JSON object (GitHubStatelessSpans); gitleaks has only this pattern.
const githubStatelessHead = `gh[pousr]_(?:[0-9]+_)?(?:ey[IJ]|eyAi|ewo[ig]|ewki|ew0[iK]|IHsi|Cnsi|CXsi|DXsi)`

// GitHubStatelessPattern matches a stateless token whole: `ghs_APPID_JWT` as GitHub documents
// it (about 520 characters, `.`, `-` and `_` in the body), and the same without the APPID. The
// minimums sit below the smallest real JWT (a 19-character `{"alg":"none"}` header, a
// 43-character HS256 signature).
//
// It is held to that structure rather than to GitHub's looser `ghs_[A-Za-z0-9._-]{36,}`, which
// also takes ordinary names such as `ghs_status_dashboard_for_enterprise_repositories.md`. It
// has no leading `\b`: the structure is specific enough on its own, and a boundary would miss a
// token glued to a word (`cache_ghs_…`) and leave it all in clear.
const GitHubStatelessPattern = githubStatelessHead + `[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{32,}`

// GitHubClassicPattern is the classic token shape the detectors have always used: `gh?_` and
// at least 36 alphanumerics, between word boundaries. The trailing boundary is what keeps a
// name like `ghs_<36 letters>_enabled.md` out; do not drop it.
const GitHubClassicPattern = `\bgh[pousr]_[A-Za-z0-9]{36,}\b`

var (
	githubClassic = regexp.MustCompile(GitHubClassicPattern)
	githubPrefix  = regexp.MustCompile(`gh[pousr]_`)
	// githubStateless is GitHubStatelessPattern anchored at a start. statelessAt is what runs;
	// the tests hold the two to the same answers.
	githubStateless = regexp.MustCompile(`^(?:` + GitHubStatelessPattern + `)`)
)

// statelessHeads are githubStatelessHead's openings of a JSON object in base64url.
var statelessHeads = []string{"eyI", "eyJ", "eyAi", "ewoi", "ewog", "ewki", "ew0i", "ew0K", "IHsi", "Cnsi", "CXsi", "DXsi"}

// statelessMaxHeader bounds a candidate's header. A JOSE header is a few dozen bytes; the bound
// keeps a long line of token-like starts from costing a decode each.
const statelessMaxHeader = 1024

// GitHubStatelessSpans lists the stateless tokens in s as [start, end) byte offsets. Every
// `gh?_` is tried as a start on its own, so a token that a previous match ran into (two tokens
// written back to back: a signature takes the next token's head) is still found; RedactAll and
// ReplaceSpans merge the overlap. A candidate whose header does not decode to a JSON object is
// not a token.
//
// Each start is matched by statelessAt, not by the anchored regexp: the regexp runs to the end
// of the base64url run before it fails, so a line packed with starts and no dot cost time
// quadratic in its length (measured: 78 KB took 11 s, and the memory scan takes 4 MiB blobs).
// statelessAt shares each run's end between the starts inside it.
func GitHubStatelessSpans(s string) [][2]int {
	var out [][2]int
	var header, payload, sig runEnds
	for _, p := range githubPrefix.FindAllStringIndex(s, -1) {
		if end := statelessAt(s, p[0], &header, &payload, &sig); end > 0 {
			out = append(out, [2]int{p[0], end})
		}
	}
	return out
}

// statelessAt is the end of the stateless token starting at p (s[p:] starts with `gh?_`), or 0
// for none: githubStateless's answer, with the header bound and decode check on top.
func statelessAt(s string, p int, header, payload, sig *runEnds) int {
	i := p + len("ghs_")
	if i < len(s) && isDigit(s[i]) {
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		if i >= len(s) || s[i] != '_' {
			return 0
		}
		i++
	}
	h := i
	head := false
	for _, o := range statelessHeads {
		head = head || strings.HasPrefix(s[h:], o)
	}
	if !head {
		return 0
	}
	he := header.end(s, h)
	if he-h < 16 || he-h > statelessMaxHeader || he >= len(s) || s[he] != '.' {
		return 0
	}
	pe := payload.end(s, he+1)
	if pe-(he+1) < 16 || pe >= len(s) || s[pe] != '.' {
		return 0
	}
	se := sig.end(s, pe+1)
	if se-(pe+1) < 32 || !jwtHeaderIsObject(s[p:se]) {
		return 0
	}
	return se
}

// runEnds remembers the last base64url run it measured: every position inside a run has the
// same end, so the starts that share a run (or a payload, or a signature) measure it once.
type runEnds struct{ from, to int }

// end is the end of the run of base64url characters starting at i.
func (r *runEnds) end(s string, i int) int {
	if r.to > 0 && i >= r.from && i <= r.to {
		return r.to
	}
	j := i
	for j < len(s) && isBase64URL(s[j]) {
		j++
	}
	*r = runEnds{i, j}
	return j
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isBase64URL(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || isDigit(c) || c == '-' || c == '_'
}

// jwtHeaderIsObject reports whether a stateless candidate's header, the part between the APPID
// (or the prefix) and the first dot, is base64url of a JSON object.
func jwtHeaderIsObject(tok string) bool {
	h := tok[len("ghs_"):]
	if h != "" && h[0] >= '0' && h[0] <= '9' {
		h = h[strings.IndexByte(h, '_')+1:]
	}
	h, _, _ = strings.Cut(h, ".")
	b, err := base64.RawURLEncoding.DecodeString(h)
	if err != nil {
		return false
	}
	t := bytes.TrimLeft(b, " \t\r\n")
	return len(t) > 0 && t[0] == '{' && json.Valid(b)
}

// GitHubTokens finds GitHub tokens of either shape, for detectors: the classic pattern and
// GitHubStatelessSpans. It has the regexp methods the detectors call.
var GitHubTokens githubTokens

type githubTokens struct{}

// spans are the tokens in s, merged where they overlap: without an APPID, a stateless token's
// 36-character header is a classic match of its own.
func (githubTokens) spans(s string) [][2]int {
	return mergeSpans(append(Spans(s, githubClassic), GitHubStatelessSpans(s)...))
}

// MatchString reports whether s holds a GitHub token.
func (g githubTokens) MatchString(s string) bool { return len(g.spans(s)) > 0 }

// FindString returns the first GitHub token in s, "" for none.
func (g githubTokens) FindString(s string) string {
	if sp := g.spans(s); len(sp) > 0 {
		return s[sp[0][0]:sp[0][1]]
	}
	return ""
}

// FindAllStringSubmatch returns every GitHub token in s, each as a one-element match; n < 0
// means all, as with regexp.
func (g githubTokens) FindAllStringSubmatch(s string, n int) [][]string {
	var out [][]string
	for _, sp := range g.spans(s) {
		if n >= 0 && len(out) >= n {
			break
		}
		out = append(out, []string{s[sp[0]:sp[1]]})
	}
	return out
}

// IsGitHubStateless reports whether v is, whole, one stateless token.
func IsGitHubStateless(v string) bool {
	for _, sp := range GitHubStatelessSpans(v) {
		if sp[0] == 0 && sp[1] == len(v) {
			return true
		}
	}
	return false
}

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
	var b strings.Builder
	last := 0
	for _, sp := range mergeSpans(spans) {
		b.WriteString(s[last:sp[0]])
		b.WriteString(mark)
		last = sp[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// mergeSpans sorts spans and merges those that overlap or touch; empty spans are dropped.
func mergeSpans(spans [][2]int) [][2]int {
	var live [][2]int
	for _, sp := range spans {
		if sp[1] > sp[0] {
			live = append(live, sp)
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i][0] < live[j][0] })
	var out [][2]int
	for _, sp := range live {
		if n := len(out); n > 0 && sp[0] <= out[n-1][1] {
			out[n-1][1] = max(out[n-1][1], sp[1])
			continue
		}
		out = append(out, sp)
	}
	return out
}
