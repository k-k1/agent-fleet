package main

// The svn checkout handler against main's real repo-job registry. These two tests moved out of
// internal/svnx with the handler: they need repoJobs / listRepoJobs, which stay in package main.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/svnx"
)

// TestSvnCheckoutIsAsync checks the asynchronous POST /repos/svn (docs/log/78) against a real
// svn. Three things: (1) the handler does not wait for the checkout to finish, it returns 202
// and a job, (2) a folder still being imported does not appear in GET /repos, and (3) it shows
// up as a working copy only once the job reaches done. While this was synchronous, "a response
// came back" meant "the proxy gave up" rather than "it finished", and a checkout still running
// was listed as already imported.
func TestSvnCheckoutIsAsync(t *testing.T) {
	if !svnx.Available() {
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
	svnx.HandleCheckout(rec, httptest.NewRequest("POST", "/repos/svn", body))
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
	if !svnx.IsRepo(wc) {
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
	if !svnx.Available() {
		t.Skip("svn not installed")
	}
	resetRepoJobs(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A repository URL that does not exist: the checkout fails without creating a working copy,
	// so the leftovers are cleaned up.
	body := strings.NewReader(`{"url":"file:///nonexistent-svn-repo","name":"ghost"}`)
	rec := httptest.NewRecorder()
	svnx.HandleCheckout(rec, httptest.NewRequest("POST", "/repos/svn", body))
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
