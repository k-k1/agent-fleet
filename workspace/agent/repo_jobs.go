package main

// Repository import jobs (docs/log/78). Runs `git clone` / `svn checkout` as named jobs
// detached from the lifetime of the HTTP request, so progress and outcome stay observable
// through a separate API.
//
// Why it exists (the incident that was measured): a large import takes minutes to hours, but
// the ALB idle timeout is 60 seconds (deploy/aws/ecs/cfn/30-ingress.yaml). A synchronous POST
// always lost the response there, and the Console read that as "the folder exists, so it
// succeeded" — reporting a checkout that was still running as complete. Half-finished working
// copies then sat in the list as "imported", and an `svn update` against one fought the
// running checkout over the sqlite lock (E155037 / E200033). Past the 30-minute cap the
// failure path deleted the whole folder, so a working copy that had downloaded for tens of
// minutes vanished silently.
//
// Design:
//   - POST validates synchronously and answers 202 with the job; the network work runs in the
//     background.
//   - A folder being imported is not listed by `GET /repos`. Listing something unusable as a
//     working copy lets a session start in it, update it, or run `svn status` on it — all of
//     which race the running checkout.
//   - Progress is counted in lines. Buffering all of checkout/clone's output eats memory on a
//     huge repository, so only a counter, the last line and a tail ring are kept (the error
//     body uses the tail ring).
//   - A resumable working copy is never deleted on failure; svn continues with cleanup +
//     update.
//   - A marker on disk covers the agent dying (ECS task replacement, idle-stop) and taking the
//     job with it. A marker surviving startup means the import was interrupted; without
//     surfacing that as interrupted, a half-finished working copy comes back to the list as an
//     ordinary repository — the original incident again.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// Job states. Everything but running is terminal and stays in the list until the user
// dismisses it (a job that disappears before its outcome is read is "silently failed" again).
const (
	repoJobRunning     = "running"
	repoJobDone        = "done"
	repoJobFailed      = "failed"
	repoJobCanceled    = "canceled"
	repoJobInterrupted = "interrupted" // the agent restarted, taking the job with it (marker survived)
)

// repoJobDoneTTL is how long a successful job stays in the list. Success is announced by a
// Console toast as well, so it can be short. Failed and interrupted jobs have no TTL — they
// stay until the user dismisses them.
const repoJobDoneTTL = 10 * time.Minute

// repoJobTimeout caps the whole job. Killing at 30 minutes and deleting the working copy was
// the original incident, so this is not "how long a person will wait" but a value that only
// cuts off what is clearly broken: cancel exists for stopping one, and a running job is
// visible in the list.
const repoJobTimeout = 6 * time.Hour

// repoJobProgressMax is how much of a progress line is kept; svn paths can get long, so it is
// truncated.
const repoJobProgressMax = 240

// repoJobTailMax is the tail buffer used for the error body; the last few dozen lines of
// svn/git are enough to see the cause.
const repoJobTailMax = 8 << 10

// RepoJob is one import. The JSON becomes a Console row as-is.
type RepoJob struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`  // "git" | "svn"
	Name  string `json:"name"`  // folder name directly under ~/repos
	Path  string `json:"path"`  //
	URL   string `json:"url"`   // for display; never carries credentials
	State string `json:"state"` //

	// Progress is the last output line seen, Items the number of files fetched (svn's
	// "A path" / git's checkout lines). Both are approximations that exist only to show
	// movement: the total is unknown (neither svn nor git announces it up front).
	Progress string `json:"progress,omitempty"`
	Items    int    `json:"items,omitempty"`

	Error     string `json:"error,omitempty"`
	Kept      bool   `json:"kept,omitempty"` // failed, but the working copy was kept (resumable)
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt,omitempty"`
}

// repoJobEntry is one registry entry: the outward RepoJob plus cancel and mutable progress.
type repoJobEntry struct {
	job    RepoJob
	cancel func()
	sink   *repoJobSink
}

var repoJobs = struct {
	mu   sync.Mutex
	m    map[string]*repoJobEntry // id -> entry
	seq  int
	swpt bool // whether the startup marker sweep has run
}{m: map[string]*repoJobEntry{}}

// repoJobMarkerDir is where interruption markers live. Under ~/repos deliberately, so they
// share the working copies' lifetime (recreating the container drops ~/repos and the markers
// with it).
func repoJobMarkerDir() string { return filepath.Join(gitx.ReposRoot(), ".af-repo-jobs") }

func repoJobMarkerPath(name string) string {
	return filepath.Join(repoJobMarkerDir(), name+".json")
}

// repoJobMarker is the minimum kept on disk: only what it takes to put the job back in the
// list as interrupted.
type repoJobMarker struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	StartedAt string `json:"startedAt"`
}

