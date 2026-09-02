package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
)

// scheduleGuardErr blocks deleting a repo/worktree or a session that an ENABLED
// Schedule still references — the fix for the incident where a live cron's
// worktree was removed by a repo cleanup out from under it (docs/log/38). Pass "" for
// whichever of repoName/sessionName doesn't apply. A schedule's Repo is the
// absolute workspace-agent repo path (docs/log/38 P2: "dir" passthrough), so it is
// compared by basename against the repo/worktree {name}; ReuseTarget/ReuseSession
// already hold the literal session name. A store error fails OPEN — a lookup
// failure must not itself become a reason deletion is blocked.
func scheduleGuardErr(ctx context.Context, st Store, membershipID, repoName, sessionName string) error {
	if repoName == "" && sessionName == "" {
		return nil
	}
	schedules, err := st.ListSchedules(ctx, membershipID)
	if err != nil {
		return nil
	}
	for _, sc := range schedules {
		if !sc.Enabled {
			continue
		}
		label := sc.SpecLabel
		if label == "" {
			label = sc.ID
		}
		if repoName != "" && sc.Repo != "" && filepath.Base(filepath.Clean(sc.Repo)) == repoName {
			return fmt.Errorf("schedule %q still targets this repo/worktree — disable or delete the schedule first", label)
		}
		if sessionName != "" && (sc.ReuseTarget == sessionName || sc.ReuseSession == sessionName) {
			return fmt.Errorf("schedule %q reuses this session — disable or delete the schedule first", label)
		}
	}
	return nil
}

// scheduleDeleteGuard is scheduleGuardErr wired to the Console's proxied REST
// calls: it recognizes exactly DELETE /api/repos/{name} and DELETE
// /api/sessions/{name} (the routes that reach workspace agent's handleDeleteRepo /
// handleDeleteSession) and no-ops for anything else, including the more specific
// sub-routes like DELETE /api/repos/{name}/branch.
func scheduleDeleteGuard(ctx context.Context, st Store, membershipID string, r *http.Request) error {
	if r.Method != http.MethodDelete {
		return nil
	}
	name := r.PathValue("name")
	if name == "" {
		return nil
	}
	switch r.URL.Path {
	case "/api/repos/" + name:
		return scheduleGuardErr(ctx, st, membershipID, name, "")
	case "/api/sessions/" + name:
		return scheduleGuardErr(ctx, st, membershipID, "", name)
	default:
		return nil
	}
}
