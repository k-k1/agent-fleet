package sessionx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/lifegate"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

// retireWaker is the Control Plane's wake booking target.
type retireWaker struct {
	mu      sync.Mutex
	n       int
	booked  map[string]bool
	cancel  error
	started int
}

func (w *retireWaker) Book(context.Context, time.Time) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.n++
	id := "wake" + string(rune('0'+w.n))
	if w.booked == nil {
		w.booked = map[string]bool{}
	}
	w.booked[id] = true
	return id, nil
}

func (w *retireWaker) Cancel(_ context.Context, id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return w.cancel
	}
	delete(w.booked, id)
	return nil
}

func (w *retireWaker) live() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.booked)
}

// onCancel runs inside the first Cancel, after the engine took the owner's subscriptions out.
type cancelHook struct {
	*retireWaker
	once sync.Once
	fn   func()
}

func (h *cancelHook) Cancel(ctx context.Context, id string) error {
	h.once.Do(h.fn)
	return h.retireWaker.Cancel(ctx, id)
}

type retireEnv struct {
	*watchFixture
	e *watch.Engine
	w *retireWaker
}

func newRetireEnv(t *testing.T) *retireEnv {
	t.Helper()
	isolateAgentState(t)
	f := newWatchFixture(t)
	e := watch.New(watchDiskStore{}, newWatchSink(), stubSource{}, &watch.Time{})
	e.Barred = watchBarred
	w := &retireWaker{}
	e.SetWaker(w)
	useWatchEngine(t, e)
	return &retireEnv{f, e, w}
}

func writeMeta(t *testing.T, m session.Meta) {
	t.Helper()
	if m.Kind == "" {
		m.Kind = session.KindClaude
	}
	if m.Dir == "" {
		m.Dir = t.TempDir()
	}
	session.WriteMeta(m)
}

func (r *retireEnv) hold(t *testing.T, owner string) {
	t.Helper()
	ctx := context.Background()
	if _, err := r.e.Subscribe(ctx, watch.Request{Owner: owner, Source: "github", Ref: "o/r#1", Types: []string{"pr.merged"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.Subscribe(ctx, watch.Request{Owner: owner, Source: "time", Ref: "in 1h", Wake: true}); err != nil {
		t.Fatal(err)
	}
}

func postRetire(t *testing.T, name string) (int, retireResp) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/retire", nil)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleSessionRetire(rec, req)
	var out retireResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func retiredNames(rs []retireReport) string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Name)
	}
	return strings.Join(out, ",")
}

// The walk follows origin=session and the originSession chain at any depth, once each even when
// the chain loops, and takes nobody else: not a handoff launch (which also carries
// originSession), and not a session whose name merely starts with the same letters.
func TestRetireWalksTheTreeAtAnyDepthAndOnlyTheTree(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	writeMeta(t, session.Meta{Name: "c1", Origin: session.OriginSession, OriginSession: "p"})
	writeMeta(t, session.Meta{Name: "g1", Origin: session.OriginSession, OriginSession: "c1"})
	writeMeta(t, session.Meta{Name: "gg", Origin: session.OriginSession, OriginSession: "g1"})
	writeMeta(t, session.Meta{Name: "h1", Origin: session.OriginHandoff, OriginSession: "p"})
	writeMeta(t, session.Meta{Name: "p2", Origin: session.OriginSession, OriginSession: "px"})
	writeMeta(t, session.Meta{Name: "px"})
	writeMeta(t, session.Meta{Name: "old", Origin: session.OriginSession, OriginSession: "p", Archived: true})
	for _, n := range []string{"p", "c1", "g1", "gg", "h1", "px", "p2"} {
		r.hold(t, n)
	}
	code, out := postRetire(t, "p")
	if code != 200 || retiredNames(out.Sessions) != "p,c1,g1,gg" {
		t.Fatalf("retire = %d %s", code, retiredNames(out.Sessions))
	}
	for _, n := range []string{"p", "c1", "g1", "gg"} {
		m, _ := session.ReadMeta(n)
		if m.RetiredAt == "" {
			t.Errorf("%s is not marked retired", n)
		}
		if w, tm, _ := r.e.Wakers(n); w+tm != 0 {
			t.Errorf("%s still holds %d watches %d timers", n, w, tm)
		}
	}
	for _, rep := range out.Sessions {
		if rep.Ended.Watches != 1 || rep.Ended.Timers != 1 || !rep.Stopped || len(rep.Unended) != 0 {
			t.Errorf("%s report = %+v", rep.Name, rep)
		}
	}
	// Left alone: a handoff launch, the prefix look-alikes, an archived child.
	for _, n := range []string{"h1", "px", "p2"} {
		m, _ := session.ReadMeta(n)
		if w, tm, _ := r.e.Wakers(n); m.RetiredAt != "" || w != 1 || tm != 1 {
			t.Errorf("%s was retired or lost its subscriptions: retiredAt=%q %d %d", n, m.RetiredAt, w, tm)
		}
	}
	if m, _ := session.ReadMeta("old"); m.RetiredAt != "" {
		t.Error("an archived child was retired")
	}
	if r.w.live() != 3 { // h1, px, p2 keep their wake bookings; the tree's four are cancelled
		t.Fatalf("wake bookings left = %d, want 3", r.w.live())
	}
}

func TestRetireTwiceAnswersTheSameSessionsWithoutError(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	writeMeta(t, session.Meta{Name: "c1", Origin: session.OriginSession, OriginSession: "p"})
	r.hold(t, "p")
	_, first := postRetire(t, "p")
	m1, _ := session.ReadMeta("p")
	code, second := postRetire(t, "p")
	if code != 200 || retiredNames(second.Sessions) != retiredNames(first.Sessions) {
		t.Fatalf("second retire = %d %s, first %s", code, retiredNames(second.Sessions), retiredNames(first.Sessions))
	}
	if second.Sessions[0].Ended != (watch.Ended{}) || len(second.Sessions[0].Unended) != 0 {
		t.Fatalf("second retire ended more: %+v", second.Sessions[0])
	}
	if m2, _ := session.ReadMeta("p"); m2.RetiredAt != m1.RetiredAt {
		t.Fatalf("the mark's date moved: %q -> %q", m1.RetiredAt, m2.RetiredAt)
	}
}

