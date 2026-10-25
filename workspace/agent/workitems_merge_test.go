package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// ADR 0061, 2026-10-25 note on #2632 - the second human-gated write. What is pinned: the request
// carries the displayed head SHA (a stale panel is refused, not merged), only a method GitHub
// knows is sent, GitHub's refusals reach the Console with a code it can branch on, and the token
// never travels back.

const mergeSHA = "0123456789abcdef0123456789abcdef01234567"

type mergeCall struct {
	method, path, auth, body string
}

// fakeGitHubMerge answers every request with `status`/`reply` and records what arrived.
func fakeGitHubMerge(t *testing.T, status int, reply string) *[]mergeCall {
	t.Helper()
	var calls []mergeCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, mergeCall{r.Method, r.URL.Path, r.Header.Get("Authorization"), string(b)})
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old; srv.Close() })
	return &calls
}

func seedGitHubToken(t *testing.T) {
	t.Helper()
	seedBktStore(t, func(s *secrets.Data) {
		s.Git["github.com"] = secrets.GitEntry{Token: "ghu_secret_token_value"}
	})
}

func postMerge(body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handleWorkItemsMerge(w, httptest.NewRequest("POST", "/work-items/merge", strings.NewReader(body)))
	return w
}

func TestWorkItemsMergeSendsDisplayedSHAAndMethod(t *testing.T) {
	seedGitHubToken(t)
	calls := fakeGitHubMerge(t, 200, `{"sha":"feedface","merged":true,"message":"Pull Request successfully merged"}`)
	w := postMerge(`{"provider":"github","key":"acme/web#12","sha":"` + mergeSHA + `","method":"squash"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"merged":true`) {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %+v, want exactly one", *calls)
	}
	c := (*calls)[0]
	if c.method != "PUT" || c.path != "/repos/acme/web/pulls/12/merge" {
		t.Errorf("request = %s %s", c.method, c.path)
	}
	// Without `sha` GitHub merges whatever the head is now, which is the stale-panel hole.
	var sent map[string]string
	_ = json.Unmarshal([]byte(c.body), &sent)
	if sent["sha"] != mergeSHA || sent["merge_method"] != "squash" {
		t.Errorf("body = %s", c.body)
	}
	if c.auth != "Bearer ghu_secret_token_value" {
		t.Errorf("the token path is the live read's, got %q", c.auth)
	}
	if strings.Contains(w.Body.String(), "ghu_secret") {
		t.Errorf("response leaks the token: %s", w.Body.String())
	}
}

// A moved head is the case the SHA exists for: GitHub answers 409 and nothing is merged.
func TestWorkItemsMergeRefusesStaleHead(t *testing.T) {
	seedGitHubToken(t)
	fakeGitHubMerge(t, 409, `{"message":"Head branch was modified. Review and try the merge again."}`)
	w := postMerge(`{"key":"acme/web#12","sha":"` + mergeSHA + `","method":"merge"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"head_changed"`) {
		t.Fatalf("got %d %s, want 409 head_changed", w.Code, w.Body.String())
	}
}

// GitHub's 405 carries the reason (branch protection, a conflict, a method the repository
// forbids); rewriting it would hide which of them it was.
func TestWorkItemsMergeSurfacesNotMergeable(t *testing.T) {
	seedGitHubToken(t)
	fakeGitHubMerge(t, 405, `{"message":"Required status check \"ci\" is expected."}`)
	w := postMerge(`{"key":"acme/web#12","sha":"` + mergeSHA + `","method":"rebase"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"not_mergeable"`) ||
		!strings.Contains(w.Body.String(), "Required status check") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestWorkItemsMergePermissionRefusalIsAProviderError(t *testing.T) {
	seedGitHubToken(t)
	fakeGitHubMerge(t, 403, `{"message":"Resource not accessible by integration"}`)
	w := postMerge(`{"key":"acme/web#12","sha":"` + mergeSHA + `","method":"merge"}`)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "provider_error") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "ghu_secret") {
		t.Errorf("error leaks the token: %s", w.Body.String())
	}
}