func writeRepoJobMarker(j RepoJob) {
	if err := os.MkdirAll(repoJobMarkerDir(), 0o755); err != nil {
		return
	}
	b, err := json.Marshal(repoJobMarker{ID: j.ID, Kind: j.Kind, Name: j.Name, URL: j.URL, StartedAt: j.StartedAt})
	if err != nil {
		return
	}
	_ = os.WriteFile(repoJobMarkerPath(j.Name), b, 0o644)
}

func removeRepoJobMarker(name string) { _ = os.Remove(repoJobMarkerPath(name)) }

// sweepRepoJobMarkers runs once at startup and restores surviving markers as interrupted. It
// is the only way the user gets to see "the agent died, so the import died too": the job goes
// down with the process, so otherwise a half-finished working copy is all that silently
// remains.
func sweepRepoJobMarkers() {
	repoJobs.mu.Lock()
	if repoJobs.swpt {
		repoJobs.mu.Unlock()
		return
	}
	repoJobs.swpt = true
	repoJobs.mu.Unlock()

	entries, err := os.ReadDir(repoJobMarkerDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(repoJobMarkerDir(), e.Name()))
		if err != nil {
			continue
		}
		var m repoJobMarker
		if json.Unmarshal(b, &m) != nil || m.Name == "" {
			continue
		}
		dir, ok := gitx.ResolveRepoDir(m.Name)
		if !ok {
			continue
		}
		kept := gitx.IsGitRepo(dir) || isSvnRepo(dir)
		if _, err := os.Stat(dir); err != nil {
			// The folder itself is gone (the user deleted it, or we died before the import
			// started). Nobody is left to report the interruption to, so just clean up the
			// marker.
			removeRepoJobMarker(m.Name)
			continue
		}
		repoJobs.mu.Lock()
		repoJobs.m[m.ID] = &repoJobEntry{job: RepoJob{
			ID: m.ID, Kind: m.Kind, Name: m.Name, Path: dir, URL: m.URL,
			State: repoJobInterrupted, Kept: kept, StartedAt: m.StartedAt,
			EndedAt: time.Now().Format(time.RFC3339),
			Error:   "the workspace stopped (or the agent restarted) while this import was running",
		}}
		repoJobs.mu.Unlock()
		removeRepoJobMarker(m.Name)
	}
}

// repoJobSink is an io.Writer that folds the import command's output into a line count, the
// last line and a tail. Unlike CombinedOutput it keeps nothing else: a huge repository's
// checkout runs to hundreds of thousands of lines.
type repoJobSink struct {
	mu       sync.Mutex
	items    int
	progress string
	tail     []byte
	partial  []byte
}

func (s *repoJobSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tail = append(s.tail, p...)
	if len(s.tail) > repoJobTailMax {
		s.tail = append([]byte(nil), s.tail[len(s.tail)-repoJobTailMax:]...)
	}
	// git overwrites its progress with \r, so treat that as a separator like a newline.
	s.partial = append(s.partial, p...)
	for {
		i := bytesIndexAny(s.partial, "\n\r")
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(s.partial[:i]))
		s.partial = s.partial[i+1:]
		if line == "" {
			continue
		}
		s.items++
		if len(line) > repoJobProgressMax {
			line = line[:repoJobProgressMax]
		}
		s.progress = line
	}
	if len(s.partial) > repoJobProgressMax*4 {
		s.partial = s.partial[len(s.partial)-repoJobProgressMax:]
	}
	return len(p), nil
}

func (s *repoJobSink) snapshot() (items int, progress string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items, s.progress
}

func (s *repoJobSink) tailString() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(string(s.tail))
}

// bytesIndexAny is the first position of a newline character; the []byte version of
// strings.IndexAny (it only looks for single-byte characters).
func bytesIndexAny(b []byte, chars string) int {
	for i, c := range b {
		if strings.IndexByte(chars, c) >= 0 {
			return i
		}
	}
	return -1
}

// repoJobActive reports whether name is being imported right now. The gate for listing
// (GET /repos) and for deletion.
func repoJobActive(name string) bool {
	repoJobs.mu.Lock()
	defer repoJobs.mu.Unlock()
	for _, e := range repoJobs.m {
		if e.job.Name == name && e.job.State == repoJobRunning {
			return true
		}
	}
	return false
}

// repoJobsRunning is the number of running jobs. Reported on GET /sessions to tell the CP's
// idle-stop that this workspace is busy (stopping it mid-import corrupts the working copy).
func repoJobsRunning() int {
	repoJobs.mu.Lock()
	defer repoJobs.mu.Unlock()
	n := 0
	for _, e := range repoJobs.m {
		if e.job.State == repoJobRunning {
			n++
		}
	}
	return n
}

