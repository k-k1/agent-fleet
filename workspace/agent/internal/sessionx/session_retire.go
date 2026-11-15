package sessionx

// Retiring a session (ADR 0137 §6). A stop alone does not end a session's work: its watches, its
// timers and its booked usage-limit resume start it again, and so does a built-in pull request
// subscription the Agent makes for it. Retire ends all of that for the session and for every
// session it started, then stops them with the ordinary, resumable stop.
//
// The order is the point. The mark goes on first, because the things that would undo a retire
// (a notice already claimed, a built-in made again by the next sweep, a timer's wake, the
// booked resume's time passing) are not stopped by ending what exists now; each of them asks
// retiredAndStopped before it starts anything. Ending the subscriptions comes second and the stop
// last, so nothing between them can start a session that is about to be stopped and find it
// stopped afterwards.
//
// The mark means "retired while stopped": a person starting the session (POST /start) clears it,
// and it is an ordinary session from then on.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

// retireCancelKeep is how long a cancelled resume's booking stays recorded as dead after the
// Control Plane confirmed the delete, so a fire already in flight is still refused
// (refuseCancelledResume).
const retireCancelKeep = 6 * time.Hour

// retiredAndStopped reports whether m is retired and not running. A retired session that is
// running was started by a person (or is being retired right now), and nothing here may
// interfere with it.
func retiredAndStopped(m session.Meta) bool {
	return m.RetiredAt != "" && !watchAlive(m)
}

// watchBarred is the engine's Barred hook: no subscription that could start a retired, stopped
// session is made for it.
func watchBarred(owner string) bool {
	m, ok := session.ReadMeta(owner)
	return ok && retiredAndStopped(m)
}

// clearRetired takes the mark off a session a person started.
func clearRetired(name string) {
	UpdateSessionMeta(name, func(m *session.Meta) bool {
		if m.RetiredAt == "" {
			return false
		}
		m.RetiredAt = ""
		return true
	})
}

// retireReport is one retired session in the answer.
type retireReport struct {
	Name  string      `json:"name"`
	Ended watch.Ended `json:"ended"`
	// ResumeCancelled is true when a booked usage-limit resume was cancelled.
	ResumeCancelled bool `json:"resumeCancelled"`
	Stopped         bool `json:"stopped"`
	// Unended is what could not be ended, each with the reason. The Control Plane keeps a
	// delivery's slot while this is not empty.
	Unended []string `json:"unended"`
}

// retireResp is POST /sessions/{name}/retire's answer. Sessions lists the session and all it
// started, the named one first. A repeated call lists the same sessions and ends nothing more.
type retireResp struct {
	Sessions []retireReport `json:"sessions"`
}

// HandleSessionRetire (POST /sessions/{name}/retire) ends everything the Agent holds that could
// start the session, and every session it started at any depth, and stops them all. It is
// reachable the way the other /sessions/{name}/ routes the Control Plane calls are, and has no
// MCP tool: a session must not be able to retire another.
func HandleSessionRetire(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	if _, ok := session.ReadMeta(name); !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, retireResp{Sessions: retireTree(r.Context(), name)})
}

// retireTree retires root and everything it started. The walk repeats until it finds nobody new:
// a child created while the retire was running is retired too. Each round marks all of its
// sessions before it ends anything, and stops them after.
func retireTree(ctx context.Context, root string) []retireReport {
	seen := map[string]*retireReport{}
	var order []*retireReport
	for round := 0; round < 5; round++ {
		names := []string{}
		if round == 0 {
			names = append(names, root)
		}
		for _, d := range spendDescendants(root, session.ListMetas()) {
			names = append(names, d.Name)
		}
		var fresh []*retireReport
		for _, n := range names {
			if seen[n] == nil {
				rep := &retireReport{Name: n, Unended: []string{}}
				seen[n] = rep
				order = append(order, rep)
				fresh = append(fresh, rep)
			}
		}
		if len(fresh) == 0 {
			break
		}
		for _, rep := range fresh {
			markRetired(rep)
		}
		for _, rep := range fresh {
			endHeld(ctx, rep)
		}
		for _, rep := range fresh {
			stopRetired(rep)
			// A session that was still running while its subscriptions were ended can have made
			// one more before the stop took it down; it is stopped now, so this is the last.
			endHeld(ctx, rep)
		}
	}
	out := make([]retireReport, 0, len(order))
	for _, rep := range order {
		out = append(out, *rep)
	}
	return out
}

// markRetired puts the mark on, keeping the first date when it is already there.
func markRetired(rep *retireReport) {
	UpdateSessionMeta(rep.Name, func(m *session.Meta) bool {
		if m.RetiredAt != "" {
			return false
		}
		m.RetiredAt = time.Now().UTC().Format(time.RFC3339)
		return true
	})
	if _, exists := session.ReadMeta(rep.Name); !exists {
		rep.Unended = append(rep.Unended, "the session is gone")
	}
}

// endHeld ends the subscriptions and the booked resume of one session.
func endHeld(ctx context.Context, rep *retireReport) {
	ended, unended, err := watchEngine().EndOwner(ctx, rep.Name)
	rep.Ended.Watches += ended.Watches
	rep.Ended.Timers += ended.Timers
	rep.Ended.Builtins += ended.Builtins
	rep.Unended = appendNew(rep.Unended, unended...)
	if err != nil {
		rep.Unended = appendNew(rep.Unended, "subscriptions: "+err.Error())
	}
	cancelled, left := cancelBookedResume(rep.Name)
	rep.ResumeCancelled = rep.ResumeCancelled || cancelled
	rep.Unended = appendNew(rep.Unended, left...)
}

func appendNew(list []string, add ...string) []string {
	for _, a := range add {
		if !slices.Contains(list, a) {
			list = append(list, a)
		}
	}
	return list
}

// cancelBookedResume cancels the usage-limit resume the session has booked at the Control Plane
// and closes its episode, so the row reports no rateLimitResumeAt and the sweep books nothing
// for a stopped session. An id whose delete the Control Plane has not confirmed stays recorded
// for the sweep to retry (rate_limit_cleanup.go), and a fire of it that comes first is refused
// (refuseCancelledResume); it is reported here as not ended.
func cancelBookedResume(name string) (cancelled bool, unended []string) {
	mu := rateLimitEpisodeLock(name)
	mu.Lock()
	defer mu.Unlock()
	st, has := RateLimitStates.Read(name)
	if !has {
		return false, nil
	}
	for _, id := range []string{st.ScheduleID, st.MovedFrom} {
		if id == "" {
			continue
		}
		if err := recordRateLimitCancelKeep(name, id, retireCancelKeep); err != nil {
			unended = append(unended, fmt.Sprintf("usage-limit resume %s: cancellation could not be recorded: %v", id, err))
			_ = cancelRateLimitSchedule(name, id)
			continue
		}
		finishRateLimitCancel(id)
		if _, pending := pendingRateLimitCleanups()[id]; pending {
			unended = append(unended, "usage-limit resume "+id+": the Control Plane did not confirm the cancel; it is retried")
		} else {
			cancelled = true
		}
	}
	if len(unended) == 0 {
		RateLimitStates.Remove(name)
	}
	return cancelled, unended
}

// stopRetired stops one session with the ordinary, resumable stop.
func stopRetired(rep *retireReport) {
	m, ok := session.ReadMeta(rep.Name)
	if !ok {
		return
	}
	if _, err := haltSessionMeta(m); err != nil {
		rep.Unended = append(rep.Unended, "stop: "+err.Error())
		return
	}
	rep.Stopped = true
}