// Everything that can be refused locally is refused before any network call.
func TestWorkItemsMergeValidation(t *testing.T) {
	seedGitHubToken(t)
	calls := fakeGitHubMerge(t, 200, `{"merged":true}`)
	for name, body := range map[string]string{
		"bitbucket":    `{"provider":"bitbucket","key":"ws/repo#1","sha":"` + mergeSHA + `","method":"merge"}`,
		"jira":         `{"provider":"jira","key":"PROJ-1","sha":"` + mergeSHA + `","method":"merge"}`,
		"no sha":       `{"key":"acme/web#1","method":"merge"}`,
		"short sha":    `{"key":"acme/web#1","sha":"deadbeef","method":"merge"}`,
		"no method":    `{"key":"acme/web#1","sha":"` + mergeSHA + `"}`,
		"odd method":   `{"key":"acme/web#1","sha":"` + mergeSHA + `","method":"fast-forward"}`,
		"malformed":    `{"key":"nonsense","sha":"` + mergeSHA + `","method":"merge"}`,
		"not json":     `nope`,
		"path escapes": `{"key":"acme/../web#1","sha":"` + mergeSHA + `","method":"merge"}`,
	} {
		if w := postMerge(body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d %s, want 400", name, w.Code, w.Body.String())
		}
	}
	if len(*calls) != 0 {
		t.Errorf("GitHub was reached for a refused request: %+v", *calls)
	}
}