func TestRetireRefusesWhatIsNotASession(t *testing.T) {
	newRetireEnv(t)
	if code, _ := postRetire(t, "ghost"); code != http.StatusNotFound {
		t.Fatalf("unknown session = %d", code)
	}
	if code, _ := postRetire(t, "../x"); code != http.StatusBadRequest {
		t.Fatalf("bad name = %d", code)
	}
}

// The things that would undo a retire. Each is tried after the retire has run.
func TestRetiredSessionIsNotStartedByAWatchNotice(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	s := newWatchSink()
	postRetire(t, "a")
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	// A notice that was already on its way: the engine asked Ready before the retire, claimed
	// the event, and Inject runs after. Injecting into a stopped session resumes a Managed one.
	r.alive["a"] = false
	sub := watch.Subscription{ID: "w1", Owner: "a", Until: watch.UntilFirst}
	ev := watch.Event{Source: "github", Subject: "github:o/r#1", Type: "pr.merged", Summary: "merged"}
	if err := s.Inject(sub, []watch.Event{ev}); err == nil || len(r.sent) != 0 {
		t.Fatalf("a claimed notice reached a retired session: err=%v sent=%v", err, r.sent)
	}
	// And the readiness question never starts it.
	for i := 0; i < 3; i++ {
		if got := s.Ready("a", now.Add(time.Duration(i)*time.Minute)); got != watch.Unreachable {
			t.Fatalf("Ready for a retired stopped session = %v", got)
		}
	}
	if len(r.started) != 0 {
		t.Fatalf("the sink started a retired session: %v", r.started)
	}
	if err := startSessionInProcess("a"); err == nil {
		t.Fatal("the in-process start (what a watch wake uses) started a retired session")
	}
}

func TestRetiredSessionGetsNoSubscriptionMadeAfterwards(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	writeMeta(t, session.Meta{Name: "c1", Origin: session.OriginSession, OriginSession: "p"})
	postRetire(t, "p")

	// A built-in the feature makes again on its next sweep, for a stopped session.
	spec := watch.BuiltinSpec{Subject: "github:o/r#1", Owner: "c1", Ref: "o/r#1", Types: []string{"pr.checks.failed"}, Fallback: watch.NotifyMember}
	r.e.SyncBuiltins("ci", []watch.BuiltinSpec{spec})
	if w, _, _ := r.e.Wakers("c1"); w != 0 {
		t.Fatalf("a built-in was made again for a retired session: %d", w)
	}
	// A watch a running child asks for on behalf of its retired parent.
	req := httptest.NewRequest(http.MethodPost, "/sessions/c1/watches", strings.NewReader(`{"ref":"o/r#1","types":["pr.merged"],"notify":"parent"}`))
	req.SetPathValue("name", "c1")
	r.alive["c1"] = true // the child is mid-turn while the retire ran
	rec := httptest.NewRecorder()
	HandleWatchCreate(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "retired") {
		t.Fatalf("a watch for a retired, stopped parent: %d %s", rec.Code, rec.Body)
	}
	if w, tm, _ := r.e.Wakers("p"); w+tm != 0 {
		t.Fatalf("the parent holds %d %d after the refusal", w, tm)
	}
}

func TestRetiredSessionTakesNoBookedResume(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	writeMeta(t, session.Meta{Name: "k", Origin: session.OriginSession, OriginSession: "a"})
	var dropped []string
	var dropErr error
	prev := dropRateLimitSchedule
	dropRateLimitSchedule = func(id string) error {
		if dropErr != nil {
			return dropErr
		}
		dropped = append(dropped, id)
		return nil
	}
	t.Cleanup(func() { dropRateLimitSchedule = prev })
	at := time.Now().Add(time.Hour).Format(time.RFC3339)
	for name, id := range map[string]string{"a": "sch_a", "k": "sch_k"} {
		if err := RateLimitStates.Write(name, rateLimitState{At: time.Now().Format(time.RFC3339), ResumeAt: at, ScheduleID: id}); err != nil {
			t.Fatal(err)
		}
	}
	dropErr = errors.New("control plane unreachable")
	_, out := postRetire(t, "a")
	// The cancel the Control Plane did not confirm is reported, and a fire of it is refused.
	for _, rep := range out.Sessions {
		if rep.ResumeCancelled || len(rep.Unended) != 1 || !strings.Contains(rep.Unended[0], "usage-limit resume") {
			t.Fatalf("%s: %+v, want the unconfirmed cancel reported", rep.Name, rep)
		}
	}
	prompt := rateLimitResumePromptFor("en")
	for name, id := range map[string]string{"a": "sch_a", "k": "sch_k"} {
		if !refuseCancelledResume(name, TurnSourceSchedule, prompt, id) {
			t.Errorf("%s: the time of the booked resume passing started a retired session", name)
		}
	}

	// The Control Plane comes back: a repeated retire settles it.
	dropErr = nil
	_, out = postRetire(t, "a")
	for _, rep := range out.Sessions {
		if len(rep.Unended) != 0 {
			t.Fatalf("%s still unended: %v", rep.Name, rep.Unended)
		}
	}
	retryRateLimitScheduleCleanups()
	if strings.Join(dropped, ",") != "sch_a,sch_k" && strings.Join(dropped, ",") != "sch_k,sch_a" {
		t.Fatalf("cancelled = %v", dropped)
	}
	for _, n := range []string{"a", "k"} {
		m, _ := session.ReadMeta(n)
		if at := rateLimitBookedAt(m, time.Now()); at != "" {
			t.Errorf("%s still reports a booked resume at %s", n, at)
		}
	}
	_ = r
}

