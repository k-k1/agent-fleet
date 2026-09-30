package gitx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// squashMerged gives origin a branch with one commit, publishes it as refs/pull/<pr>/head the
// way GitHub keeps a pull request's head, squash-merges it into main and deletes the branch.
// The branch commit is then reachable from refs/pull only, which a plain fetch does not take.
func squashMerged(t *testing.T, origin, branch, pr string) string {
	t.Helper()
	sha := commitOn(t, origin, branch)
	gitAt(t, origin, "update-ref", "refs/pull/"+pr+"/head", sha)
	gitAt(t, origin, "merge", "-q", "--squash", branch)
	gitAt(t, origin, "commit", "-q", "-m", branch+" (#"+pr+")")
	gitAt(t, origin, "branch", "-q", "-D", branch)
	return sha
}

// fakeGitHub answers GET /repos/o/r/pulls from prs (keyed by the head query) and counts the
// requests, so a test can also show the forge was never asked.
func fakeGitHub(t *testing.T, prs map[string][]map[string]any) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		q := r.URL.Query()
		if r.URL.Path != "/repos/o/r/pulls" || q.Get("state") != "closed" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("unexpected request %s %s auth=%q", r.Method, r.URL, r.Header.Get("Authorization"))
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		list := prs[q.Get("head")]
		if list == nil {
			list = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(list)
	}))
	t.Cleanup(srv.Close)
	base, tok := githubAPIBase, githubToken
	githubAPIBase, githubToken = srv.URL, func() string { return "tok" }
	t.Cleanup(func() { githubAPIBase, githubToken = base, tok })
	return &hits
}

func pullJSON(n int, branch, sha string, merged bool) map[string]any {
	p := map[string]any{"number": n, "merged_at": nil,
		"head": map[string]any{"ref": branch, "sha": sha, "repo": map[string]any{"full_name": "o/r"}}}
	if merged {
		p["merged_at"] = "2026-09-30T00:00:00Z"
	}
	return p
}

// onGitHub makes parent's origin read as github.com/o/r while every fetch still goes to the
// local origin, through url.<base>.insteadOf.
func onGitHub(t *testing.T, parent string) string {
	t.Helper()
	origin := filepath.Join(filepath.Dir(filepath.Dir(parent)), "origin")
	gitAt(t, parent, "remote", "set-url", "origin", "https://github.com/o/r.git")
	gitAt(t, parent, "config", "url."+origin+".insteadOf", "https://github.com/o/r.git")
	return origin
}

