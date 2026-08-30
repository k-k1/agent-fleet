package main

import (
	"context"
	"encoding/json"
	"errors"
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

// ★ 取り込み中のフォルダは「作業コピー」ではない。ここを一覧に出していたせいで、
// 走行中の checkout に対して 起動 / 更新 / svn status が掛かり、E155037・E200033 に
// なっていた（docs/log/78）。
func TestRepoJobHidesWorkingCopyWhileRunning(t *testing.T) {
	resetRepoJobs(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(reposRoot(), "importing")
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
		t.Fatalf("repoJobsRunning = %d, want 1 (CP はこれで idle-stop を止める)", n)
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
	handleListRepos(rec, httptest.NewRequest("GET", "/repos", nil))
	var repos []Repo
	if err := json.Unmarshal(rec.Body.Bytes(), &repos); err != nil {
		// The handler may wrap; fall back to an envelope shape.
		var env struct {
			Repos []Repo `json:"repos"`
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
	dir := filepath.Join(reposRoot(), "slow")

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
	// 中止したジョブは記録として残る（結末を読む前に消えると、また「黙って失敗した」になる）。
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

// ★ Agent は取り込みの途中で死ぬ（ECS のタスク入れ替え・idle-stop）。marker を残さないと
// 半端な作業コピーだけが普通のリポジトリ顔で一覧に戻る＝元の事故と同じ状態になる。
func TestRepoJobMarkerSurvivesRestart(t *testing.T) {
	resetRepoJobs(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(reposRoot(), "halfway")
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
	// 中断は「走行中」ではない: 作業コピーとしては一覧に戻り、更新 / 削除できる。
	if repoJobActive("halfway") {
		t.Error("an interrupted job must not block the folder")
	}
}

func TestRepoJobSinkCountsLines(t *testing.T) {
	s := &repoJobSink{}
	// git は \r で進捗を上書きする。行として数えないと、clone の進捗が 1 件のまま動かない。
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