// A retire that fails to reach the Control Plane for a time-watch booking says so and keeps no
// subscription behind; the booking is cancelled by the engine's next sweep.
func TestRetireReportsAWakeBookingItCouldNotCancel(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	r.hold(t, "a")
	r.w.cancel = errors.New("control plane unreachable")
	_, out := postRetire(t, "a")
	if len(out.Sessions[0].Unended) != 1 || !strings.Contains(out.Sessions[0].Unended[0], "wake booking") {
		t.Fatalf("report = %+v", out.Sessions[0])
	}
	if w, tm, _ := r.e.Wakers("a"); w+tm != 0 {
		t.Fatalf("subscriptions remain: %d %d", w, tm)
	}
}

// A person starting a retired session from the Console makes it an ordinary session again: its
// watches work, built-ins come back, and a notice starts it.
func TestAPersonsStartMakesARetiredSessionOrdinary(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	postRetire(t, "a")
	if m, _ := session.ReadMeta("a"); m.RetiredAt == "" {
		t.Fatal("not retired")
	}

	// tmux that says the session exists: /start has nothing to launch and succeeds.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	req := httptest.NewRequest(http.MethodPost, "/sessions/a/start", nil)
	req.SetPathValue("name", "a")
	rec := httptest.NewRecorder()
	HandleStartSession(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/start = %d %s", rec.Code, rec.Body)
	}
	if m, _ := session.ReadMeta("a"); m.RetiredAt != "" {
		t.Fatalf("a person's start left the mark: %q", m.RetiredAt)
	}
	r.hold(t, "a")
	if w, tm, _ := r.e.Wakers("a"); w != 1 || tm != 1 {
		t.Fatalf("an ordinary session cannot hold watches again: %d %d", w, tm)
	}
	if got := newWatchSink().Ready("a", time.Now()); got == watch.Unreachable {
		t.Fatal("an ordinary session is still unreachable")
	}
}

// The row: what a session holds, running or stopped, and across an Agent restart; the engine's
// unreadable state is "unknown", never zero.
func TestRowReportsWakersLiveStoppedAndAcrossARestart(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	m, _ := session.ReadMeta("a")
	if s := wireSession(m, false); s.Wakers != nil || s.WakersUnknown {
		t.Fatalf("a session holding nothing reports %+v unknown=%v", s.Wakers, s.WakersUnknown)
	}
	r.hold(t, "a")
	for _, alive := range []bool{true, false} {
		s := wireSession(m, alive)
		if s.Wakers == nil || s.Wakers.Watches != 1 || s.Wakers.Timers != 1 || s.WakersUnknown {
			t.Fatalf("alive=%v wakers=%+v unknown=%v", alive, s.Wakers, s.WakersUnknown)
		}
	}
	b, err := json.Marshal(wireSession(m, false))
	if err != nil || !strings.Contains(string(b), `"wakers":{"watches":1,"timers":1}`) || strings.Contains(string(b), "wakersUnknown") {
		t.Fatalf("wire = %s %v", b, err)
	}

	// An Agent restart: a new engine over the same disk.
	useWatchEngine(t, watch.New(watchDiskStore{}, newWatchSink(), stubSource{}, &watch.Time{}))
	if s := wireSession(m, false); s.Wakers == nil || s.Wakers.Watches != 1 || s.Wakers.Timers != 1 {
		t.Fatalf("after a restart: %+v", s.Wakers)
	}
}

func TestRowSaysUnknownWhenTheWatchStateCannotBeRead(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	r.hold(t, "a")
	if err := os.WriteFile(watchStateStore.Path(watchStateKey), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	useWatchEngine(t, watch.New(watchDiskStore{}, newWatchSink(), stubSource{}, &watch.Time{}))
	m, _ := session.ReadMeta("a")
	s := wireSession(m, false)
	if s.Wakers != nil || !s.WakersUnknown {
		t.Fatalf("an unreadable state gave wakers=%+v unknown=%v", s.Wakers, s.WakersUnknown)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), `"wakers"`) || !strings.Contains(string(b), `"wakersUnknown":true`) {
		t.Fatalf("wire = %s", b)
	}
}

// A state that is merely absent is a session that holds nothing, not an unknown.
func TestRowWithNoWatchStateFileHoldsNothing(t *testing.T) {
	newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	m, _ := session.ReadMeta("a")
	s := wireSession(m, false)
	if s.Wakers != nil || s.WakersUnknown {
		t.Fatalf("wakers=%+v unknown=%v", s.Wakers, s.WakersUnknown)
	}
}

func queueRow(t *testing.T, name, kind string) (session.Meta, string) {
	t.Helper()
	writeMeta(t, session.Meta{Name: name, Kind: kind, QueueDelivery: "d1", InitialPromptState: session.InitialPromptDelivered})
	m, _ := session.ReadMeta(name)
	return m, session.UUID(m.Dir, name)
}

func TestRowQueueTurnHoldsTheFirstTurnAcrossAStopAndALaterTurn(t *testing.T) {
	newRetireEnv(t)
	m, sid := queueRow(t, "q1", session.KindClaude)
	if s := wireSession(m, true); s.QueueTurn != nil {
		t.Fatalf("a turn that has not ended is reported: %+v", s.QueueTurn)
	}
	status.ArmFirstTurn(sid, false)
	status.MarkFirstTurnDelivering(sid, "task")
	status.Persist(sid, "working")
	time.Sleep(10 * time.Millisecond)
	status.PersistTurnEndReason(sid, "idle", status.TurnEndReasonAborted)
	first := wireSession(m, true).QueueTurn
	if first == nil || first.Outcome != "turn-aborted" || first.EndedAt == "" {
		t.Fatalf("first turn = %+v", first)
	}

	// A second turn, a stop that deletes the status record, a third turn.
	status.Persist(sid, "working")
	status.PersistTurnEnd(sid, "idle")
	status.Remove(sid)
	status.Persist(sid, "working")
	status.PersistTurnEndReason(sid, "idle", status.TurnEndReasonFailed)
	for _, alive := range []bool{true, false} {
		if got := wireSession(m, alive).QueueTurn; got == nil || *got != *first {
			t.Fatalf("alive=%v: %+v, want the first turn's %+v", alive, got, first)
		}
	}
	b, _ := json.Marshal(wireSession(m, false))
	if !strings.Contains(string(b), `"queueTurn":{"outcome":"turn-aborted","ms":`) {
		t.Fatalf("wire = %s", b)
	}
}

