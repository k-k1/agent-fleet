package memoryx

// One lint --apply run at a time (ADR 0130 decision 5), whoever starts it: the member's
// `af-memory lint --apply` (a separate process, but it only posts to the Agent's lint-apply route,
// so the write happens here) and the scheduled run of internal/memlintsched both go through
// agentMemLintRunMu. Two overlapping runs would each compute findings from the same revisions and
// race for the compare-and-swap; nothing would be corrupted, but one run's work would be thrown
// away as "moved" and the commits would interleave. The manual run is refused rather than queued:
// a run can hold the lock for minutes, and the CLI would otherwise hang on it.

import (
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"
)

var agentMemLintRunMu sync.Mutex

// ErrLintRunning is returned when another lint --apply run holds the run lock.
var ErrLintRunning = errors.New("a lint run is already in progress")

// agentMemLintRunning is the member-facing form of ErrLintRunning.
func agentMemLintRunning() error {
	return memoryErrf(http.StatusConflict, errCodeMemoryConflict, "a lint --apply run is already in progress; try again when it has finished")
}

// LintApplyAllOut is what one scheduled run did, summed over the scopes it covered.
type LintApplyAllOut struct {
	Scopes   int
	Marked   int
	Renamed  int
	Changed  int
	Deferred int
	// Stopped is set when the run left scopes unvisited because Continue said stop.
	Stopped bool
}

// LintApplyAll runs lint --apply over the user scope and every project that holds memory, with one
// write budget (agentMemLintApplyMax) for the whole run: what does not fit is Deferred and the
// next run continues. cont is asked before each scope and false stops the run there (the
// workspace became busy). It returns ErrLintRunning, having done nothing, when a run is in flight.
// A failing scope ends the run with its error; the scopes done before it keep their writes.
func LintApplyAll(now time.Time, cont func() bool) (LintApplyAllOut, error) {
	var out LintApplyAllOut
	if !agentMemLintRunMu.TryLock() {
		return out, ErrLintRunning
	}
	defer agentMemLintRunMu.Unlock()
	if AgentMemoryEnabled == nil || !AgentMemoryEnabled() {
		return out, nil
	}
	type target struct{ scope, project string }
	targets := []target{{agentMemScopeUser, ""}}
	projs, err := agentMemMemberProjects()
	if err != nil {
		return out, err
	}
	var ids []string
	for _, r := range projs.Projects {
		if r.Count > 0 {
			ids = append(ids, r.Project.ID)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		targets = append(targets, target{agentMemScopeProject, id})
	}
	budget := agentMemLintApplyMax
	for _, t := range targets {
		if cont != nil && !cont() {
			out.Stopped = true
			return out, nil
		}
		o, err := agentMemLintApplyBudget(t.scope, t.project, now, &budget)
		out.Marked += o.Marked
		out.Renamed += o.Renamed
		out.Changed += o.Changed
		out.Deferred += o.Deferred
		if err != nil {
			return out, err
		}
		out.Scopes++
	}
	return out, nil
}
