package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// resetRepoJobs clears the process-wide registry between tests (it is global by
// design — one Agent, one set of imports).
func resetRepoJobs(t *testing.T) {
	t.Helper()
	repoJobs.mu.Lock()
	repoJobs.m = map[string]*repoJobEntry{}
	repoJobs.seq = 0
	repoJobs.swpt = false
	repoJobs.mu.Unlock()
}

// waitRepoJob polls the registry until id leaves running.
func waitRepoJob(t *testing.T, id string) RepoJob {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, j := range listRepoJobs() {
			if j.ID == id && j.State != repoJobRunning {
				return j
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s never left %q", id, repoJobRunning)
	return RepoJob{}
}

// A folder still being imported is not a working copy. Listing one made launch / update /
// svn status run against a checkout in flight, which produced E155037 and E200033
// (docs/log/78).
func TestRepoJobHidesWorkingCopyWhileRunning(t *testing.T) {
	resetRepoJobs(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(gitx.ReposRoot(), "importing")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	job := startRepoJob("git", "importing", dir, "https://example.invalid/x.git",
		func(ctx context.Context, sink *repoJobSink) error {
			<-release
			return nil
		})

	if !repoJobActive("importing") {
		t.Fatal("repoJobActive = false while the job runs")
	}
	if n := repoJobsRunning(); n != 1 {
		t.Fatalf("repoJobsRunning = %d, want 1 (this is what holds off CP's idle-stop)", n)
	}
	if names := listedRepoNames(t); len(names) != 0 {
		t.Errorf("GET /repos listed %v while importing; want none", names)
	}

	close(release)
	done := waitRepoJob(t, job.ID)
	if done.State != repoJobDone {
		t.Fatalf("state = %q, want %q (err=%q)", done.State, repoJobDone, done.Error)
	}
	if repoJobActive("importing") {
		t.Error("repoJobActive still true after the job finished")
	}
	if names := listedRepoNames(t); len(names) != 1 || names[0] != "importing" {
		t.Errorf("GET /repos = %v, want [importing] once the import finished", names)
	}
}

// listedRepoNames drives the real GET /repos handler.
func listedRepoNames(t *testing.T) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	gitx.HandleListRepos(rec, httptest.NewRequest("GET", "/repos", nil))
	var repos []gitx.Repo
	if err := json.Unmarshal(rec.Body.Bytes(), &repos); err != nil {
		// The handler may wrap; fall back to an envelope shape.
		var env struct {
			Repos []gitx.Repo `json:"repos"`
		}
		if err2 := json.Unmarshal(rec.Body.Bytes(), &env); err2 != nil {
			t.Fatalf("decode repos: %v / %v: %s", err, err2, rec.Body.String())
		}
		repos = env.Repos
	}
	out := []string{}
	for _, r := range repos {
		out = append(out, r.Name)
	}
	return out
}

func TestRepoJobCancelAndDismiss(t *testing.T) {
	resetRepoJobs(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(gitx.ReposRoot(), "slow")

	job := startRepoJob("svn", "slow", dir, "https://example.invalid/svn",
		func(ctx context.Context, sink *repoJobSink) error {
			<-ctx.Done()
			return errors.New("killed: " + ctx.Err().Error())
		})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/repo-jobs/"+job.ID, nil)
	req.SetPathValue("id", job.ID)
	handleDeleteRepoJob(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	got := waitRepoJob(t, job.ID)
	if got.State != repoJobCanceled {
		t.Fatalf("state = %q, want %q", got.State, repoJobCanceled)
	}
	// A canceled job stays on the list as a record: vanishing before anyone reads its outcome
	// is how it becomes "it just silently failed" again.
	if len(listRepoJobs()) != 1 {
		t.Fatalf("canceled job disappeared from the list: %+v", listRepoJobs())
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("DELETE", "/repo-jobs/"+job.ID, nil)
	req.SetPathValue("id", job.ID)
	handleDeleteRepoJob(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dismiss = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(listRepoJobs()) != 0 {
		t.Fatalf("dismiss left %+v", listRepoJobs())
	}
}

// The Agent does die mid-import (an ECS task replacement, idle-stop). Without the marker,
// only a half-finished working copy comes back to the list wearing an ordinary repository
// face - the original incident all over again.
func TestRepoJobMarkerSurvivesRestart(t *testing.T) {
	resetRepoJobs(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(gitx.ReposRoot(), "halfway")
	if err := os.MkdirAll(filepath.Join(dir, ".svn"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepoJobMarker(RepoJob{ID: "rj-old", Kind: "svn", Name: "halfway", URL: "https://example.invalid/svn", StartedAt: "2026-08-26T00:00:00Z"})

	sweepRepoJobMarkers()

	jobs := listRepoJobs()
	if len(jobs) != 1 || jobs[0].State != repoJobInterrupted {
		t.Fatalf("jobs = %+v, want one interrupted", jobs)
	}
	if !jobs[0].Kept {
		t.Error("Kept = false; the half-written svn working copy is still on disk and resumable")
	}
	if _, err := os.Stat(repoJobMarkerPath("halfway")); !os.IsNotExist(err) {
		t.Error("marker not cleared after the sweep (it would report the same interruption forever)")
	}
	// Interrupted is not running: as a working copy it comes back to the list and can be
	// updated or deleted.
	if repoJobActive("halfway") {
		t.Error("an interrupted job must not block the folder")
	}
}

func TestRepoJobSinkCountsLines(t *testing.T) {
	s := &repoJobSink{}
	// git overwrites progress with \r. Unless those count as lines, a clone's progress sits at
	// one item and never moves.
	_, _ = s.Write([]byte("A  a/b.txt\nA  a/c.txt\nReceiving objects:  10%\rReceiving objects:  90%\r"))
	items, progress := s.snapshot()
	if items != 4 {
		t.Errorf("items = %d, want 4", items)
	}
	if !strings.Contains(progress, "90%") {
		t.Errorf("progress = %q, want the latest line", progress)
	}
	if !strings.Contains(s.tailString(), "a/b.txt") {
		t.Errorf("tail lost the earlier output: %q", s.tailString())
	}
}
