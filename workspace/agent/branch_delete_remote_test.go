package main

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeleteBranchRemote drives DELETE /repos/{name}/branch against a real bare origin on
// disk, because the whole point of the remote=1 flag is the half no unit test with a fake
// git can see: whether the ref on the OTHER side is gone, and whether it survives the cases
// where it must.
//
// The origin is a bare repo in a temp dir, so no network and no credentials are involved —
// what is under test is the ordering and the opt-in, not the transport.
func TestDeleteBranchRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)

	origin := filepath.Join(root, "origin.git")
	gitAt(t, root, "init", "--bare", "-b", "main", origin)
	seed := filepath.Join(root, "seed")
	gitInit(t, seed)
	gitAt(t, seed, "remote", "add", "origin", origin)
	gitAt(t, seed, "push", "-u", "origin", "main")

	repo := filepath.Join(root, "repos", "app")
	gitAt(t, root, "clone", origin, repo)

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /repos/{name}/branch", handleDeleteBranch)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// merged makes a branch whose commits end up in main, which is what `git branch -d`
	// requires; pushed decides whether origin ever hears about it.
	merged := func(branch string, pushed bool) {
		t.Helper()
		gitAt(t, repo, "checkout", "-b", branch)
		commitIntegrationFile(t, repo, strings.ReplaceAll(branch, "/", "-")+".txt")
		if pushed {
			gitAt(t, repo, "push", "-u", "origin", branch)
		}
		gitAt(t, repo, "checkout", "main")
		gitAt(t, repo, "merge", "--no-ff", "-m", "merge "+branch, branch)
	}
	onOrigin := func(branch string) bool {
		t.Helper()
		return strings.Contains(gitAt(t, repo, "ls-remote", "--heads", "origin", branch), "refs/heads/"+branch)
	}
	localBranches := func() string { return gitAt(t, repo, "branch", "--format=%(refname:short)") }

	// ① remote=1 on a pushed, merged branch: both sides go.
	merged("temp/pushed", true)
	if !onOrigin("temp/pushed") {
		t.Fatal("precondition: temp/pushed is not on origin")
	}
	var res struct {
		Deleted     string `json:"deleted"`
		Remote      string `json:"remote"`
		RemoteError string `json:"remote_error"`
	}
	do(t, srv, "DELETE", "/repos/app/branch?branch=temp%2Fpushed&remote=1", nil, http.StatusOK, &res)
	if res.Remote != "deleted" || res.RemoteError != "" {
		t.Errorf("remote = %q (err %q), want deleted", res.Remote, res.RemoteError)
	}
	if onOrigin("temp/pushed") {
		t.Error("temp/pushed still on origin after remote=1")
	}
	if strings.Contains(localBranches(), "temp/pushed") {
		t.Error("temp/pushed still local")
	}

	// ② A branch that was never pushed is not a failure — "absent", and the local delete
	// still happened. This is the ordinary worktree branch, so an error here would put a
	// red line in front of the user on the common path.
	merged("temp/local", false)
	res.Remote, res.RemoteError = "", ""
	do(t, srv, "DELETE", "/repos/app/branch?branch=temp%2Flocal&remote=1", nil, http.StatusOK, &res)
	if res.Remote != "absent" || res.RemoteError != "" {
		t.Errorf("unpushed branch: remote = %q (err %q), want absent", res.Remote, res.RemoteError)
	}
	if strings.Contains(localBranches(), "temp/local") {
		t.Error("temp/local still local")
	}

	// ③ WITHOUT the flag the remote ref must survive — the positive control for ①: without
	// it, an origin that never had the branch would make ① pass for the wrong reason.
	merged("temp/keep", true)
	res.Remote, res.RemoteError = "", ""
	do(t, srv, "DELETE", "/repos/app/branch?branch=temp%2Fkeep", nil, http.StatusOK, &res)
	if res.Remote != "" {
		t.Errorf("remote = %q without the flag, want it empty", res.Remote)
	}
	if !onOrigin("temp/keep") {
		t.Error("temp/keep was removed from origin without remote=1")
	}

	// ④ The trap this flag had to grow a guard for, and the reason the guard is not
	// `git branch -d`: a branch that was PUSHED but never merged is merged *into its
	// upstream*, which is all `-d` asks for (measured — without the ancestor check this
	// request returned 200 and took both refs). remote=1 must refuse it, and refuse it
	// before either side is touched.
	gitAt(t, repo, "checkout", "-b", "temp/unmerged")
	commitIntegrationFile(t, repo, "unmerged.txt")
	gitAt(t, repo, "push", "-u", "origin", "temp/unmerged")
	gitAt(t, repo, "checkout", "main")
	code, body := roundtrip(t, srv, "DELETE", "/repos/app/branch?branch=temp%2Funmerged&remote=1", nil)
	if code != http.StatusConflict {
		t.Fatalf("unmerged delete = %d (%s), want 409", code, body)
	}
	if got := errPayload(t, body); got["code"] != "branch_not_in_head" {
		t.Errorf("error code = %q, want branch_not_in_head", got["code"])
	}
	if !onOrigin("temp/unmerged") {
		t.Error("the remote branch was deleted even though its commits are in no other history")
	}
	if !strings.Contains(localBranches(), "temp/unmerged") {
		t.Error("unmerged branch was deleted locally")
	}
	// Local-only delete keeps its older, looser contract: git allows it because the commits
	// are still on origin. Pinned here so the stricter test above is not "simplified" into
	// the shared path, which would change what the Cleanup modal's rows do.
	res.Remote, res.RemoteError = "", ""
	do(t, srv, "DELETE", "/repos/app/branch?branch=temp%2Funmerged", nil, http.StatusOK, &res)
	if !onOrigin("temp/unmerged") {
		t.Error("a local-only delete removed the remote ref")
	}
}
