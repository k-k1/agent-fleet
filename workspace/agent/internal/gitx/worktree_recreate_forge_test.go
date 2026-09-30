package gitx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
		per, _ := strconv.Atoi(q.Get("per_page"))
		page, _ := strconv.Atoi(q.Get("page"))
		if per <= 0 || page <= 0 {
			t.Errorf("unpaged request %s", r.URL)
			per, page = 30, 1
		}
		lo, hi := min((page-1)*per, len(list)), min(page*per, len(list))
		_ = json.NewEncoder(w).Encode(append([]map[string]any{}, list[lo:hi]...))
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

// A merged pull request below a full page of closed retries is still found, and one whose
// head repository is gone or a fork is passed over for the one from repo itself.
func TestGitHubMergedPRPagesAndHeadRepo(t *testing.T) {
	good := strings.Repeat("a1", 20)
	var list []map[string]any
	for i := 0; i < githubPullsPerPage+20; i++ {
		list = append(list, pullJSON(1000-i, "sq", strings.Repeat("0f", 20), false))
	}
	orphan := pullJSON(900, "sq", strings.Repeat("b2", 20), true)
	orphan["head"].(map[string]any)["repo"] = nil
	fork := pullJSON(899, "sq", strings.Repeat("c3", 20), true)
	fork["head"].(map[string]any)["repo"] = map[string]any{"full_name": "someone/r"}
	list = append(list, orphan, fork, pullJSON(898, "sq", good, true))
	hits := fakeGitHub(t, map[string][]map[string]any{"o:sq": list})

	if sha, pr, _ := githubMergedPR(context.Background(), "tok", "o/r", "sq"); sha != good || pr != 898 {
		t.Errorf("githubMergedPR = %s, %d; want %s, 898", sha, pr, good)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("pages asked = %d, want 2", n)
	}
}

// The token never follows a redirect off the API's scheme and host: Go's default client would
// forward Authorization to the same hostname on another port, or from https to http.
func TestGitHubMergedPRRefusesCrossOriginRedirect(t *testing.T) {
	var leaked atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Add(1)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{pullJSON(1, "sq", strings.Repeat("d4", 20), true)})
	}))
	t.Cleanup(sink.Close)
	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL+r.URL.RequestURI(), http.StatusFound)
	})
	base, transport := githubAPIBase, githubHTTPClient.Transport
	t.Cleanup(func() { githubAPIBase, githubHTTPClient.Transport = base, transport })

	for name, srv := range map[string]*httptest.Server{
		"another port":  httptest.NewServer(redirect),
		"https to http": httptest.NewTLSServer(redirect),
	} {
		t.Cleanup(srv.Close)
		githubAPIBase, githubHTTPClient.Transport = srv.URL, srv.Client().Transport
		if sha, _, _ := githubMergedPR(context.Background(), "tok", "o/r", "sq"); sha != "" {
			t.Errorf("%s: followed the redirect to %s", name, sha)
		}
	}
	if n := leaked.Load(); n != 0 {
		t.Errorf("the token reached the redirect target %d times", n)
	}
}