func TestWorkItemsMergeNeedsAConnection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := fakeGitHubMerge(t, 200, `{"merged":true}`)
	w := postMerge(`{"key":"acme/web#1","sha":"` + mergeSHA + `","method":"merge"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not_connected") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if len(*calls) != 0 {
		t.Errorf("GitHub was reached without a token: %+v", *calls)
	}
}

// --- the live read's merge gate ----------------------------------------------

func mergePR(repoExtra string) []byte {
	return []byte(`{"number":3,"state":"open","mergeable_state":"CLEAN","mergeable":true,
	  "base":{"ref":"main","repo":{"full_name":"acme/web"` + repoExtra + `}},
	  "head":{"ref":"f","sha":"` + mergeSHA + `"}}`)
}

func TestParseGitHubPullRequestMergeGate(t *testing.T) {
	out, _, err := parseGitHubPullRequest(mergePR(`,"private":true,"allow_merge_commit":false,
	  "allow_squash_merge":true,"allow_rebase_merge":true,"permissions":{"admin":false,"push":true,"pull":true}`), "acme/web#3")
	if err != nil {
		t.Fatal(err)
	}
	if out.HeadSHA != mergeSHA || out.MergeableState != "clean" {
		t.Errorf("head/state = %q/%q — the state is lower-cased, the SHA is what merge sends back", out.HeadSHA, out.MergeableState)
	}
	if strings.Join(out.MergeMethods, ",") != "squash,rebase" || out.DefaultMergeMethod != "squash" {
		t.Errorf("methods = %v default %q, want only the allowed ones, merge first when allowed", out.MergeMethods, out.DefaultMergeMethod)
	}
	if !out.CanMerge {
		t.Error("push access must allow the button")
	}
	// A read with no permissions block (nothing to go on) must not draw a button.
	bare, _, _ := parseGitHubPullRequest(mergePR(`,"allow_merge_commit":true`), "acme/web#3")
	if bare.CanMerge {
		t.Error("unknown permissions drew a Merge button")
	}
	ro, _, _ := parseGitHubPullRequest(mergePR(`,"allow_merge_commit":true,"permissions":{"admin":false,"push":false,"pull":true}`), "acme/web#3")
	if ro.CanMerge {
		t.Error("a read-only user drew a Merge button")
	}
	// Never null on the wire, and absent for the providers that cannot merge.
	empty, _, _ := parseGitHubPullRequest([]byte(`{"number":1}`), "acme/web#1")
	j, _ := json.Marshal(empty)
	if !strings.Contains(string(j), `"mergeMethods":[]`) {
		t.Errorf("mergeMethods must marshal as []: %s", j)
	}
	bb, _ := parseBitbucketPullRequest([]byte(`{"id":1,"state":"OPEN"}`), "ws/r#1")
	bj, _ := json.Marshal(bb)
	if !strings.Contains(string(bj), `"mergeMethods":[]`) || !strings.Contains(string(bj), `"canMerge":false`) || bb.MergeableState != "" {
		t.Errorf("a Bitbucket row must carry no merge gate: %s", bj)
	}
}

func TestGithubScopesCanMerge(t *testing.T) {
	h := func(v string, set bool) http.Header {
		hd := http.Header{}
		if set {
			hd.Set("X-OAuth-Scopes", v)
		}
		return hd
	}
	for _, tc := range []struct {
		name    string
		hdr     http.Header
		private bool
		want    bool
	}{
		{"fine-grained (no header)", h("", false), true, true},
		{"repo scope", h("repo, workflow", true), true, true},
		{"read:user only", h("read:user", true), false, false},
		{"empty scopes", h("", true), false, false},
		{"public_repo on a public repo", h("public_repo", true), false, true},
		{"public_repo on a private repo", h("public_repo", true), true, false},
	} {
		if got := githubScopesCanMerge(tc.hdr, tc.private); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// When the pull request object carries no merge settings the repository is read once more; a
// failure there still leaves a usable panel with no button.
func TestGitHubPullRequestDetailFallsBackToRepo(t *testing.T) {
	pr := string(mergePR(``))
	hits := fakeGitHub(t, map[string]string{
		"/repos/acme/web/pulls/3": pr,
		"/repos/acme/web":         `{"private":false,"allow_merge_commit":true,"allow_squash_merge":false,"allow_rebase_merge":false,"permissions":{"push":true}}`,
	})
	out, err := githubPullRequestDetail("tok", "acme/web#3")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(out.MergeMethods, ",") != "merge" || !out.CanMerge {
		t.Errorf("fallback not applied: %v %v (hits %v)", out.MergeMethods, out.CanMerge, *hits)
	}
	fakeGitHub(t, map[string]string{"/repos/acme/web/pulls/3": pr})
	out, err = githubPullRequestDetail("tok", "acme/web#3")
	if err != nil {
		t.Fatalf("a failed repository read must not fail the panel: %v", err)
	}
	if out.CanMerge || len(out.MergeMethods) != 0 {
		t.Errorf("no repository data, yet a button was drawn: %+v", out)
	}
}

// The embedded repository may carry the merge settings but no permissions block; the repository
// read then decides, so a writer is not left without the button.
func TestGitHubPullRequestDetailReadsPermissionsWhenEmbeddedOnesAreMissing(t *testing.T) {
	hits := fakeGitHub(t, map[string]string{
		"/repos/acme/web/pulls/3": string(mergePR(`,"allow_merge_commit":true`)),
		"/repos/acme/web":         `{"allow_merge_commit":true,"permissions":{"push":true}}`,
	})
	out, err := githubPullRequestDetail("tok", "acme/web#3")
	if err != nil {
		t.Fatal(err)
	}
	if !out.CanMerge {
		t.Errorf("repository permissions were not consulted: %+v (hits %v)", out, *hits)
	}
}

// A classic token without the repo scope is caught before the press.
func TestGitHubPullRequestDetailChecksTokenScopes(t *testing.T) {
	body := string(mergePR(`,"private":true,"allow_merge_commit":true,"permissions":{"push":true}`))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-OAuth-Scopes", "read:user")
		if r.URL.Path == "/repos/acme/web/pulls/3" {
			_, _ = w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	old := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = old }()
	out, err := githubPullRequestDetail("tok", "acme/web#3")
	if err != nil {
		t.Fatal(err)
	}
	if out.CanMerge {
		t.Error("a token without the repo scope drew a Merge button")
	}
}
