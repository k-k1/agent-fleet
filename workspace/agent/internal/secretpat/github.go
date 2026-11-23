// Package secretpat holds the credential shapes that more than one detector or redactor in
// its module matches on, so a vendor changing a token's shape is fixed in one place.
// control-plane/internal/secretpat and workspace/agent/internal/secretpat are byte-identical
// copies, because the two modules cannot import each other; secretpat_test.go compares them.
package secretpat

import (
	"regexp"
	"strconv"
)

// GitHubTokenMinBody is the shortest classic body (the alphanumerics after `gh?_`) a detector
// takes. A classic token's body is exactly 36; anything shorter in prose is far more likely a
// name than a credential. Redactors that deliberately cast a wider net pass their own, lower
// minimum to GitHubTokenPattern.
const GitHubTokenMinBody = 36

// GitHubStatelessPattern matches a stateless token whole: `ghs_APPID_JWT` as GitHub documents
// it (about 520 characters), and the same without the APPID. It is held to that structure, a
// numeric APPID and a three-segment JWT, rather than to GitHub's looser suggestion
// `ghs_[A-Za-z0-9._-]{36,}`, because the looser class also takes ordinary names such as
// `ghs_status_dashboard_for_enterprise_repositories.md`.
//
// It has no leading `\b`: the structure is specific enough on its own, and a boundary would
// miss a token glued to a word (`cache_ghs_…`) and leave it all in clear.
const GitHubStatelessPattern = `gh[pousr]_(?:[0-9]+_)?eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`

// GitHubClassicPattern matches a classic token: `gh?_` and at least minBody alphanumerics.
// There is no trailing `\b`, which would drop a token followed by `_` entirely.
func GitHubClassicPattern(minBody int) string {
	return `\bgh[pousr]_[A-Za-z0-9]{` + strconv.Itoa(minBody) + `,}`
}

// GitHubTokenPattern matches a GitHub token of either shape. The stateless branch comes first:
// a stateless token without an APPID starts with a 36-character JWT header that the classic
// branch would take on its own, leaving the payload and signature behind.
func GitHubTokenPattern(minBody int) string {
	return `(?:` + GitHubStatelessPattern + `|` + GitHubClassicPattern(minBody) + `)`
}

var (
	// GitHubToken matches a GitHub token of either shape at the detectors' minimum.
	GitHubToken = regexp.MustCompile(GitHubTokenPattern(GitHubTokenMinBody))
	// GitHubStateless matches the stateless shape only.
	GitHubStateless = regexp.MustCompile(GitHubStatelessPattern)
	// GitHubClassic matches the classic shape only, at the detectors' minimum.
	GitHubClassic = regexp.MustCompile(GitHubClassicPattern(GitHubTokenMinBody))
)