// A branch name merged twice — first through a merge commit, then reused and squash-merged —
// resolves to the later merge. The merge commit alone answers offline with the older head, so
// the forge is asked as well and the newer of the two wins; any doubt keeps the offline answer.
func TestResolveRecreateMergedTwicePrefersNewest(t *testing.T) {
	setup := func(t *testing.T) (parent, oldSHA, newSHA string) {
		parent = recreateFixture(t)
		origin := onGitHub(t, parent)
		oldSHA = commitOn(t, origin, "reused")
		t.Setenv("GIT_COMMITTER_DATE", "2026-09-10T00:00:00Z")
		gitAt(t, origin, "update-ref", "refs/pull/7/head", oldSHA)
		gitAt(t, origin, "merge", "-q", "--no-ff", "-m", "Merge pull request #7 from o/reused", "reused")
		gitAt(t, origin, "branch", "-q", "-D", "reused")
		t.Setenv("GIT_COMMITTER_DATE", "2026-09-20T00:00:00Z")
		gitAt(t, origin, "checkout", "-q", "-b", "reused", "main")
		writeFile(t, filepath.Join(origin, "reused-again"), "again")
		gitAt(t, origin, "add", "-A")
		gitAt(t, origin, "commit", "-q", "-m", "reused again")
		newSHA = strings.TrimSpace(gitAt(t, origin, "rev-parse", "HEAD"))
		gitAt(t, origin, "checkout", "-q", "main")
		gitAt(t, origin, "update-ref", "refs/pull/9/head", newSHA)
		gitAt(t, origin, "merge", "-q", "--squash", "reused")
		gitAt(t, origin, "commit", "-q", "-m", "reused (#9)")
		gitAt(t, origin, "branch", "-q", "-D", "reused")
		gitAt(t, parent, "fetch", "-q", "--prune")
		if commitExists(parent, newSHA) {
			t.Fatal("fixture: the squash-merged head is already in the clone")
		}
		return parent, oldSHA, newSHA
	}
	merged := func(n int, sha, at string) map[string]any {
		p := pullJSON(n, "reused", sha, true)
		p["merged_at"] = at
		return p
	}
	resolve := func(t *testing.T, parent string) RecreateCandidate {
		t.Helper()
		got := ResolveRecreate(parent, "app@x", []string{"reused"}, nil, nil)
		if len(got) != 1 {
			t.Fatalf("candidates = %+v, want one", got)
		}
		return got[0]
	}

	t.Run("forge newer wins", func(t *testing.T) {
		parent, oldSHA, newSHA := setup(t)
		hits := fakeGitHub(t, map[string][]map[string]any{"o:reused": {
			merged(9, newSHA, "2026-09-20T00:00:00Z"), merged(7, oldSHA, "2026-09-10T00:00:00Z")}})
		if c, want := resolve(t, parent), (RecreateCandidate{Source: RecreateMerged, Branch: "reused", SHA: newSHA, PR: 9}); c != want {
			t.Errorf("candidate = %+v, want %+v", c, want)
		}
		if n := hits.Load(); n != 1 {
			t.Errorf("forge asked %d times, want 1", n)
		}
	})
	offline := func(oldSHA string) RecreateCandidate {
		return RecreateCandidate{Source: RecreateMerged, Branch: "reused", SHA: oldSHA, PR: 7}
	}
	// GitHub lists by creation, not by merge: a pull request opened earlier can be merged
	// later, and the latest merge wins wherever it sits in the list.
	t.Run("forge newest merge listed later", func(t *testing.T) {
		parent, oldSHA, newSHA := setup(t)
		hits := fakeGitHub(t, map[string][]map[string]any{"o:reused": {
			merged(11, strings.Repeat("e5", 20), "2026-09-01T00:00:00Z"), merged(9, newSHA, "2026-09-20T00:00:00Z"),
			merged(7, oldSHA, "2026-09-10T00:00:00Z")}})
		if c, want := resolve(t, parent), (RecreateCandidate{Source: RecreateMerged, Branch: "reused", SHA: newSHA, PR: 9}); c != want {
			t.Errorf("candidate = %+v, want %+v", c, want)
		}
		if n := hits.Load(); n != 1 {
			t.Errorf("forge asked %d times, want 1", n)
		}
	})
	// keepsOffline also shows the forge was really asked, and that an answer not newer than
	// the merge commit is never fetched.
	keepsOffline := func(t *testing.T, parent, oldSHA, newSHA string, hits *atomic.Int32) {
		t.Helper()
		if c := resolve(t, parent); c != offline(oldSHA) {
			t.Errorf("candidate = %+v, want %+v", c, offline(oldSHA))
		}
		if n := hits.Load(); n != 1 {
			t.Errorf("forge asked %d times, want 1", n)
		}
		if commitExists(parent, newSHA) {
			t.Error("the forge's head was fetched although the offline answer stands")
		}
	}
	t.Run("forge has the same merge", func(t *testing.T) {
		parent, oldSHA, newSHA := setup(t)
		hits := fakeGitHub(t, map[string][]map[string]any{"o:reused": {merged(7, oldSHA, "2026-09-10T00:00:00Z")}})
		keepsOffline(t, parent, oldSHA, newSHA, hits)
	})
	t.Run("forge older", func(t *testing.T) {
		parent, oldSHA, newSHA := setup(t)
		hits := fakeGitHub(t, map[string][]map[string]any{"o:reused": {merged(9, newSHA, "2026-09-01T00:00:00Z")}})
		keepsOffline(t, parent, oldSHA, newSHA, hits)
	})
	t.Run("forge merged_at unreadable", func(t *testing.T) {
		parent, oldSHA, newSHA := setup(t)
		hits := fakeGitHub(t, map[string][]map[string]any{"o:reused": {merged(9, newSHA, "yesterday")}})
		keepsOffline(t, parent, oldSHA, newSHA, hits)
	})
	t.Run("forge error", func(t *testing.T) {
		parent, oldSHA, newSHA := setup(t)
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		fakeGitHub(t, nil)
		githubAPIBase = srv.URL
		keepsOffline(t, parent, oldSHA, newSHA, &hits)
	})
	t.Run("no connection", func(t *testing.T) {
		parent, oldSHA, newSHA := setup(t)
		hits := fakeGitHub(t, map[string][]map[string]any{"o:reused": {merged(9, newSHA, "2026-09-20T00:00:00Z")}})
		githubToken = func() string { return "" }
		if c := resolve(t, parent); c != offline(oldSHA) {
			t.Errorf("candidate = %+v, want %+v", c, offline(oldSHA))
		}
		if hits.Load() != 0 {
			t.Error("the forge was asked without a token")
		}
	})
}

// The latest merge is chosen across pages: a later page can hold a pull request merged after
// every one on the first, and an unreadable merged_at ranks below any readable one.
func TestGitHubMergedPRPicksLatestMergeAcrossPages(t *testing.T) {
	late := strings.Repeat("a7", 20)
	var list []map[string]any
	for i := 0; i < githubPullsPerPage; i++ {
		p := pullJSON(1000-i, "sq", strings.Repeat("0e", 20), true)
		p["merged_at"] = "2026-09-01T00:00:00Z"
		if i == 0 {
			p["merged_at"] = "not a date"
		}
		list = append(list, p)
	}
	p := pullJSON(10, "sq", late, true)
	p["merged_at"] = "2026-09-20T00:00:00Z"
	list = append(list, p)
	hits := fakeGitHub(t, map[string][]map[string]any{"o:sq": list})

	sha, pr, at := githubMergedPR(context.Background(), "tok", "o/r", "sq")
	if sha != late || pr != 10 || !at.Equal(time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("githubMergedPR = %s, %d, %v; want %s, 10, 2026-09-20", sha, pr, at, late)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("pages asked = %d, want 2", n)
	}
}
