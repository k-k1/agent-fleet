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

// clearRetired takes the mark off a session a person started, and leaves a durable note of when,
// so a recovery can tell this from a mark that was never written.
func clearRetired(name string) {
	if _, cleared := UpdateSessionMeta(name, func(m *session.Meta) bool {
		if m.RetiredAt == "" {
			return false
		}
		m.RetiredAt = ""
		return true
	}); cleared {
		_ = retireCleared.Write(name, time.Now().Format(time.RFC3339Nano))
	}
}

// retireCleared holds, per session, when a person last cleared its retired mark.
var retireCleared = fstore.Strings(paths.AgentStateDir, "session-retire-cleared", ".txt")

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
	// resumeIDs are the booked resumes found under the gate; only those are cancelled.
	resumeIDs []string
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
			retireOne(ctx, rep, in, in.wasMarked(rep.Name) || in.resumedSince(rep.Name))
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

// retireOne retires one session: under its gate mark, end its subscriptions, stop, and end once
// more for what a still-running session made in between; then cancel its booked resume (outside
// the gate, which only has to order the launches). Every step is safe to repeat. onlyIfMarked is
// the recovery's rule: the mark is checked again under the gate, and a session whose mark is gone
// (a person started it) is left alone.
func retireOne(ctx context.Context, rep *retireReport, in *retireIntent, onlyIfMarked bool) {
	g := retireGate(rep.Name)
	g.Lock()
	if onlyIfMarked {
		if cur, ok := session.ReadMeta(rep.Name); !ok || !isRetired(cur) {
			g.Unlock()
			return
		}
	}
	// The intent names the session before the mark is written and confirms it after, so a crash
	// in between is told from a person's resume: a pending member without a mark was never marked.
	if err := in.pending(rep.Name); err != nil {
		rep.Unended = appendNew(rep.Unended, "the retire intent could not be written, nothing was marked: "+err.Error())
		g.Unlock()
		return
	}
	markRetired(rep)
	if cur, ok := session.ReadMeta(rep.Name); ok && isRetired(cur) {
		if err := in.confirm(rep.Name); err != nil {
			rep.Unended = appendNew(rep.Unended, "the retire intent could not be updated: "+err.Error())
		}
	}
	// The resume's ids are fixed here, under the gate: a person's start after this point may book
	// a resume of its own, which this retire must not cancel.
	rep.resumeIDs = bookedResumeIDs(rep.Name)
	endSubscriptions(ctx, rep)
	stopRetired(rep)
	endSubscriptions(ctx, rep)
	g.Unlock()
	cancelResume(rep)
}

// retireIntent is the durable record of a retire in progress: its root and the sessions it has
// marked (or is about to). It lets the start-up recovery finish a tree whose root alone was
// marked when the process died, and tell a session a person has resumed since from one never
// reached.
type retireIntent struct {
	Root    string         `json:"root"`
	Members []retireMember `json:"members,omitempty"`
}

// retireMember is a session of the tree. Marked is set once its mark is on disk; before that it
// only says the retire was about to mark it.
type retireMember struct {
	Name   string `json:"name"`
	Marked bool   `json:"marked,omitempty"`
	// At is when the member was recorded as pending.
	At string `json:"at,omitempty"`
}

var retireIntents = fstore.JSON[retireIntent](paths.AgentStateDir, "session-retire-intent", ".json")
var retireIntentMu sync.Mutex

// resumedSince reports whether name was recorded as pending and a person cleared its mark after
// that: the crash came between the mark and its confirmation, and the session is theirs now.
func (in *retireIntent) resumedSince(name string) bool {
	if in == nil {
		return false
	}
	i := slices.IndexFunc(in.Members, func(m retireMember) bool { return m.Name == name && !m.Marked })
	if i < 0 {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, in.Members[i].At)
	cleared, ok := retireCleared.Read(name)
	if !ok || err != nil {
		return false
	}
	ct, err := time.Parse(time.RFC3339Nano, cleared)
	return err == nil && ct.After(at)
}

// wasMarked reports whether the intent saw name's mark on disk: a mark found missing now is a
// person's start.
func (in *retireIntent) wasMarked(name string) bool {
	if in == nil {
		return false
	}
	return slices.ContainsFunc(in.Members, func(m retireMember) bool { return m.Name == name && m.Marked })
}

