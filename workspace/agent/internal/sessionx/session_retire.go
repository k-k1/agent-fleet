package sessionx

// Retiring a session (ADR 0137 §6). A stop alone does not end a session's work: its watches, its
// timers and its booked usage-limit resume start it again, and so does a built-in pull request
// subscription the Agent makes for it. Retire ends all of that for the session and for every
// session it started, then stops them with the ordinary, resumable stop.
//
// The order is the point. The mark goes on first, because the things that would undo a retire
// (a notice already claimed, a built-in made again by the next sweep, a timer's wake, the
// booked resume's time passing, a Managed input, a child create in flight) are not stopped by
// ending what exists now; each of them checks the mark (isRetired) before it starts anything.
// Ending the subscriptions comes second and the stop last. A per-session gate (retireGates)
// orders the whole of it against the starts that check the mark and then launch.
//
// The mark means "retired": from the moment it is written no automatic start or input reaches the
// session. A person's start (POST /start) clears it, and it is an ordinary session from then on.
// The intent (retireIntent) is on disk, so a process that died half-way finishes the tree at
// its next start (ReconcileRetired).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
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

// clearRetired takes the mark off a session a person started. Which retire's mark it was stays on
// the meta (RetireClearedID), in the same update, and the update is a checked temp-and-rename
// write: a failure is returned, and the start that asked reports it instead of claiming success.
func clearRetired(name string) error {
	_, _, err := updateSessionMetaChecked(name, func(m *session.Meta) bool {
		if m.RetiredAt == "" {
			return false
		}
		m.RetiredAt, m.RetireClearedID, m.RetireID = "", m.RetireID, ""
		return true
	})
	return err
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
	in, err := beginRetire(name)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "retire_intent_unwritable",
			"the retire could not be recorded, so nothing was ended: "+err.Error())
		return
	}
	reports := retireTree(r.Context(), name, in)
	finishRetire(in, reports)
	httpx.WriteJSON(w, http.StatusOK, retireResp{Sessions: reports})
}

