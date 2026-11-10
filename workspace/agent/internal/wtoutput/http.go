package wtoutput

// HTTP surface for dropping regenerable build output from dormant worktrees (#3202, phase 2 of #3079).
//
//   GET  /cleanup/worktree-output?dormant_days=N      → every linked worktree: what would go, or why not
//   POST /cleanup/worktree-output?worktree=NAME&dry_run=1    → drop it from one worktree (dry_run reports only)
//
// The rules are in go. These deletes are not bundled to the trash, so they are a
// grade of their own ("regenerable"), never "safe" (sessionx/session_cleanup.go). Manual only:
// nothing here runs on a timer.

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// applyMu keeps two applies from racing on one tree.
var applyMu sync.Mutex

func liveDeps() Deps {
	return Deps{
		Locked: func(dir string) bool {
			return sessionx.RepoLocked(dir) || len(sessionx.LockedSessionsInDir(session.ListMetas(), dir)) > 0
		},
		LiveSessions: sessionx.LiveSessionsInDir,
		LastSession:  func(dir string) time.Time { return sessionx.LastActivityInDir(session.ListMetas(), dir) },
		Logf:         log.Printf,
	}
}

// dormantDays reads ?dormant_days=; absent means the default, anything else must be 1..3650.
func dormantDays(r *http.Request) (int, bool) {
	v := r.URL.Query().Get("dormant_days")
	if v == "" {
		return DefaultDormantDays, true
	}
	n, err := strconv.Atoi(v)
	return n, err == nil && n >= 1 && n <= 3650
}

type surveyAnswer struct {
	DormantDays int      `json:"dormant_days"`
	Worktrees   []Report `json:"worktrees"`
	MeasuredAt  string   `json:"measured_at"`
}

// HandleSurvey (GET /cleanup/worktree-output). Read-only.
func HandleSurvey(w http.ResponseWriter, r *http.Request) {
	days, ok := dormantDays(r)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_dormant_days", "dormant_days must be 1..3650")
		return
	}
	deps := liveDeps()
	out := surveyAnswer{DormantDays: days, Worktrees: []Report{}}
	budget := 3_000_000
	ents, _ := os.ReadDir(gitx.ReposRoot())
	for _, e := range ents {
		dir := filepath.Join(gitx.ReposRoot(), e.Name())
		if !e.IsDir() || !gitx.IsGitRepo(dir) || !gitx.IsLinkedWorktree(dir) {
			continue
		}
		out.Worktrees = append(out.Worktrees, Survey(r.Context(), deps, e.Name(), dir, days, &budget))
	}
	out.MeasuredAt = time.Now().UTC().Format(time.RFC3339)
	httpx.WriteJSON(w, http.StatusOK, out)
}

// HandleApply (POST /cleanup/worktree-output?worktree=NAME). 409 with the report when
// the worktree is refused or nothing could be removed.
func HandleApply(w http.ResponseWriter, r *http.Request) {
	dir, ok := gitx.ResolveRepoDir(r.URL.Query().Get("worktree"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid working copy name")
		return
	}
	if !gitx.IsGitRepo(dir) {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such working copy")
		return
	}
	days, ok := dormantDays(r)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_dormant_days", "dormant_days must be 1..3650")
		return
	}
	dry := r.URL.Query().Get("dry_run") == "1"
	applyMu.Lock()
	defer applyMu.Unlock()
	rep := Apply(context.WithoutCancel(r.Context()), liveDeps(), r.URL.Query().Get("worktree"), dir, days, dry)
	code := http.StatusOK
	if rep.Status == "refused" {
		code = http.StatusConflict
	}
	httpx.WriteJSON(w, code, rep)
}
