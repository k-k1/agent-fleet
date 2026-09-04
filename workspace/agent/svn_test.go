package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func TestDeriveSvnName(t *testing.T) {
	cases := map[string]string{
		"https://host/svn/myrepo/trunk":        "myrepo", // bare trunk → repo name
		"https://host/svn/myrepo/trunk/":       "myrepo",
		"https://host/svn/myrepo/branches/x":   "x",
		"https://host/svn/myrepo":              "myrepo",
		"https://host/svn/myrepo/tags/rel-1.0": "rel-1.0",
		"https://host/a/b/c":                   "c",
		"":                                     "",
	}
	for in, want := range cases {
		if got := deriveSvnName(in); got != want {
			t.Errorf("deriveSvnName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSvnBuildURL(t *testing.T) {
	cases := []struct{ base, sub, want string }{
		{"https://host/svn/repo", "", "https://host/svn/repo"},
		{"https://host/svn/repo/", "trunk", "https://host/svn/repo/trunk"},
		{"https://host/svn/repo", "/trunk/sub/", "https://host/svn/repo/trunk/sub"},
		{"  https://host/svn/repo  ", " trunk ", "https://host/svn/repo/trunk"},
	}
	for _, c := range cases {
		if got := svnBuildURL(c.base, c.sub); got != c.want {
			t.Errorf("svnBuildURL(%q,%q) = %q, want %q", c.base, c.sub, got, c.want)
		}
	}
}

// TestResolveRepoDirUnicode locks in that a folder name may be Japanese (or any
// Unicode letters/numbers) — an SVN checkout target can be named 日本語プロジェクト —
// while path traversal and other unsafe names stay rejected.
func TestResolveRepoDirUnicode(t *testing.T) {
	ok := []string{"日本語プロジェクト", "repo", "repo-2", "my.repo", "x@feat-1", "数字123", "café"}
	for _, name := range ok {
		if dir, valid := gitx.ResolveRepoDir(name); !valid {
			t.Errorf("gitx.ResolveRepoDir(%q) rejected a valid name", name)
		} else if filepath.Base(dir) != name {
			t.Errorf("gitx.ResolveRepoDir(%q) mapped to %q", name, dir)
		}
	}
	bad := []string{"", "..", "../evil", "a/b", ".hidden", "-flag", "@at", "a b", "sub\x00null"}
	for _, name := range bad {
		if _, valid := gitx.ResolveRepoDir(name); valid {
			t.Errorf("gitx.ResolveRepoDir(%q) accepted an unsafe name", name)
		}
	}
}

func TestSvnLocked(t *testing.T) {
	locked := []string{
		"svn: E155004: Working copy '/x' locked",
		"svn: run 'svn cleanup' to remove locks",
		"is already locked",
		// What svn 1.14 actually prints after an interrupted import (measured). It writes
		// `cleanup`, not `svn cleanup`, so while only the E155004 wording was matched this
		// slipped straight through and the automatic repair never ran once - users had to
		// unlock by hand every time.
		"svn: E155037: Previous operation has not finished; run 'cleanup' if it was interrupted",
	}
	for _, s := range locked {
		if !svnLocked(s) {
			t.Errorf("svnLocked(%q) = false, want true", s)
		}
	}
	if svnLocked("svn: E170013: Unable to connect") {
		t.Error("svnLocked matched an unrelated error")
	}
}

func TestPickSvnCred(t *testing.T) {
	list := []secrets.SVNCred{
		{URLPrefix: "https://host/svn", Username: "broad"},
		{URLPrefix: "https://host/svn/repo", Username: "narrow"},
		{URLPrefix: "https://other", Username: "other"},
	}
	if c := pickSvnCred(list, "https://host/svn/repo/trunk"); c == nil || c.Username != "narrow" {
		t.Errorf("longest-prefix match failed: %+v", c)
	}
	if c := pickSvnCred(list, "https://host/svn/elsewhere"); c == nil || c.Username != "broad" {
		t.Errorf("broad match failed: %+v", c)
	}
	if c := pickSvnCred(list, "https://nomatch/x"); c != nil {
		t.Errorf("expected no match, got %+v", c)
	}
}

func TestSvnAuthedArgs(t *testing.T) {
	has := func(a []string, s string) bool {
		for _, x := range a {
			if x == s {
				return true
			}
		}
		return false
	}
	// No creds: base flags only, no auth, no trust.
	got, authed := svnAuthedArgs(nil, "checkout", "url", "dir")
	if authed || has(got, svnTrustFailures) || has(got, "--username") {
		t.Errorf("nil creds should add no auth/trust: %v authed=%v", got, authed)
	}
	// Trust only (no username): trust flag present, still no --username / not authed.
	got, authed = svnAuthedArgs(&secrets.SVNCred{TrustCert: true}, "update", "dir")
	if authed || !has(got, svnTrustFailures) || has(got, "--username") {
		t.Errorf("trust-only creds wrong: %v authed=%v", got, authed)
	}
	// Auth + trust: both wired, password never in argv.
	got, authed = svnAuthedArgs(&secrets.SVNCred{Username: "u", Password: "secret", TrustCert: true}, "checkout", "url", "dir")
	if !authed || !has(got, svnTrustFailures) || !has(got, "--username") || !has(got, "--password-from-stdin") || has(got, "secret") {
		t.Errorf("auth+trust wrong: %v authed=%v", got, authed)
	}
	// Order: subcommand args come last (svn wants global options before the subcommand).
	if got[len(got)-3] != "checkout" || got[len(got)-1] != "dir" {
		t.Errorf("subcommand args not trailing: %v", got)
	}
}

// TestSvnCheckoutFileRepo exercises the svn runner + info/dirty/cleanup against a
// local file:// repository — no network, no auth, no secrets store. Skipped when
// svn/svnadmin are absent (e.g. the native runtime relies on host tools).
func TestSvnCheckoutFileRepo(t *testing.T) {
	if !svnAvailable() {
		t.Skip("svn not installed")
	}
	if _, err := exec.LookPath("svnadmin"); err != nil {
		t.Skip("svnadmin not installed")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "srv")
	if out, err := exec.Command("svnadmin", "create", repo).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v: %s", err, out)
	}
	// Seed one file via a throwaway import dir.
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repoURL := "file://" + repo
	if out, err := exec.Command("svn", "import", "--non-interactive", "-m", "seed", seed, repoURL+"/trunk").CombinedOutput(); err != nil {
		t.Fatalf("svn import: %v: %s", err, out)
	}

	wc := filepath.Join(root, "wc")
	out, err := runSvnAuthedHealing(context.Background(), wc, nil, "checkout", repoURL+"/trunk", wc)
	if err != nil {
		t.Fatalf("checkout: %v: %s", err, out)
	}
	if !isSvnRepo(wc) {
		t.Fatal("expected an svn working copy after checkout")
	}
	if rev, url := svnInfo(wc); rev == "" || url == "" {
		t.Fatalf("svnInfo empty: rev=%q url=%q", rev, url)
	}
	if svnDirty(wc) {
		t.Error("fresh checkout should not be dirty")
	}
	if err := os.WriteFile(filepath.Join(wc, "hello.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !svnDirty(wc) {
		t.Error("modified working copy should be dirty")
	}
	if out, err := svnCleanup(wc); err != nil {
		t.Fatalf("cleanup: %v: %s", err, out)
	}
}

// TestSvnCheckoutIsAsync checks the asynchronous POST /repos/svn (docs/log/78) against a real
// svn. Three things: (1) the handler does not wait for the checkout to finish, it returns 202
// and a job, (2) a folder still being imported does not appear in GET /repos, and (3) it shows
// up as a working copy only once the job reaches done. While this was synchronous, "a response
// came back" meant "the proxy gave up" rather than "it finished", and a checkout still running
// was listed as already imported.
func TestSvnCheckoutIsAsync(t *testing.T) {
	if !svnAvailable() {
		t.Skip("svn not installed")
	}
	if _, err := exec.LookPath("svnadmin"); err != nil {
		t.Skip("svnadmin not installed")
	}
	resetRepoJobs(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := filepath.Join(t.TempDir(), "srv")
	if out, err := exec.Command("svnadmin", "create", srv).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v: %s", err, out)
	}
	seed := filepath.Join(t.TempDir(), "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repoURL := "file://" + srv
	if out, err := exec.Command("svn", "import", "--non-interactive", "-m", "seed", seed, repoURL+"/trunk").CombinedOutput(); err != nil {
		t.Fatalf("svn import: %v: %s", err, out)
	}

	body := strings.NewReader(`{"url":"` + repoURL + `","subpath":"trunk","name":"docs"}`)
	rec := httptest.NewRecorder()
	handleSvnCheckout(rec, httptest.NewRequest("POST", "/repos/svn", body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Job RepoJob `json:"job"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	if res.Job.ID == "" || res.Job.State != repoJobRunning || res.Job.Kind != "svn" {
		t.Fatalf("job = %+v, want a running svn job", res.Job)
	}

	done := waitRepoJob(t, res.Job.ID)
	if done.State != repoJobDone {
		t.Fatalf("state = %q, want %q (err=%q)", done.State, repoJobDone, done.Error)
	}
	wc := filepath.Join(gitx.ReposRoot(), "docs")
	if !isSvnRepo(wc) {
		t.Fatal("no working copy after the job finished")
	}
	if names := listedRepoNames(t); len(names) != 1 || names[0] != "docs" {
		t.Fatalf("GET /repos = %v, want [docs]", names)
	}
}

// TestSvnCheckoutFailureKeepsResumableCopy holds that a resumable working copy survives a
// failure (docs/log/78). Back when the run was killed at the 30-minute cap and then RemoveAll'd,
// a working copy that had downloaded for tens of minutes vanished silently - svn can pick up
// where it left off with cleanup + update, so deleting is only allowed when nothing became a
// working copy.
func TestSvnCheckoutFailureKeepsResumableCopy(t *testing.T) {
	if !svnAvailable() {
		t.Skip("svn not installed")
	}
	resetRepoJobs(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A repository URL that does not exist: the checkout fails without creating a working copy,
	// so the leftovers are cleaned up.
	body := strings.NewReader(`{"url":"file:///nonexistent-svn-repo","name":"ghost"}`)
	rec := httptest.NewRecorder()
	handleSvnCheckout(rec, httptest.NewRequest("POST", "/repos/svn", body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Job RepoJob `json:"job"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	done := waitRepoJob(t, res.Job.ID)
	if done.State != repoJobFailed {
		t.Fatalf("state = %q, want %q", done.State, repoJobFailed)
	}
	if done.Error == "" {
		t.Error("failure body is empty: with svn's own words gone there is nothing left to investigate with")
	}
	if done.Kept {
		t.Error("Kept = true although no working copy was ever created")
	}
	if _, err := os.Stat(filepath.Join(gitx.ReposRoot(), "ghost")); !os.IsNotExist(err) {
		t.Error("leftovers that never became a working copy should be cleaned up")
	}
}

// The dirty verdict in the list walks the whole working copy. On an 11.4 GB working copy (a
// real one) that can hold GET /repos, so concurrent requests for the same folder ride on a
// single walk - otherwise every interaction with the screen piles up another full walk,
// recreating by ourselves the same fight over wc.db that a running checkout caused.
// docs/log/78.
func TestSvnDirtySharesOneScan(t *testing.T) {
	if !svnAvailable() {
		t.Skip("svn not installed")
	}
	if _, err := exec.LookPath("svnadmin"); err != nil {
		t.Skip("svnadmin not installed")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "srv")
	if out, err := exec.Command("svnadmin", "create", repo).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v: %s", err, out)
	}
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("svn", "import", "--non-interactive", "-m", "seed", seed, "file://"+repo).CombinedOutput(); err != nil {
		t.Fatalf("svn import: %v: %s", err, out)
	}
	wc := filepath.Join(root, "wc")
	if out, err := runSvnAuthed(context.Background(), nil, "checkout", "file://"+repo, wc); err != nil {
		t.Fatalf("checkout: %v: %s", err, out)
	}

	var wg sync.WaitGroup
	got := make([]bool, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = svnDirty(wc)
		}(i)
	}
	wg.Wait()
	for i, d := range got {
		if d {
			t.Fatalf("caller %d saw dirty on a fresh checkout", i)
		}
	}
	// This is not a cache, so a change is visible on the next call: a manual refresh must keep
	// working.
	if err := os.WriteFile(filepath.Join(wc, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !svnDirty(wc) {
		t.Error("the change did not show up in the next verdict (a TTL cache would make a manual refresh return a stale answer)")
	}
}
