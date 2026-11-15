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
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

// retireCancelKeep is how long a cancelled resume's booking stays recorded as dead after the
// Control Plane confirmed the delete, so a fire already in flight is still refused
// (refuseCancelledResume).
const retireCancelKeep = 6 * time.Hour

// isRetired reports whether m carries the retired mark. From the moment it is set, nothing the
// Agent does on its own (a notice, a built-in, a timer, a resume, a spawn, a plan-review start)
// may start the session or put input into it; only a person's start clears it.
func isRetired(m session.Meta) bool { return m.RetiredAt != "" }

// watchBarred is the engine's Barred hook: no subscription that could start a retired session
// is made for it.
func watchBarred(owner string) bool {
	m, ok := session.ReadMeta(owner)
	return ok && isRetired(m)
}

// retireGates serialise a retire of one session against the starts that check the mark and then
// launch: such a start holds the read side from the check to the end of the launch, retire the
// write side from the mark to the stop. So a start either finished before the retire began (and
// the stop that follows takes it down) or begins after the mark and is refused. A person's start
// that overlaps a retire is held up until the retire is done; the mark is cleared only after
// its own launch, under the same read side.
var retireGates sync.Map // session name -> *sync.RWMutex

func retireGate(name string) *sync.RWMutex {
	v, _ := retireGates.LoadOrStore(name, &sync.RWMutex{})
	return v.(*sync.RWMutex)
}

// errSessionRetired is what an automatic start or input is refused with.
var errSessionRetired = errors.New("session_retired: this session was retired; resume it from the Console first")

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

type autoStartKey struct{}

// asAutomaticStart marks a request to HandleStartSession as one the Agent makes on its own.
func asAutomaticStart(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), autoStartKey{}, true))
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

// retireTree retires root and everything it started. The walk repeats until it finds nobody new,
// waiting for creates that already reserved a child slot to publish, so a child that was still
// being launched is found by the next round; a retired parent reserves no new slot.
func retireTree(ctx context.Context, root string) []retireReport {
	const maxRounds = 20
	seen := map[string]*retireReport{}
	var order []*retireReport
	for round := 0; ; round++ {
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
		if round >= maxRounds {
			order[0].Unended = appendNew(order[0].Unended, "the tree is deeper than the walk's limit; retire again")
			break
		}
		for _, rep := range fresh {
			retireOne(ctx, rep)
		}
		if !waitSpawns(ctx, order) {
			order[0].Unended = appendNew(order[0].Unended, "a child create was still in flight; retire again once it has finished")
		}
	}
	out := make([]retireReport, 0, len(order))
	for _, rep := range order {
		out = append(out, *rep)
	}
	return out
}

// retireOne retires one session under its gate: mark, end, stop, and end once more for what a
// still-running session made in between. Every step is safe to repeat, which is also what the
// start-up recovery (ReconcileRetired) relies on.
func retireOne(ctx context.Context, rep *retireReport) {
	g := retireGate(rep.Name)
	g.Lock()
	defer g.Unlock()
	markRetired(rep)
	endHeld(ctx, rep)
	stopRetired(rep)
	endHeld(ctx, rep)
}

// waitSpawns waits, within a bound, until none of the sessions has a create in flight, and
// reports whether it did.
func waitSpawns(ctx context.Context, reps []*retireReport) bool {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		spawnInflight.mu.Lock()
		busy := false
		for _, r := range reps {
			if spawnInflight.n[r.Name] > 0 {
				busy = true
			}
		}
		spawnInflight.mu.Unlock()
		if !busy {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ReconcileRetired finishes a retire the previous Agent process did not: the mark was set and the
// session still runs, or it was stopped and its subscriptions were not all ended yet. A marked
// session is by definition one nobody resumed (a person's start clears the mark).
func ReconcileRetired() int {
	n := 0
	for _, m := range session.ListMetas() {
		if m.Archived || !isRetired(m) {
			continue
		}
		retireOne(context.Background(), &retireReport{Name: m.Name, Unended: []string{}})
		n++
	}
	return n
}

// markRetired puts the mark on, keeping the first date when it is already there. The write is
// checked: a mark that did not reach the disk protects nothing, and is reported as not ended.
func markRetired(rep *retireReport) {
	_, _, err := updateSessionMetaChecked(rep.Name, func(m *session.Meta) bool {
		if m.RetiredAt != "" {
			return false
		}
		m.RetiredAt = time.Now().UTC().Format(time.RFC3339)
		return true
	})
	if err != nil {
		rep.Unended = appendNew(rep.Unended, "the retired mark could not be written: "+err.Error())
	}
	if _, exists := session.ReadMeta(rep.Name); !exists {
		rep.Unended = appendNew(rep.Unended, "the session is gone")
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