func (in *retireIntent) set(name string, marked bool) error {
	if in == nil {
		return nil
	}
	retireIntentMu.Lock()
	defer retireIntentMu.Unlock()
	next := *in
	next.Members = slices.Clone(in.Members)
	if i := slices.IndexFunc(next.Members, func(m retireMember) bool { return m.Name == name }); i >= 0 {
		if next.Members[i].Marked || !marked {
			return nil
		}
		next.Members[i].Marked = true
	} else {
		next.Members = append(next.Members, retireMember{Name: name, Marked: marked, At: time.Now().Format(time.RFC3339Nano)})
	}
	if err := writeRetireIntent(next); err != nil {
		return err
	}
	*in = next
	return nil
}

func (in *retireIntent) pending(name string) error { return in.set(name, false) }
func (in *retireIntent) confirm(name string) error { return in.set(name, true) }

// writeRetireIntent writes through a read-back: fstore's Write reports the error of the write
// itself, which is what is needed here, and the read-back catches a directory that accepted it
// and keeps nothing.
func writeRetireIntent(in retireIntent) error {
	if err := retireIntents.Write(in.Root, in); err != nil {
		return err
	}
	if got, ok := retireIntents.Read(in.Root); !ok || got.Root != in.Root {
		return errors.New("the intent could not be read back")
	}
	return nil
}

// beginRetire records the intent before anything is marked; an existing one for the same root is
// continued, so its members stay known. Without a durable intent nothing may be marked.
func beginRetire(root string) (*retireIntent, error) {
	retireIntentMu.Lock()
	defer retireIntentMu.Unlock()
	in, ok := retireIntents.Read(root)
	if !ok {
		in = retireIntent{Root: root}
		if err := writeRetireIntent(in); err != nil {
			return nil, err
		}
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
		in, err := beginRetire(root)
		if err != nil {
			log.Printf("retire: the intent of %s cannot be read or written: %v", root, err)
			continue
		}
		if m, ok := session.ReadMeta(root); !ok || !isRetired(m) {
			retireIntentMu.Lock()
			retireIntents.Remove(root)
			retireIntentMu.Unlock()
			continue
		}
		reps := retireTree(ctx, root, in)
		for _, r := range reps {
			covered[r.Name] = true
		}
		finishRetire(in, reps)
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

// beforeResumeCancel is a test seam: it runs after the retire left the gate and before the resume
// cancel, where a person's start and a new booking can land. Nil in production.
var beforeResumeCancel func(name string)

// cancelResume cancels the booked usage-limit resume.
func cancelResume(rep *retireReport) {
	if beforeResumeCancel != nil {
		beforeResumeCancel(rep.Name)
	}
	cancelled, left := cancelBookedResume(rep.Name, rep.resumeIDs)
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

// bookedResumeIDs reads the resume bookings the session's episode holds now.
func bookedResumeIDs(name string) []string {
	// Under the episode lock: a sweep that began before the retire and is still booking holds it,
	// so the snapshot waits for the id that sweep is about to save. Lock order: retire gate, then
	// episode lock.
	mu := rateLimitEpisodeLock(name)
	mu.Lock()
	defer mu.Unlock()
	st, has := RateLimitStates.Read(name)
	if !has {
		return nil
	}
	var ids []string
	for _, id := range []string{st.ScheduleID, st.MovedFrom} {
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// cancelBookedResume cancels the given usage-limit bookings of the session at the Control Plane
// and closes the episode that held them, so the row reports no rateLimitResumeAt and the sweep
// books nothing for a stopped session. An id whose delete the Control Plane has not confirmed
// stays recorded for the sweep to retry (rate_limit_cleanup.go), and a fire of it that comes
// first is refused (refuseCancelledResume); it is reported here as not ended. Only the ids the
// retire found are cancelled, and the episode is removed only while it still names one of them:
// a person's start after the retire may have booked a resume of its own.
func cancelBookedResume(name string, ids []string) (cancelled bool, unended []string) {
	mu := rateLimitEpisodeLock(name)
	mu.Lock()
	defer mu.Unlock()
	for _, id := range ids {
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
	if st, has := RateLimitStates.Read(name); has && len(unended) == 0 &&
		(slices.Contains(ids, st.ScheduleID) || (st.ScheduleID == "" && st.MovedFrom != "" && slices.Contains(ids, st.MovedFrom))) {
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