// retireTree retires root and everything it started. The walk repeats until it finds nobody new,
// waiting for creates that already reserved a child slot to publish, so a child that was still
// being launched is found by the next round; a retired parent reserves no new slot.
func retireTree(ctx context.Context, root string, in *retireIntent) []retireReport {
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
			// A session this retire had already marked and a person has resumed since is theirs
			// again: a recovery leaves it alone.
			retireOne(ctx, rep, in, false)
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

// retireOne retires one session under its gate: mark, end its subscriptions, stop, end once more
// for what a still-running session made in between, and cancel its booked resume. Everything runs
// under the gate, so a person's start cannot land between the steps, and the mark is the one
// guard: the automatic booking path refuses a session that carries it, so whatever the episode
// holds now was booked before it. Every step is safe to repeat.
//
// A retire acts on a member whose meta says a person cleared this same retire's mark (RetireClearedID)
// not at all, and on every other session of its tree, reached before or not. force is the recovery
// of a mark with no intent: it acts only while the mark is still there.
func retireOne(ctx context.Context, rep *retireReport, in *retireIntent, force bool) {
	g := retireGate(rep.Name)
	g.Lock()
	defer g.Unlock()
	cur, ok := session.ReadMeta(rep.Name)
	if ok && !isRetired(cur) && (force || (in != nil && cur.RetireClearedID == in.ID)) {
		return // a person started it since
	}
	markRetired(rep, in.id())
	endSubscriptions(ctx, rep)
	stopRetired(rep)
	endSubscriptions(ctx, rep)
	cancelResume(rep)
}

// retireIntent is the durable record of a retire in progress: its root and its id. It lets the
// start-up recovery walk the tree again, and the id on each member's meta says whether the
// retire's mark was ever written there or a person cleared it.
type retireIntent struct {
	Root string `json:"root"`
	ID   string `json:"id"`
}

func (in *retireIntent) id() string {
	if in == nil {
		return ""
	}
	return in.ID
}

var retireIntents = fstore.JSON[retireIntent](paths.AgentStateDir, "session-retire-intent", ".json")
var retireIntentMu sync.Mutex

// beginRetire records a new retire before anything is marked. Without a durable intent nothing
// may be marked: the write is read back, because a directory that accepted it and keeps nothing
// would leave a crash with no tree to recover.
func beginRetire(root string) (*retireIntent, error) {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	in := retireIntent{Root: root, ID: hex.EncodeToString(b)}
	retireIntentMu.Lock()
	defer retireIntentMu.Unlock()
	if err := retireIntents.Write(root, in); err != nil {
		return nil, err
	}
	if got, ok := retireIntents.Read(root); !ok || got != in {
		return nil, errors.New("the intent could not be read back")
	}
	return &in, nil
}

// finishRetire drops the intent once nothing is left unended; otherwise it stays for the next
// start's recovery to try again.
func finishRetire(in *retireIntent, reports []retireReport) {
	for _, r := range reports {
		if len(r.Unended) > 0 {
			return
		}
	}
	retireIntentMu.Lock()
	defer retireIntentMu.Unlock()
	retireIntents.Remove(in.Root)
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

// ReconcileRetired finishes the retires the previous Agent process did not: each recorded intent
// is walked again from its root (so children never reached are retired), and a marked session
// with no intent (a mark whose intent could not be written) is finished on its own. A session
// whose mark a person cleared since is left alone. What still cannot be ended keeps its intent,
// to be tried at the next start. It returns the number of retires that are now complete.
func ReconcileRetired(ctx context.Context) int {
	done := 0
	covered := map[string]bool{}
	for _, root := range retireIntentRoots() {
		in, ok := retireIntents.Read(root)
		if !ok {
			continue
		}
		m, exists := session.ReadMeta(root)
		if !exists || (!isRetired(m) && m.RetireClearedID == in.ID) {
			retireIntentMu.Lock()
			retireIntents.Remove(root) // gone, or a person resumed the root: nothing to finish
			retireIntentMu.Unlock()
			continue
		}
		reps := retireTree(ctx, root, &in)
		for _, r := range reps {
			covered[r.Name] = true
		}
		finishRetire(&in, reps)
		if _, still := retireIntents.Read(root); !still {
			done++
		} else {
			log.Printf("retire: %s is not finished, it is tried again at the next start", root)
		}
	}
	for _, m := range session.ListMetas() {
		if m.Archived || !isRetired(m) || covered[m.Name] {
			continue
		}
		rep := &retireReport{Name: m.Name, Unended: []string{}}
		retireOne(ctx, rep, nil, true)
		if len(rep.Unended) == 0 {
			done++
		}
	}
	return done
}

func retireIntentRoots() []string {
	entries, err := os.ReadDir(retireIntents.Dir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	return out
}

// markRetired puts the mark and the retire's id on in one update, keeping the first date when it is
// already there. The write is checked: a mark that did not reach the disk protects nothing, and
// is reported as not ended.
func markRetired(rep *retireReport, id string) {
	_, _, err := updateSessionMetaChecked(rep.Name, func(m *session.Meta) bool {
		// The first date stays; the id is this retire's, so a person's start clears the mark
		// of the retire whose intent is on disk.
		if m.RetiredAt != "" && m.RetireID == id {
			return false
		}
		if m.RetiredAt == "" {
			m.RetiredAt = time.Now().UTC().Format(time.RFC3339)
		}
		m.RetireID = id
		return true
	})
	if err != nil {
		rep.Unended = appendNew(rep.Unended, "the retired mark could not be written: "+err.Error())
	}
	if _, exists := session.ReadMeta(rep.Name); !exists {
		rep.Unended = appendNew(rep.Unended, "the session is gone")
	}
}

// endSubscriptions ends the subscriptions of one session.
func endSubscriptions(ctx context.Context, rep *retireReport) {
	ended, unended, err := watchEngine().EndOwner(ctx, rep.Name)
	rep.Ended.Watches += ended.Watches
	rep.Ended.Timers += ended.Timers
	rep.Ended.Builtins += ended.Builtins
	rep.Unended = appendNew(rep.Unended, unended...)
	if err != nil {
		rep.Unended = appendNew(rep.Unended, "subscriptions: "+err.Error())
	}
}

// cancelResume cancels the booked usage-limit resume.
func cancelResume(rep *retireReport) {
	cancelled, left := cancelBookedResume(rep.Name)
	rep.ResumeCancelled = rep.ResumeCancelled || cancelled
	rep.Unended = appendNew(rep.Unended, left...)
}

// cancelBookedResume cancels the usage-limit bookings of the session at the Control Plane and
// closes the episode that held them, so the row reports no rateLimitResumeAt and the sweep books
// nothing for a stopped session. Called under the retire gate with the mark set, and under the
// episode lock the booking path also takes (it refuses a marked session there): whatever the
// episode holds was booked before the mark, and a sweep that was still booking has finished.
// An id whose delete the Control Plane has not confirmed stays recorded for the sweep to retry
// (rate_limit_cleanup.go), and a fire of it that comes first is refused
// (refuseCancelledResume); it is reported as not ended.
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

func appendNew(list []string, add ...string) []string {
	for _, a := range add {
		if !slices.Contains(list, a) {
			list = append(list, a)
		}
	}
	return list
}
