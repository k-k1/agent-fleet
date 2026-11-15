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
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// readMetaStable reads a session's meta under the meta lock. A plain read can land in the middle
// of a writer that truncates and rewrites the file and then comes back empty or cut short; the
// retire must not take that for "no act and no release" and go on to mark and stop the session.
func readMetaStable(name string) (session.Meta, bool) {
	sessionLockMu.Lock()
	defer sessionLockMu.Unlock()
	return session.ReadMeta(name)
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
	// Released: a person started this session (or one above it) after the retire began, so the
	// retire left it, and everything under it, alone. Running is its real state then.
	Released bool `json:"released"`
	Running  bool `json:"running"`
	// Unended is what could not be ended, each with the reason. The Control Plane keeps a
	// delivery's slot while this is not empty.
	Unended []string `json:"unended"`
}

// retireResp is POST /sessions/{name}/retire's answer. Sessions lists the session and all it
// started, the named one first. A repeated call lists the same sessions and ends nothing more.
type retireResp struct {
	Sessions []retireReport `json:"sessions"`
	// Complete is true when every session was retired: none was released, none has anything
	// unended. A retire that met a released session is not complete.
	Complete bool `json:"complete"`
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
	m, ok := readMetaStable(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	// Retire is for roots only: the tree of a root is fixed (a session's parent never changes), so
	// the trees of two roots are disjoint and an intent never meets a mark or a release written by
	// a retire of another root. Ask for the root of a session that another session started.
	if session.OriginOf(m) == session.OriginSession && m.OriginSession != "" && m.OriginSession != name {
		httpx.WriteErr(w, http.StatusConflict, "retire_not_root",
			name+" was started by session "+retireRootOf(m)+"; retire that session instead")
		return
	}
	// The body is optional: none, or 0, is the unconditional retire.
	var cond retireReq
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&cond); err != nil && !errors.Is(err, io.EOF) {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "the body, when there is one, is {\"ifNoActSince\": n}")
			return
		}
	}
	// One retire of a root at a time, from the intent to the end, recovery included: a second
	// request waits for the first, so an older run never goes on with a tree a newer one has
	// already been through and a person has resumed since.
	unlock, err := lockRetireRoot(r.Context(), name)
	if err != nil {
		httpx.WriteErr(w, http.StatusServiceUnavailable, "retire_wait_cancelled",
			"another retire of this session was still running and this request's deadline passed; nothing was done")
		return
	}
	defer unlock()
	if r.Context().Err() != nil { // the lock came free as the deadline passed
		httpx.WriteErr(w, http.StatusServiceUnavailable, "retire_wait_cancelled",
			"this request's deadline passed before the retire started; nothing was done")
		return
	}
	in, err := beginRetire(name, cond.IfNoActSince)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "retire_intent_unwritable",
			"the retire could not be recorded, so nothing was ended: "+err.Error())
		return
	}
	reports := retireTree(r.Context(), name, in)
	finishRetire(in, reports)
	httpx.WriteJSON(w, http.StatusOK, retireResp{Sessions: reports, Complete: retireComplete(reports)})
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
		metas := session.ListMetas()
		for _, d := range spendDescendants(root, metas) {
			if !underReleased(d, metas, seen, in.ID) {
				names = append(names, d.Name)
			}
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
			retireOne(ctx, rep, in)
		}
		if !waitSpawns(ctx, order) {
			order[0].Unended = appendNew(order[0].Unended, "a child create was still in flight; retire again once it has finished")
		}
	}
	// What the answer says is the state now: a session a person started after this retire marked it
	// is released and running, not stopped.
	for _, rep := range order {
		if cur, ok := readMetaStable(rep.Name); ok && releasedFrom(cur, in.ID) {
			rep.Released, rep.Running, rep.Stopped = true, SessionAlive(cur), false
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
// A retire never touches a session a person has released from it (the meta carries this retire's
// id as RetireClearedID), nor anything under such a session: the ancestors are read here, under
// the member's own gate, not from the walk's snapshot. Every other session of its tree is retired,
// reached before or not.
//
// This relies on two facts. A session can make children only while it runs, and a marked
// session reserves no child slot (reserveSpawnSlot), so a child made after a person's start of its
// parent was made after that start cleared the mark (/start clears it after the launch, so a spawn
// in between is refused). And /start answers success only after the mark is cleared, with the
// tombstone in the same checked write (HandleStartSession). A released session's new children
// therefore always have a released ancestor when the walk finds them.
func retireOne(ctx context.Context, rep *retireReport, in *retireIntent) {
	g := retireGate(rep.Name)
	g.Lock()
	defer g.Unlock()
	cur, ok := readMetaStable(rep.Name)
	if !ok {
		// A meta that cannot be read, or is gone, is in doubt: nothing here may stand for "no act" and
		// "not released". The member is neither marked nor stopped in this pass, and the answer says
		// so (not complete), so the Control Plane repeats.
		if metaFileExists(rep.Name) {
			rep.Unended = appendNew(rep.Unended, "the session's meta could not be read, so it was neither marked nor stopped; retire again")
		} else {
			rep.Unended = appendNew(rep.Unended, "the session is gone")
		}
		return
	}
	above, doubt := ancestorState(cur, in.ID)
	if doubt != "" {
		rep.Unended = appendNew(rep.Unended, "the meta of "+doubt+" (above this session) could not be read, so this session was neither marked nor stopped; retire again")
		return
	}
	if above || releasedFrom(cur, in.ID) {
		// A person started it, or a session above it, since this retire began: it and everything
		// below it are theirs. Nothing is marked, stopped or ended, and the answer says so.
		rep.Released, rep.Running = true, SessionAlive(cur)
		return
	}
	switch state, final := markOrRelease(rep, in); state {
	case markReleased:
		rep.Released, rep.Running = true, SessionAlive(final)
		return
	case markDoubt:
		return // nothing is ended or stopped: the reason is in unended and the Control Plane repeats
	}
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
	// IfNoActSince is the condition the request carried: a member whose PersonActMark is at or
	// above it is released from this retire. 0 is none. It lives with the intent so that the
	// recovery after a restart applies the same one.
	IfNoActSince int64 `json:"ifNoActSince,omitempty"`
}

// retireReq is the optional body of POST /sessions/{name}/retire.
type retireReq struct {
	// IfNoActSince is the actMark of the session list the retire was decided on. A session a
	// person started or typed into after that list (PersonActMark >= it) is not retired.
	IfNoActSince int64 `json:"ifNoActSince"`
}

var retireIntents = fstore.JSON[retireIntent](paths.AgentStateDir, "session-retire-intent", ".json")
var retireIntentMu sync.Mutex

// beginRetire records a new retire before anything is marked. Without a durable intent nothing
// may be marked: the write is read back, because a directory that accepted it and keeps nothing
// would leave a crash with no tree to recover.
func beginRetire(root string, ifNoActSince int64) (*retireIntent, error) {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	in := retireIntent{Root: root, ID: hex.EncodeToString(b), IfNoActSince: max(ifNoActSince, 0)}
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
// is walked again from its root, under the root's lock (so children never reached are retired).
// A marked session with no intent is a retire that finished and is left alone. A session whose
// mark a person cleared since is left alone. What still cannot be ended keeps its intent, to be
// tried at the next start. It returns the number of retires that are now complete.
func ReconcileRetired(ctx context.Context) int {
	done := 0
	for _, root := range retireIntentRoots() {
		unlock, err := lockRetireRoot(ctx, root)
		if err != nil {
			continue
		}
		// Everything below is read under the root's lock: a request that held it may have
		// replaced or finished the intent, or a person may have resumed the root.
		in, ok := retireIntents.Read(root)
		if !ok {
			unlock()
			continue
		}
		m, exists := readMetaStable(root)
		if !exists && metaFileExists(root) {
			unlock() // unreadable, not gone: in doubt, so the intent stays for the next start
			continue
		}
		if !exists || (!isRetired(m) && m.RetireClearedID == in.ID) {
			retireIntentMu.Lock()
			retireIntents.Remove(root) // gone, or a person resumed the root: nothing to finish
			retireIntentMu.Unlock()
			unlock()
			continue
		}
		reps := retireTree(ctx, root, &in)
		finishRetire(&in, reps)
		unlock()
		if _, still := retireIntents.Read(root); !still {
			done++
		} else {
			log.Printf("retire: %s is not finished, it is tried again at the next start", root)
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

// markState is what markOrRelease came to. Only markMarked lets the retire end and stop.
type markState int

const (
	// markDoubt: the update failed, or the meta could not be read inside it. Nothing was decided.
	markDoubt markState = iota
	// markMarked: this retire's mark is on the meta, written durably (or was already there).
	markMarked
	// markReleased: a person has the session, by a start after the retire began or by an act.
	markReleased
)

// markOrRelease decides and writes in one checked meta update, on the meta that update reads under
// the meta lock: a member a person started (this retire's tombstone) is left alone; one with
// personActMark >= ifNoActSince is released with the same record a person's start leaves; every
// other member gets the mark and the retire's id, keeping the first date. Any error, and a meta
// that cannot be read inside the update, is markDoubt: a mark or a release that did not reach the
// disk protects nothing, and the caller must not end or stop on it.
//
// A number above this Agent's own counter was not handed out by it (a restored older state): nothing
// here proves "no act since", so it reads as an act. A member this retire or an earlier one has
// marked is not looked at for acts: an act that reached it after the mark found a retired session.
func markOrRelease(rep *retireReport, in *retireIntent) (markState, session.Meta) {
	id := in.ID
	state := markDoubt
	final, _, err := updateSessionMetaChecked(rep.Name, func(m *session.Meta) bool {
		if releasedFrom(*m, id) {
			state = markReleased
			return false
		}
		if in.IfNoActSince > 0 && !isRetired(*m) &&
			(m.PersonActMark >= in.IfNoActSince || in.IfNoActSince > actMarks.now()) {
			m.RetiredAt, m.RetireID, m.RetireClearedID = "", "", id
			state = markReleased
			return true
		}
		state = markMarked
		// The first date stays; the id is this retire's, so a person's start clears the mark
		// of the retire whose intent is on disk.
		if m.RetiredAt != "" && (m.RetireID == id || id == "") {
			return false // an id-less caller (no intent) never overwrites a retire's id
		}
		if m.RetiredAt == "" {
			m.RetiredAt = time.Now().UTC().Format(time.RFC3339)
		}
		m.RetireID = id
		return true
	})
	switch {
	case err != nil:
		rep.Unended = appendNew(rep.Unended, "the retired mark (or the release of a session a person acted on) could not be written, so nothing was ended or stopped: "+err.Error())
		return markDoubt, final
	case state == markDoubt:
		// The callback never ran (the checked updater does not call it for a meta it cannot decode,
		// whatever name that decode left behind): decided from the state, never from the name.
		rep.Unended = appendNew(rep.Unended, "the session's meta could not be read, so it was neither marked nor stopped; retire again")
		return markDoubt, final
	}
	return state, final
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
	m, ok := readMetaStable(rep.Name)
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

// retireRootOf follows the origin=session chain up from m to the session no other session started
// (or the last one that still exists, when an ancestor is gone), without looping.
func retireRootOf(m session.Meta) string {
	seen := map[string]bool{m.Name: true}
	for session.OriginOf(m) == session.OriginSession && m.OriginSession != "" && !seen[m.OriginSession] {
		parent, ok := readMetaStable(m.OriginSession)
		if !ok {
			return m.OriginSession
		}
		seen[parent.Name] = true
		m = parent
	}
	return m.Name
}

var retireRoots sync.Map // root name -> chan struct{} (capacity 1)

// lockRetireRoot serialises the whole of a retire of one root, from its intent to its end. Lock
// order: this, then the per-session gates. The wait ends with ctx: a request whose deadline passed
// while it waited does not run later.
func lockRetireRoot(ctx context.Context, root string) (unlock func(), err error) {
	v, _ := retireRoots.LoadOrStore(root, make(chan struct{}, 1))
	ch := v.(chan struct{})
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// releasedFrom reports whether a person's start released m from the retire id.
func releasedFrom(m session.Meta, id string) bool {
	return !isRetired(m) && m.RetireClearedID == id
}

// releasedAncestor reports whether any session above m (origin=session chain, up to the root) was
// released from the retire id. Loop-safe; a missing ancestor ends the climb.
func releasedAncestor(m session.Meta, id string) bool {
	released, _ := ancestorState(m, id)
	return released
}

// ancestorState is releasedAncestor that also says when an ancestor's meta exists but cannot be
// read (doubt names it): the climb cannot tell whether that one was released, so a destructive step
// must not go on.
func ancestorState(m session.Meta, id string) (released bool, doubt string) {
	seen := map[string]bool{m.Name: true}
	for session.OriginOf(m) == session.OriginSession && m.OriginSession != "" && !seen[m.OriginSession] {
		parent, ok := readMetaStable(m.OriginSession)
		if !ok {
			if metaFileExists(m.OriginSession) {
				return false, m.OriginSession
			}
			return false, ""
		}
		if releasedFrom(parent, id) {
			return true, ""
		}
		seen[parent.Name] = true
		m = parent
	}
	return false, ""
}

// metaFileExists says the session's meta file is there, readable or not.
func metaFileExists(name string) bool {
	_, err := os.Stat(session.MetaPath(name))
	return err == nil
}

// underReleased reports whether a session of the walk sits below one this retire found released.
func underReleased(d session.Meta, metas []session.Meta, seen map[string]*retireReport, id string) bool {
	byName := map[string]session.Meta{}
	for _, m := range metas {
		byName[m.Name] = m
	}
	visited := map[string]bool{d.Name: true}
	for cur := d; session.OriginOf(cur) == session.OriginSession && cur.OriginSession != "" && !visited[cur.OriginSession]; {
		if rep := seen[cur.OriginSession]; rep != nil && rep.Released {
			return true
		}
		if a, ok := byName[cur.OriginSession]; ok && releasedFrom(a, id) {
			return true // released already when the walk read the metas; the member's own gate rechecks
		}
		visited[cur.OriginSession] = true
		p, ok := byName[cur.OriginSession]
		if !ok {
			return false
		}
		cur = p
	}
	return false
}

func retireComplete(reports []retireReport) bool {
	for _, r := range reports {
		if r.Released || len(r.Unended) > 0 {
			return false
		}
	}
	return len(reports) > 0
}