// listRepoJobs is the list for display: running jobs get their progress folded in, expired
// successes are dropped.
func listRepoJobs() []RepoJob {
	now := time.Now()
	repoJobs.mu.Lock()
	defer repoJobs.mu.Unlock()
	out := []RepoJob{}
	for id, e := range repoJobs.m {
		if e.job.State == repoJobDone {
			if t, err := time.Parse(time.RFC3339, e.job.EndedAt); err == nil && now.Sub(t) > repoJobDoneTTL {
				delete(repoJobs.m, id)
				continue
			}
		}
		j := e.job
		if e.sink != nil {
			j.Items, j.Progress = e.sink.snapshot()
		}
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].StartedAt > out[k].StartedAt })
	return out
}

// startRepoJob starts a validated import in the background. run is the network work itself:
// it must hook the given ctx to exec and stream the output into sink. The return value is the
// job's initial snapshot (the 202 body). ctx is capped by repoJobTimeout, and its cancel is
// reachable from DELETE.
func startRepoJob(kind, name, dir, url string, run func(ctx context.Context, sink *repoJobSink) error) RepoJob {
	ctx, cancel := context.WithTimeout(context.Background(), repoJobTimeout)
	repoJobs.mu.Lock()
	repoJobs.seq++
	id := fmt.Sprintf("rj%d-%d", time.Now().UnixNano(), repoJobs.seq)
	sink := &repoJobSink{}
	e := &repoJobEntry{
		job: RepoJob{
			ID: id, Kind: kind, Name: name, Path: dir, URL: url,
			State: repoJobRunning, StartedAt: time.Now().Format(time.RFC3339),
		},
		cancel: cancel,
		sink:   sink,
	}
	repoJobs.m[id] = e
	job := e.job
	repoJobs.mu.Unlock()

	writeRepoJobMarker(job)
	go func() {
		defer cancel()
		finishRepoJob(id, run(ctx, sink))
	}()
	return job
}

// finishRepoJob moves the job to a terminal state and drops the marker. Whether a failed run
// kept the working copy was already decided by run; here we only look at what is on disk and
// set Kept.
func finishRepoJob(id string, err error) {
	repoJobs.mu.Lock()
	e, ok := repoJobs.m[id]
	if !ok {
		repoJobs.mu.Unlock()
		return
	}
	e.job.EndedAt = time.Now().Format(time.RFC3339)
	if e.sink != nil {
		e.job.Items, e.job.Progress = e.sink.snapshot()
	}
	name, dir := e.job.Name, e.job.Path
	canceled := e.job.State == repoJobCanceled
	switch {
	case err == nil:
		e.job.State = repoJobDone
		e.job.Error = ""
	case canceled:
		e.job.Error = err.Error()
	default:
		e.job.State = repoJobFailed
		e.job.Error = err.Error()
	}
	if e.job.State != repoJobDone {
		e.job.Kept = gitx.IsGitRepo(dir) || isSvnRepo(dir)
	}
	repoJobs.mu.Unlock()
	removeRepoJobMarker(name)
}

// markRepoJobCanceled records the cancel request and kills the running process. The terminal
// transition itself happens in finishRepoJob when run returns (which is also where "delete or
// keep" is decided).
func markRepoJobCanceled(id string) bool {
	repoJobs.mu.Lock()
	e, ok := repoJobs.m[id]
	if !ok || e.job.State != repoJobRunning {
		repoJobs.mu.Unlock()
		return false
	}
	e.job.State = repoJobCanceled
	cancel := e.cancel
	repoJobs.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

// dismissRepoJob removes a terminated job from the list (the user saying they have read the
// outcome).
func dismissRepoJob(id string) bool {
	repoJobs.mu.Lock()
	defer repoJobs.mu.Unlock()
	e, ok := repoJobs.m[id]
	if !ok || e.job.State == repoJobRunning {
		return false
	}
	delete(repoJobs.m, id)
	return true
}

// handleListRepoJobs (GET /repos/jobs) — the progress and outcome of imports. The Console
// draws its "importing" rows from this, so the same thing is visible after closing the browser
// and in another tab.
func handleListRepoJobs(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"jobs": listRepoJobs()})
}

// handleDeleteRepoJob (DELETE /repos/jobs/{id}) — cancels a running job, removes a terminated
// one from the list.
func handleDeleteRepoJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if markRepoJobCanceled(id) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"canceled": id})
		return
	}
	if dismissRepoJob(id) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"dismissed": id})
		return
	}
	httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such repo job: "+id)
}