func TestRowQueueTurnOfAKindWithoutHooksHasNoOutcomeButTimes(t *testing.T) {
	newRetireEnv(t)
	m, sid := queueRow(t, "q2", session.KindClaude)
	status.ArmFirstTurn(sid, true)
	status.MarkFirstTurnDelivering(sid, "task")
	status.Persist(sid, "working")
	status.PersistTurnEnd(sid, "idle")
	got := wireSession(m, false).QueueTurn
	if got == nil || got.Outcome != "" || got.EndedAt == "" {
		t.Fatalf("queueTurn = %+v", got)
	}
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"outcome":""`) {
		t.Fatalf("the empty outcome is not on the wire: %s", b)
	}
}

func TestRowQueueTurnIsAbsentWhenTheEndSeenMayBeALaterTurns(t *testing.T) {
	newRetireEnv(t)
	m, sid := queueRow(t, "q3", session.KindClaude)
	status.ArmFirstTurn(sid, false)
	status.MarkFirstTurnDelivering(sid, "task")
	status.Persist(sid, "working")
	status.Remove(sid) // stopped before the end was seen
	status.Persist(sid, "working")
	status.PersistTurnEnd(sid, "idle")
	if got := wireSession(m, false).QueueTurn; got != nil {
		t.Fatalf("a later turn's end was reported as the first's: %+v", got)
	}
}

func TestRowQueueTurnIsAbsentWhenThePromptWasNeverDelivered(t *testing.T) {
	newRetireEnv(t)
	m, sid := queueRow(t, "q4", session.KindClaude)
	status.ArmFirstTurn(sid, false)
	status.MarkFirstTurnDelivering(sid, "task")
	status.Persist(sid, "working")
	status.PersistTurnEnd(sid, "idle")
	m.InitialPromptState = session.InitialPromptFailed
	if got := wireSession(m, false).QueueTurn; got != nil {
		t.Fatalf("a prompt that never went in has a first turn: %+v", got)
	}
	// And a session that is not a delivery's has none, whatever the status says.
	other, _ := queueRow(t, "q5", session.KindClaude)
	other.QueueDelivery = ""
	if got := wireSession(other, false).QueueTurn; got != nil {
		t.Fatalf("a session without a delivery has a queueTurn: %+v", got)
	}
}

// A session that is still running while its subscriptions are ended can make one more before the
// stop takes it down. The sweep after the stop ends it, so nothing is left counted on a stopped row.
func TestRetireEndsAWatchMadeWhileTheSessionWasStillRunning(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	r.hold(t, "a")
	r.e.SetWaker(&cancelHook{retireWaker: r.w, fn: func() {
		r.e.Barred = nil // a call that passed the check before the mark was written
		_, err := r.e.Subscribe(context.Background(), watch.Request{Owner: "a", Source: "github", Ref: "o/r#2", Types: []string{"pr.merged"}})
		if err != nil {
			t.Errorf("late watch: %v", err)
		}
	}})
	_, out := postRetire(t, "a")
	if w, tm, _ := r.e.Wakers("a"); w+tm != 0 {
		t.Fatalf("a watch made during the retire survives it: %d %d", w, tm)
	}
	if out.Sessions[0].Ended.Watches != 2 {
		t.Fatalf("report = %+v, want both watches ended", out.Sessions[0])
	}
}

// The list envelope names the contract this Agent reports under, so a Control Plane can tell an
// Agent that omits wakers because it holds nothing from one that does not say.
func TestListEnvelopeCarriesQueueReport(t *testing.T) {
	newRetireEnv(t)
	rec := httptest.NewRecorder()
	HandleListSessions(rec, httptest.NewRequest(http.MethodGet, "/sessions", nil))
	var env map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || string(env["queueReport"]) != "1" {
		t.Fatalf("envelope = %s (%v)", rec.Body, err)
	}
}

// A retired session is not started by an input of any kind, and an automatic start is refused;
// only a person's start clears the mark.
func TestRetiredSessionRefusesAutomaticStartsAndInputs(t *testing.T) {
	newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	postRetire(t, "a")
	m, _ := session.ReadMeta("a")
	if _, err := resumeManaged(nil, m); !errors.Is(err, errSessionRetired) {
		t.Fatalf("a managed input started a retired session: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/sessions/a/start", nil)
	req.SetPathValue("name", "a")
	rec := httptest.NewRecorder()
	HandleStartSession(rec, asAutomaticStart(req))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "session_retired") {
		t.Fatalf("automatic start = %d %s", rec.Code, rec.Body)
	}
	if m, _ := session.ReadMeta("a"); m.RetiredAt == "" {
		t.Fatal("an automatic start cleared the mark")
	}
	if err := reserveSpawnSlot("a"); err == nil {
		t.Fatal("a retired session reserved a child slot")
	}
}

// A start that checked the mark holds the gate until its launch is done, so a retire waits for it
// instead of landing in between.
func TestRetireWaitsForAStartInProgress(t *testing.T) {
	newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	g := retireGate("a")
	g.RLock()
	done := make(chan struct{})
	go func() { postRetire(t, "a"); close(done) }()
	select {
	case <-done:
		t.Fatal("retire finished while a start held the gate")
	case <-time.After(200 * time.Millisecond):
	}
	if m, _ := session.ReadMeta("a"); m.RetiredAt != "" {
		t.Fatal("retire marked the session under a start in progress")
	}
	g.RUnlock()
	<-done
	if m, _ := session.ReadMeta("a"); m.RetiredAt == "" {
		t.Fatal("retire did not run after the start finished")
	}
}

// A create that reserved a slot before the retire and publishes after it is found and retired.
func TestRetireFindsAChildPublishedWhileItRuns(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	spawnInflight.mu.Lock()
	spawnInflight.n["p"] = 1
	spawnInflight.mu.Unlock()
	r.hold(t, "p")
	done := make(chan []retireReport, 1)
	go func() { _, out := postRetire(t, "p"); done <- out.Sessions }()
	time.Sleep(400 * time.Millisecond)
	writeMeta(t, session.Meta{Name: "late", Origin: session.OriginSession, OriginSession: "p"})
	if _, err := r.e.Subscribe(context.Background(), watch.Request{Owner: "late", Source: "github", Ref: "o/r#1", Types: []string{"pr.merged"}}); err != nil {
		t.Fatal(err)
	}
	spawnInflight.mu.Lock()
	delete(spawnInflight.n, "p")
	spawnInflight.mu.Unlock()
	got := <-done
	if retiredNames(got) != "p,late" {
		t.Fatalf("retired %s", retiredNames(got))
	}
	if m, _ := session.ReadMeta("late"); m.RetiredAt == "" {
		t.Fatal("the late child is not retired")
	}
	if w, _, _ := r.e.Wakers("late"); w != 0 {
		t.Fatalf("the late child still holds %d", w)
	}
}

// A previous process died after the mark (the session still runs) or after the stop (the
// subscriptions were not swept): the next process finishes it with a fresh engine.
func TestReconcileRetiredFinishesWhatACrashLeft(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		r := newRetireEnv(t)
		writeMeta(t, session.Meta{Name: "a"})
		r.hold(t, "a")
		m := session.Meta{Name: "a", RetiredAt: "2026-11-15T10:00:00Z"}
		if stopped {
			m.StoppedAt = "2026-11-15T10:00:01Z"
		}
		in := mustBeginRetire(t, "a") // the intent is written before anything is marked
		m.RetireID = in.ID
		writeMeta(t, m)
		fresh := watch.New(watchDiskStore{}, newWatchSink(), stubSource{}, &watch.Time{})
		fresh.Barred = watchBarred
		fresh.SetWaker(r.w)
		useWatchEngine(t, fresh)
		if w, tm, _ := fresh.Wakers("a"); w+tm != 2 {
			t.Fatalf("stopped=%v setup: %d %d", stopped, w, tm)
		}
		if n := ReconcileRetired(context.Background()); n != 1 {
			t.Fatalf("stopped=%v: reconciled %d", stopped, n)
		}
		if w, tm, _ := fresh.Wakers("a"); w+tm != 0 {
			t.Fatalf("stopped=%v: still holds %d %d", stopped, w, tm)
		}
		if got, _ := session.ReadMeta("a"); got.RetiredAt == "" {
			t.Fatalf("stopped=%v: the mark was lost", stopped)
		}
	}
}

// An empty or undecodable state file is not "holds nothing".
func TestRowWithAnEmptyOrBrokenWatchStateIsUnknown(t *testing.T) {
	for name, content := range map[string][]byte{"empty": {}, "cut": []byte(`{"subs":[`)} {
		r := newRetireEnv(t)
		writeMeta(t, session.Meta{Name: "a"})
		if err := os.MkdirAll(filepath.Dir(watchStateStore.Path(watchStateKey)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(watchStateStore.Path(watchStateKey), content, 0o600); err != nil {
			t.Fatal(err)
		}
		useWatchEngine(t, watch.New(watchDiskStore{}, newWatchSink(), stubSource{}, &watch.Time{}))
		m, _ := session.ReadMeta("a")
		if s := wireSession(m, false); !s.WakersUnknown || s.Wakers != nil {
			t.Fatalf("%s: wakers=%+v unknown=%v", name, s.Wakers, s.WakersUnknown)
		}
		_ = r
	}
}

// The root was marked and the process died before its child was: the intent records the root, and
// the recovery walks the tree again from it.
func TestReconcileRetiredFindsChildrenNeverMarked(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	writeMeta(t, session.Meta{Name: "c", Origin: session.OriginSession, OriginSession: "p"})
	r.hold(t, "c")
	in := mustBeginRetire(t, "p")
	retireOne(context.Background(), &retireReport{Name: "p", Unended: []string{}}, in) // the crash comes here
	if m, _ := session.ReadMeta("c"); m.RetiredAt != "" {
		t.Fatal("setup: the child is marked")
	}
	if n := ReconcileRetired(context.Background()); n != 1 {
		t.Fatalf("reconciled %d", n)
	}
	if m, _ := session.ReadMeta("c"); m.RetiredAt == "" {
		t.Fatal("the child of an interrupted retire was never retired")
	}
	if w, tm, _ := r.e.Wakers("c"); w+tm != 0 {
		t.Fatalf("the child still holds %d %d", w, tm)
	}
	if len(retireIntentRoots()) != 0 {
		t.Fatal("a finished recovery kept its intent")
	}
}

// A member the retire had marked and a person resumed since is theirs again; the recovery must not
// mark it and stop it a second time.
func TestReconcileRetiredLeavesASessionAPersonResumed(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	writeMeta(t, session.Meta{Name: "c", Origin: session.OriginSession, OriginSession: "p"})
	in := mustBeginRetire(t, "p")
	for _, n := range []string{"p", "c"} {
		retireOne(context.Background(), &retireReport{Name: n, Unended: []string{}}, in)
	}
	clearRetired("c") // the person's start
	r.hold(t, "c")
	ReconcileRetired(context.Background())
	if m, _ := session.ReadMeta("c"); m.RetiredAt != "" {
		t.Fatal("the recovery retired a session a person had resumed")
	}
	if w, _, _ := r.e.Wakers("c"); w != 1 {
		t.Fatalf("its watches were ended: %d", w)
	}
}

func TestRetiredRuntimeRefusalIsAPermanent409(t *testing.T) {
	rec := httptest.NewRecorder()
	writeRuntimeErr(rec, errSessionRetired)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "session_retired") {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body)
	}
}

// The lock order is the lifecycle gate, then the retire gate. With a move waiting for exclusive
// access behind a running input, a start, a retire and the input's own resume all finish.
func TestRetireGateOrderDoesNotDeadlockWithTheLifecycleGate(t *testing.T) {
	newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	ctx := context.Background()
	input, err := lifegate.Enter(ctx, "a") // a running input
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	go func() { // a move, waiting for the input to finish
		x, err := lifegate.Lock(ctx, "a")
		if err == nil {
			x.Unlock()
		}
	}()
	time.Sleep(100 * time.Millisecond)
	wg.Add(2)
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodPost, "/sessions/a/start", nil)
		req.SetPathValue("name", "a")
		HandleStartSession(httptest.NewRecorder(), req)
	}()
	go func() { defer wg.Done(); time.Sleep(100 * time.Millisecond); postRetire(t, "a") }()
	time.Sleep(400 * time.Millisecond)
	g := retireGate("a") // the input reaching resumeManaged
	g.RLock()
	g.RUnlock()
	input.Release()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("input, start, retire and move wait for each other")
	}
}

// A person typing while the prompt waits for the CLI to boot owns that turn: it must not become
// the delivery's first turn.
func TestInitialPromptDeliveryDoesNotTakeATurnStartedDuringTheBootWait(t *testing.T) {
	newRetireEnv(t)
	dir := t.TempDir()
	writeMeta(t, session.Meta{Name: "q", Kind: session.KindShell, Dir: dir, QueueDelivery: "d1", InitialPromptState: session.InitialPromptPending})
	m, _ := session.ReadMeta("q")
	sid := session.UUID(m.Dir, "q")
	status.ArmFirstTurn(sid, false)
	flag := filepath.Join(t.TempDir(), "booting")
	bin := t.TempDir()
	script := "#!/bin/sh\n[ \"$1\" = -L ] && shift 2\ncase \"$1\" in\n  has-session) [ -e " + flag + " ] || { : > " + flag + "; /bin/sleep 1; } ;;\n  list-panes) echo '1 %1' ;;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	go func() { // the person, while the Agent waits for tmux
		for i := 0; i < 200; i++ {
			if _, err := os.Stat(flag); err == nil {
				status.Persist(sid, "working")
				status.PersistTurnEnd(sid, "idle")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	typeInitialPrompt("q", "the queue's task")
	status.Persist(sid, "working") // the prompt's own turn
	status.PersistTurnEnd(sid, "idle")
	if got, ok := status.ReadFirstTurn(sid); ok {
		t.Fatalf("a turn started during the boot wait was reported as the delivery's: %+v", got)
	}
}

func mustBeginRetire(t *testing.T, root string) *retireIntent {
	t.Helper()
	in, err := beginRetire(root)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// The watch wake starts a stopped owner from inside Ready. A retire that begins meanwhile holds the
// retire gate and ends the owner's subscriptions, which reaches the sink's lock: the start must
// not run under that lock, or the two wait for each other.
func TestWatchWakeAndRetireDoNotWaitForEachOther(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	r.hold(t, "a")
	sink := newWatchSink()
	e := watch.New(watchDiskStore{}, sink, stubSource{}, &watch.Time{})
	e.Barred = watchBarred
	e.SetWaker(r.w)
	useWatchEngine(t, e)
	prev := watchStart
	watchStart = func(name string) error { postRetire(t, name); return nil } // the retire lands during the start
	t.Cleanup(func() { watchStart = prev })
	done := make(chan struct{})
	go func() { sink.Ready("a", time.Now()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Ready and retire wait for each other")
	}
}

func TestRetireWritesItsIntentBeforeMarkingAndSaysSoWhenItCannot(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	writeMeta(t, session.Meta{Name: "k", Origin: session.OriginSession, OriginSession: "a"})
	r.hold(t, "a")
	if err := os.MkdirAll(retireIntents.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(retireIntents.Dir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(retireIntents.Dir(), 0o700) })
	if f, err := os.Create(filepath.Join(retireIntents.Dir(), "probe")); err == nil {
		f.Close()
		t.Skip("the directory stays writable here (running as root?)")
	}
	req := httptest.NewRequest(http.MethodPost, "/sessions/a/retire", nil)
	req.SetPathValue("name", "a")
	rec := httptest.NewRecorder()
	HandleSessionRetire(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "retire_intent_unwritable") {
		t.Fatalf("retire without a writable intent = %d %s", rec.Code, rec.Body)
	}
	if m, _ := session.ReadMeta("a"); m.RetiredAt != "" {
		t.Fatal("a session was marked although its retire could not be recorded")
	}
	if w, _, _ := r.e.Wakers("a"); w != 1 {
		t.Fatalf("subscriptions were ended without a recorded intent: %d", w)
	}
}

// Recovery of a retire reads each member inside the gate: a mark of this retire is finished, a
// tombstone of this retire (a person's start cleared it) is left alone, and a session with neither
// was never reached and is marked for the first time. The crash is after the root's mark.
func TestReconcileRetiredFinishesUnreachedSkipsReleasedAndEndsMarked(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	for _, n := range []string{"marked", "released", "unreached"} {
		writeMeta(t, session.Meta{Name: n, Origin: session.OriginSession, OriginSession: "p"})
		r.hold(t, n)
	}
	in := mustBeginRetire(t, "p")
	for _, n := range []string{"p", "marked", "released"} { // the crash comes before "unreached"
		retireOne(context.Background(), &retireReport{Name: n, Unended: []string{}}, in)
	}
	clearRetired("released") // a person's start, which leaves the tombstone in the same write
	r.hold(t, "released")
	if m, _ := session.ReadMeta("released"); m.RetireClearedID != in.ID || m.RetiredAt != "" {
		t.Fatalf("the clearing did not leave the retire's id: %+v", m)
	}
	if n := ReconcileRetired(context.Background()); n != 1 {
		t.Fatalf("reconciled %d", n)
	}
	for n, want := range map[string]bool{"marked": true, "unreached": true, "released": false} {
		m, _ := session.ReadMeta(n)
		if (m.RetiredAt != "") != want {
			t.Errorf("%s: retired=%v, want %v", n, m.RetiredAt != "", want)
		}
		w, _, _ := r.e.Wakers(n)
		if (w == 0) != want {
			t.Errorf("%s: holds %d watches, retired=%v", n, w, want)
		}
	}
}

// The same, with the clearing waiting behind a start that holds the gate: the recovery reads the
// member only after it has the gate.
func TestReconcileRetiredReadsAMemberUnderTheGate(t *testing.T) {
	newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	writeMeta(t, session.Meta{Name: "c", Origin: session.OriginSession, OriginSession: "p"})
	in := mustBeginRetire(t, "p")
	for _, n := range []string{"p", "c"} {
		retireOne(context.Background(), &retireReport{Name: n, Unended: []string{}}, in)
	}
	g := retireGate("c")
	g.RLock() // a person's /start in progress
	done := make(chan struct{})
	go func() { ReconcileRetired(context.Background()); close(done) }()
	time.Sleep(300 * time.Millisecond)
	clearRetired("c")
	g.RUnlock()
	<-done
	if m, _ := session.ReadMeta("c"); m.RetiredAt != "" {
		t.Fatal("the recovery stopped a session whose start it had been waiting for")
	}
}

// The booking path is guarded by the mark alone: a retired session books nothing, and a booking made
// before the mark is cancelled by the retire, also when the sweep was still booking as it began.
func TestRetireCancelsWhatWasBookedBeforeTheMarkAndNothingAfter(t *testing.T) {
	newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	var dropped []string
	var mu sync.Mutex
	prev := dropRateLimitSchedule
	dropRateLimitSchedule = func(id string) error { mu.Lock(); dropped = append(dropped, id); mu.Unlock(); return nil }
	t.Cleanup(func() { dropRateLimitSchedule = prev })
	at := time.Now().Add(time.Hour).Format(time.RFC3339)
	ep := rateLimitEpisodeLock("a")
	ep.Lock() // a sweep that began before the retire
	done := make(chan struct{})
	go func() { postRetire(t, "a"); close(done) }()
	time.Sleep(300 * time.Millisecond)
	if err := RateLimitStates.Write("a", rateLimitState{At: at, ResumeAt: at, ScheduleID: "sch_before"}); err != nil {
		t.Fatal(err)
	}
	ep.Unlock()
	<-done
	// A sweep after the mark books nothing.
	var booked int
	pp := putRateLimitSchedule
	putRateLimitSchedule = func(session.Meta, time.Time) (string, error) { booked++; return "sch_after", nil }
	t.Cleanup(func() { putRateLimitSchedule = pp })
	m, _ := session.ReadMeta("a")
	rateLimitTickSession(m, time.Now())
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(dropped, ",") != "sch_before" || booked != 0 {
		t.Fatalf("cancelled %v, booked after the mark %d", dropped, booked)
	}
	if _, has := RateLimitStates.Read("a"); has {
		t.Fatal("an episode remains")
	}
}

// The sweep leaves a retired session's episode alone: it neither books nor counts an attempt.
func TestSweepLeavesARetiredSessionsEpisodeAlone(t *testing.T) {
	newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "a", RetiredAt: "2026-11-15T10:00:00Z"})
	now := time.Now()
	st := rateLimitState{At: now.Format(time.RFC3339)}
	if err := RateLimitStates.Write("a", st); err != nil {
		t.Fatal(err)
	}
	m, _ := session.ReadMeta("a")
	rateLimitTickSession(m, now)
	if got, _ := RateLimitStates.Read("a"); got != st {
		t.Fatalf("the sweep touched a retired session's episode: %+v", got)
	}
	// The control: the same episode on a session that is not retired is worked on.
	writeMeta(t, session.Meta{Name: "b"})
	RateLimitStates.Write("b", st)
	mb, _ := session.ReadMeta("b")
	rateLimitTickSession(mb, now)
	if got, _ := RateLimitStates.Read("b"); got == st {
		t.Skip("the sweep does nothing to this episode here, so the guard cannot be told from it")
	}
}

// A repeated retire writes a new intent; the mark takes its id (the first date stays), so a
// person's start between that retire's mark and its end leaves the tombstone the recovery looks for.
func TestRepeatedRetireCrashRespectsAPersonsStart(t *testing.T) {
	newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	writeMeta(t, session.Meta{Name: "c", Origin: session.OriginSession, OriginSession: "p"})
	postRetire(t, "p") // retire A, complete
	first, _ := session.ReadMeta("c")
	in := mustBeginRetire(t, "p") // retire B
	for _, n := range []string{"p", "c"} {
		retireOne(context.Background(), &retireReport{Name: n, Unended: []string{}}, in)
	}
	if m, _ := session.ReadMeta("c"); m.RetireID != in.ID || m.RetiredAt != first.RetiredAt {
		t.Fatalf("mark = %q at %q, want retire %q at the first date %q", m.RetireID, m.RetiredAt, in.ID, first.RetiredAt)
	}
	if err := clearRetired("c"); err != nil { // the crash, then a person's start
		t.Fatal(err)
	}
	ReconcileRetired(context.Background())
	if m, _ := session.ReadMeta("c"); m.RetiredAt != "" {
		t.Fatal("the recovery of a repeated retire stopped a session a person had started")
	}
}

// A start whose clearing of the mark fails does not claim success.
func TestStartDoesNotClaimSuccessWhenClearingTheMarkFails(t *testing.T) {
	newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	postRetire(t, "a")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if err := os.Chmod(session.MetaDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(session.MetaDir(), 0o700) })
	if f, err := os.CreateTemp(session.MetaDir(), "probe"); err == nil {
		f.Close()
		os.Remove(f.Name())
		t.Skip("the meta directory stays writable here (running as root?)")
	}
	req := httptest.NewRequest(http.MethodPost, "/sessions/a/start", nil)
	req.SetPathValue("name", "a")
	rec := httptest.NewRecorder()
	HandleStartSession(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "retire_clear_failed") {
		t.Fatalf("/start = %d %s", rec.Code, rec.Body)
	}
	if m, _ := session.ReadMeta("a"); m.RetiredAt == "" {
		t.Fatal("the mark was cleared although the write failed")
	}
}

// Retire is for roots only. A session another session started is refused (naming its root, at any
// depth), so a second retire rooted at a child cannot be set up; a handoff launch carries
// originSession but was not started by it, and is a root.
func TestRetireIsAcceptedOnlyForARoot(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	writeMeta(t, session.Meta{Name: "c", Origin: session.OriginSession, OriginSession: "p"})
	writeMeta(t, session.Meta{Name: "g", Origin: session.OriginSession, OriginSession: "c"})
	writeMeta(t, session.Meta{Name: "h", Origin: session.OriginHandoff, OriginSession: "p"})
	writeMeta(t, session.Meta{Name: "orphan", Origin: session.OriginSession, OriginSession: "gone"})
	for name, root := range map[string]string{"c": "p", "g": "p", "orphan": "gone"} {
		req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/retire", nil)
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		HandleSessionRetire(rec, req)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "retire_not_root") || !strings.Contains(rec.Body.String(), "session "+root+";") {
			t.Fatalf("%s: %d %s, want 409 retire_not_root naming %s", name, rec.Code, rec.Body, root)
		}
		if m, _ := session.ReadMeta(name); m.RetiredAt != "" {
			t.Fatalf("%s was marked by a refused retire", name)
		}
	}
	r.hold(t, "h")
	if code, out := postRetire(t, "h"); code != http.StatusOK || retiredNames(out.Sessions) != "h" {
		t.Fatalf("a handoff launch is a root: %d %s", code, retiredNames(out.Sessions))
	}
	if code, _ := postRetire(t, "p"); code != http.StatusOK {
		t.Fatalf("the root: %d", code)
	}
}

// Two requests for the same root do not overlap: the second waits for the first, so the first never
// goes on with a tree the second has been through (and a person resumed) in between.
func TestTwoRetiresOfOneRootAreSerialised(t *testing.T) {
	newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	writeMeta(t, session.Meta{Name: "c", Origin: session.OriginSession, OriginSession: "p"})
	g := retireGate("c")
	g.RLock() // a person's start on c: retire A stops at c
	doneA, doneB := make(chan struct{}), make(chan struct{})
	go func() { postRetire(t, "p"); close(doneA) }()
	time.Sleep(300 * time.Millisecond)
	first, _ := retireIntents.Read("p")
	go func() { postRetire(t, "p"); close(doneB) }()
	time.Sleep(300 * time.Millisecond)
	if now, _ := retireIntents.Read("p"); now.ID != first.ID {
		t.Fatal("a second retire of the root began while the first was still running")
	}
	g.RUnlock()
	<-doneA
	<-doneB
	if _, still := retireIntents.Read("p"); still {
		t.Fatal("an intent is left after both retires finished")
	}
}

// A request whose deadline passed while it waited for the root's lock does not run later.
func TestAWaitingRetireRequestThatWasCancelledDoesNotRunLater(t *testing.T) {
	newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	unlock, err := lockRetireRoot(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/sessions/p/retire", nil).WithContext(ctx)
	req.SetPathValue("name", "p")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { HandleSessionRetire(rec, req); close(done) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
	unlock()
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "retire_wait_cancelled") {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body)
	}
	time.Sleep(100 * time.Millisecond)
	if m, _ := session.ReadMeta("p"); m.RetiredAt != "" {
		t.Fatal("a cancelled request retired the session after the lock was released")
	}
}

// A marked session with no intent is a retire that finished; the recovery leaves it alone, and so
// never writes an id over the one a newer retire has on it. (The reviewer's order: a recovery
// pass reading a stale snapshot while a new retire marks.)
func TestRecoveryLeavesAFinishedRetireAndANewerIdAlone(t *testing.T) {
	r := newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	r.hold(t, "p")
	UpdateSessionMeta("p", func(m *session.Meta) bool { m.RetiredAt, m.RetireID = "2026-11-15T10:00:00Z", "B"; return true })
	if n := ReconcileRetired(context.Background()); n != 0 {
		t.Fatalf("reconciled %d with no intent", n)
	}
	if m, _ := session.ReadMeta("p"); m.RetireID != "B" {
		t.Fatalf("the id was overwritten: %q", m.RetireID)
	}
	if w, tm, _ := r.e.Wakers("p"); w != 1 || tm != 1 {
		t.Fatalf("the recovery ended a session no intent names: %d %d", w, tm)
	}
}

// A request whose lock came free just as its deadline passed does not run.
func TestRetireRequestWithACancelledContextDoesNotRunEvenWhenTheLockIsFree(t *testing.T) {
	newRetireEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/sessions/p/retire", nil).WithContext(ctx)
	req.SetPathValue("name", "p")
	rec := httptest.NewRecorder()
	HandleSessionRetire(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body)
	}
	if m, _ := session.ReadMeta("p"); m.RetiredAt != "" {
		t.Fatal("a cancelled request retired the session")
	}
}