func TestResolveRecreateSquashMergedFromForge(t *testing.T) {
	parent := recreateFixture(t)
	origin := onGitHub(t, parent)

	// sq-seen was fetched before it was merged and deleted: its commit is still here.
	seenSHA := commitOn(t, origin, "sq-seen")
	gitAt(t, parent, "fetch", "-q")
	gitAt(t, origin, "update-ref", "refs/pull/11/head", seenSHA)
	gitAt(t, origin, "merge", "-q", "--squash", "sq-seen")
	gitAt(t, origin, "commit", "-q", "-m", "sq-seen (#11)")
	gitAt(t, origin, "branch", "-q", "-D", "sq-seen")
	// sq-unseen never reached this clone: its head has to come from refs/pull/12/head.
	unseenSHA := squashMerged(t, origin, "sq-unseen", "12")
	// sq-open's pull request was closed without merging.
	openSHA := squashMerged(t, origin, "sq-open", "13")
	gitAt(t, parent, "fetch", "-q", "--prune")
	if commitExists(parent, unseenSHA) {
		t.Fatal("fixture: the unseen head is already in the clone")
	}

	hits := fakeGitHub(t, map[string][]map[string]any{
		"o:sq-seen": {pullJSON(11, "sq-seen", seenSHA, true)},
		// The newest pull request is an unmerged retry; the merged one below it wins.
		"o:sq-unseen": {pullJSON(14, "sq-unseen", openSHA, false), pullJSON(12, "sq-unseen", unseenSHA, true)},
		"o:sq-open":   {pullJSON(13, "sq-open", openSHA, false)},
	})
	got := ResolveRecreate(parent, "app@x", []string{"sq-seen", "sq-unseen", "sq-open"}, nil, nil)
	want := []RecreateCandidate{
		{Source: RecreateMerged, Branch: "sq-seen", SHA: seenSHA, PR: 11},
		{Source: RecreateMerged, Branch: "sq-unseen", SHA: unseenSHA, PR: 12},
	}
	if len(got) != len(want) {
		t.Fatalf("candidates = %+v\nwant %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if n := hits.Load(); n != 3 {
		t.Errorf("forge asked %d times, want 3", n)
	}

	// The fetched head is enough to put the worktree back on its branch.
	dir := filepath.Join(filepath.Dir(parent), "app@sq-unseen")
	if err := RecreateWorktreeAt(parent, dir, got[1], ""); err != nil {
		t.Fatal(err)
	}
	if b, s := GitCurrentBranch(dir), strings.TrimSpace(gitAt(t, dir, "rev-parse", "HEAD")); b != "sq-unseen" || s != unseenSHA {
		t.Errorf("recreated = %s@%s, want sq-unseen@%s", b, s, unseenSHA)
	}

	// Nothing merged at all: still today's new branch off the parent.
	if got := ResolveRecreate(parent, "app@x", []string{"sq-open"}, nil, nil); len(got) != 1 ||
		got[0] != (RecreateCandidate{Source: RecreateNew, Branch: "sq-open", Ref: "main"}) {
		t.Errorf("unmerged pull request = %+v, want new", got)
	}
}

// Every way the forge cannot answer keeps today's plan: a new branch off the parent.
func TestResolveRecreateForgeUnavailable(t *testing.T) {
	newOnly := func(t *testing.T, parent string) {
		t.Helper()
		if got := ResolveRecreate(parent, "app@x", []string{"sq"}, nil, nil); len(got) != 1 ||
			got[0] != (RecreateCandidate{Source: RecreateNew, Branch: "sq", Ref: "main"}) {
			t.Errorf("candidates = %+v, want one new off main", got)
		}
	}
	setup := func(t *testing.T) (parent, sha string, hits *atomic.Int32) {
		parent = recreateFixture(t)
		origin := onGitHub(t, parent)
		sha = squashMerged(t, origin, "sq", "5")
		gitAt(t, parent, "fetch", "-q", "--prune")
		hits = fakeGitHub(t, map[string][]map[string]any{"o:sq": {pullJSON(5, "sq", sha, true)}})
		return parent, sha, hits
	}

	t.Run("no connection", func(t *testing.T) {
		parent, _, hits := setup(t)
		githubToken = func() string { return "" }
		newOnly(t, parent)
		if hits.Load() != 0 {
			t.Error("the forge was asked without a token")
		}
	})
	t.Run("not github", func(t *testing.T) {
		parent, _, hits := setup(t)
		gitAt(t, parent, "remote", "set-url", "origin", "https://gitlab.com/o/r.git")
		newOnly(t, parent)
		if hits.Load() != 0 {
			t.Error("the forge was asked for a non-GitHub origin")
		}
	})
	t.Run("forge unreachable", func(t *testing.T) {
		parent, _, _ := setup(t)
		srv := httptest.NewServer(http.NotFoundHandler())
		githubAPIBase = srv.URL
		srv.Close()
		newOnly(t, parent)
	})
	t.Run("forge error", func(t *testing.T) {
		parent, _, _ := setup(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "rate limited", http.StatusForbidden)
		}))
		t.Cleanup(srv.Close)
		githubAPIBase = srv.URL
		newOnly(t, parent)
	})
	t.Run("head not fetchable", func(t *testing.T) {
		parent, _, _ := setup(t)
		fakeGitHub(t, map[string][]map[string]any{"o:sq": {pullJSON(6, "sq", strings.Repeat("ab", 20), true)}})
		newOnly(t, parent)
	})
}
