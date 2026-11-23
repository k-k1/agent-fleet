// Package secretpat holds the credential shapes that more than one detector or redactor in
// its module matches on, so a vendor changing a token's shape is fixed in one place.
// control-plane/internal/secretpat and workspace/agent/internal/secretpat are byte-identical
// copies, because the two modules cannot import each other; secretpat_test.go compares them.
package secretpat

import (
	"regexp"
	"strconv"
)

// GitHubTokenMinBody is the shortest body (the part after `gh?_`) a detector takes for a
// GitHub token. A classic token's body is exactly 36 alphanumerics; anything shorter in prose
// is far more likely a name than a credential. Redactors that deliberately cast a wider net
// pass their own, lower minimum to GitHubTokenPattern.
const GitHubTokenMinBody = 36

// GitHubTokenPattern is the source of a regexp matching one whole GitHub token (ghp_, gho_,
// ghu_, ghs_, ghr_) whose body has at least minBody characters (minBody >= 2).
//
// The body admits `.`, `-` and `_` because stateless installation tokens (`ghs_`, about 520
// characters, JWT-shaped) carry them; an alphanumeric-only class stops at the first one and
// leaves the rest of the token in clear, and the leading part it does catch is the JWT header,
// which is predictable. The last character excludes `.` so a sentence ending right after a
// token keeps its full stop. There is no trailing `\b`: a body may end in `-`, and a boundary
// there would cut the last character off instead.
func GitHubTokenPattern(minBody int) string {
	return `\bgh[pousr]_[A-Za-z0-9._-]{` + strconv.Itoa(minBody-1) + `,}[A-Za-z0-9_-]`
}

// GitHubToken matches a whole GitHub token, classic or stateless, at the detectors' minimum.
var GitHubToken = regexp.MustCompile(GitHubTokenPattern(GitHubTokenMinBody))
