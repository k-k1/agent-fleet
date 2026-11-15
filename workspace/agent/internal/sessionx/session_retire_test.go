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
	// A loop in the chain (p started by its own descendant) must not spin.
	writeMeta(t, session.Meta{Name: "p", Origin: session.OriginSession, OriginSession: "gg"})

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
	r.alive["a"] = true // mid-turn: not barred yet
	r.e.SetWaker(&cancelHook{retireWaker: r.w, fn: func() {
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
