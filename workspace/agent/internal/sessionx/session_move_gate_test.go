package sessionx

// The source side of a move (ADR 0115 decision 11): the lifecycle gate on every input path, the
// moving / movedTo transitions, and the usage-limit watch around them. The races are driven with
// a managed fake whose Resume and Send can be held, so "between the check and the commit" is a
// place a test can stop at.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/lifegate"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// moveGuard bounds a wait for something that must happen; it is a hang guard, not a budget.
const moveGuard = 10 * time.Second

// testLease takes name's lease for a test that calls a step requiring one.
func testLease(t *testing.T, name string) *lifegate.Lease {
	t.Helper()
	l, err := lifegate.Enter(context.Background(), name)
	if err != nil {
		t.Fatalf("lease on %s: %v", name, err)
	}
	t.Cleanup(l.Release)
	return l
}

// moveFakeHandle records what reaches the runtime. inSend, when set, runs inside Send before it
// returns: the input is past every check and not yet committed.
type moveFakeHandle struct {
	agents.ThreadHandle
	sent   chan agents.TurnInput
	inSend func()
}

func (h *moveFakeHandle) Send(in agents.TurnInput) error {
	if h.inSend != nil {
		h.inSend()
	}
	h.sent <- in
	return nil
}
func (h *moveFakeHandle) Steer(in agents.TurnInput) error { return h.Send(in) }
func (h *moveFakeHandle) Interrupt(agents.InterruptOpts) (agents.InterruptResult, error) {
	return agents.InterruptResult{}, nil
}
func (h *moveFakeHandle) UpdateSettings(agents.ThreadSettings) error { return nil }
func (h *moveFakeHandle) Snapshot() (agents.ThreadSnapshot, error) {
	return agents.ThreadSnapshot{}, nil
}
func (h *moveFakeHandle) Respond(agents.InteractionReply) error {
	h.sent <- agents.TurnInput{Prompt: "(respond)"}
	return nil
}

type moveFakeDriver struct {
	agents.Driver
	h        *moveFakeHandle
	inResume func()
	resumed  chan string
}

func (d *moveFakeDriver) Capabilities() agents.Capabilities {
	return agents.Capabilities{DynamicModel: true}
}

func (d *moveFakeDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	d.resumed <- m.Name
	if d.inResume != nil {
		d.inResume()
	}
	return d.h, nil
}

// moveEnv isolates HOME and the sessions dir, installs the fake as codex's managed driver,
// writes a managed codex session, and lets a move start from the state the fake reports.
func moveEnv(t *testing.T, name string) (*moveFakeDriver, session.Meta) {
	t.Helper()
	pendingTestEnv(t)
	h := &moveFakeHandle{sent: make(chan agents.TurnInput, 16)}
	d := &moveFakeDriver{h: h, resumed: make(chan string, 16)}
	prev, had := managedDrivers[session.KindCodex]
	managedDrivers[session.KindCodex] = d
	prevState := moveSourceState
	moveSourceState = func(session.Meta) (string, string) { return "idle", "" }
	t.Cleanup(func() {
		moveSourceState = prevState
		if had {
			managedDrivers[session.KindCodex] = prev
		} else {
			delete(managedDrivers, session.KindCodex)
		}
		lifegate.Reopen(name) // the gate's memory is process-wide
	})
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged}
	session.WriteMeta(m)
	return d, m
}

func waitWriterWaiting(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(moveGuard)
	for lifegate.WritersWaiting(name) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the move never started waiting for the gate")
		}
		time.Sleep(time.Millisecond)
	}
}

type beginResult struct {
	mv  *sourceMove
	err error
}

func beginAsync(name, target string) <-chan beginResult {
	out := make(chan beginResult, 1)
	go func() {
		mv, err := beginSessionMove(context.Background(), name, target)
		out <- beginResult{mv, err}
	}()
	return out
}

func mustBegin(t *testing.T, ch <-chan beginResult) *sourceMove {
	t.Helper()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("beginSessionMove: %v", r.err)
		}
		return r.mv
	case <-time.After(moveGuard):
		t.Fatal("the move never got the gate")
	}
	return nil
}

func inputAsync(name, body string) <-chan *httptest.ResponseRecorder {
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/input", strings.NewReader(body))
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		HandleSessionInput(rec, req)
		WaitInputMirrors() // drain the typed-input mirror before the test can finish (#3410)
		out <- rec
	}()
	return out
}

func recvRec(t *testing.T, ch <-chan *httptest.ResponseRecorder, what string) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case rec := <-ch:
		return rec
	case <-time.After(moveGuard):
		t.Fatalf("%s never returned (a deadlock on the gate?)", what)
	}
	return nil
}

// assertMovedRefusal checks a refusal names the target the sender should use instead.
func assertMovedRefusal(t *testing.T, rec *httptest.ResponseRecorder, target string) {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Target  string `json:"target"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusConflict || body.Error.Code != errCodeSessionMoved || body.Error.Target != target ||
		!strings.Contains(body.Error.Message, target) {
		t.Fatalf("got %d %s, want 409 session_moved naming %s", rec.Code, rec.Body.String(), target)
	}
}

func assertNothingSent(t *testing.T, d *moveFakeDriver) {
	t.Helper()
	select {
	case in := <-d.h.sent:
		t.Fatalf("%q reached the runtime of a closed session", in.Prompt)
	case name := <-d.resumed:
		t.Fatalf("the runtime of the closed session %s was resumed", name)
	default:
	}
}

// The managed /input of a stopped session goes /input → Resume → Send. The input holds its
// lease while the move starts waiting, then calls inward: if an inner step took the gate again,
// it would queue behind the waiting move, which waits for the outer lease — a deadlock.
func TestNestedInputCompletesWhileTheMoveWaits(t *testing.T) {
	d, m := moveEnv(t, "mvnest1")
	inResume := make(chan struct{})
	goOn := make(chan struct{})
	d.inResume = func() {
		close(inResume)
		<-goOn
	}
	input := inputAsync(m.Name, `{"prompt":"carry on"}`)
	<-inResume
	moved := beginAsync(m.Name, "mvnest1-target")
	waitWriterWaiting(t, m.Name)
	close(goOn) // the input goes on inward (Send) while the move waits
	if rec := recvRec(t, input, "the input"); rec.Code != http.StatusOK {
		t.Fatalf("input = %d %s", rec.Code, rec.Body.String())
	}
	mustBegin(t, moved)
	if got := (<-d.h.sent).Prompt; got != "carry on" {
		t.Errorf("sent %q", got)
	}
}

// The same through an in-process delivery: deliverPendingPeer holds the lease and reaches
// /input through an HTTP handler, so it hands the lease over on the request's context.
func TestDeliveryLeaseIsBorrowedByInput(t *testing.T) {
	d, m := moveEnv(t, "mvnest2")
	session.WriteMeta(session.Meta{Name: "mvnest2-peer", Dir: t.TempDir(), Kind: session.KindClaude})
	outer := testLease(t, m.Name)
	moved := beginAsync(m.Name, "mvnest2-target")
	waitWriterWaiting(t, m.Name)
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequestWithContext(lifegate.WithLease(context.Background(), outer), http.MethodPost,
			"/sessions/"+m.Name+"/input", strings.NewReader(`{"prompt":"from the spool"}`))
		req.SetPathValue("name", m.Name)
		rec := httptest.NewRecorder()
		HandleSessionInput(rec, req)
		WaitInputMirrors() // drain the typed-input mirror before the test can finish (#3410)
		out <- rec
	}()
	if rec := recvRec(t, out, "the inner /input"); rec.Code != http.StatusOK {
		t.Fatalf("inner input = %d %s", rec.Code, rec.Body.String())
	}
	outer.Release()
	mustBegin(t, moved)
	<-d.h.sent
}

// An input held between its gate check and its commit finishes before the move closes input;
// one that arrives once the move waits is refused, never sent after the snapshot.
func TestInputBetweenCheckAndCommitFinishesBeforeTheMove(t *testing.T) {
	d, m := moveEnv(t, "mvrace1")
	inSend := make(chan struct{})
	goOn := make(chan struct{})
	first := true
	d.h.inSend = func() {
		if first {
			first = false
			close(inSend)
			<-goOn
		}
	}
	held := inputAsync(m.Name, `{"prompt":"one"}`)
	<-inSend
	moved := beginAsync(m.Name, "mvrace1-target")
	waitWriterWaiting(t, m.Name)
	late := inputAsync(m.Name, `{"prompt":"two"}`)
	select {
	case r := <-moved:
		t.Fatalf("the move closed input while an input was mid-commit: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
	close(goOn)
	if rec := recvRec(t, held, "the held input"); rec.Code != http.StatusOK {
		t.Fatalf("held input = %d %s", rec.Code, rec.Body.String())
	}
	mustBegin(t, moved)
	assertMovedRefusal(t, recvRec(t, late, "the late input"), "mvrace1-target")
	if got := (<-d.h.sent).Prompt; got != "one" {
		t.Errorf("sent %q, want one", got)
	}
	<-d.resumed // the held input's
	assertNothingSent(t, d)
}

// Every gated path refuses a source that is moving, and one that was moved, naming the target.
func TestEveryInputPathRefusesAMovedSource(t *testing.T) {
	for _, done := range []bool{false, true} {
		label := "moving"
		if done {
			label = "movedTo"
		}
		t.Run(label, func(t *testing.T) {
			const name, target = "mvrefuse", "mvrefuse-target"
			d, m := moveEnv(t, name)
			m.Model = "mv-model"
			session.WriteMeta(m)
			session.WriteMeta(session.Meta{Name: "mvrefuse-peer", Dir: t.TempDir(), Kind: session.KindClaude})
			mv := mustBegin(t, beginAsync(name, target))
			if done {
				if err := mv.commit(); err != nil {
					t.Fatal(err)
				}
				// A restart: only the meta remembers.
				lifegate.Reopen(name)
				if cur, _ := session.ReadMeta(name); cur.MovedTo != target || cur.Moving != "" {
					t.Fatalf("meta after commit = moving %q movedTo %q", cur.Moving, cur.MovedTo)
				}
			}
			http1 := func(h http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+path, strings.NewReader(body))
				req.SetPathValue("name", name)
				rec := httptest.NewRecorder()
				h(rec, req)
				return rec
			}
			for _, tc := range []struct {
				path, body string
				h          http.HandlerFunc
			}{
				{"/input", `{"prompt":"hello"}`, HandleSessionInput},
				{"/input", `{"keys":["Enter"]}`, HandleSessionInput},
				{"/input", `{"prompt":"hi","peer_from":"mvrefuse-peer","peer_intent":"request"}`, HandleSessionInput},
				{"/input", `{"prompt":"nightly","source":"schedule","schedule_id":"sch_x"}`, HandleSessionInput},
				{"/input", `{"prompt":"later","when_ready":true}`, HandleSessionInput},
				{"/turn", `{"op":"start","prompt":"hello"}`, HandleSessionTurn},
				{"/turn", `{"op":"interrupt"}`, HandleSessionTurn},
				{"/start", ``, HandleStartSession},
				{"/recreate", ``, HandleRecreateSession},
				{"/driver", `{"driver":"tui"}`, HandleSessionDriver},
				{"/respond", `{"id":"x"}`, HandleSessionRespond},
				{"/answer-question", `{"choices":[1]}`, HandleSessionAnswerQuestion},
				{"/plan-respond", `{"decision":"approve"}`, HandleSessionPlanRespond},
				{"/carried-answer", `{"decision":"continue"}`, HandleSessionCarriedAnswer},
				{"/settings", `{"model":"gpt-x"}`, HandleSessionSettings},
			} {
				t.Run(tc.path+" "+tc.body, func(t *testing.T) {
					assertMovedRefusal(t, http1(tc.h, tc.path, tc.body), target)
				})
			}
			// The settings read starts no runtime: it answers from the meta.
			req := httptest.NewRequest(http.MethodGet, "/sessions/"+name+"/settings", nil)
			req.SetPathValue("name", name)
			rec := httptest.NewRecorder()
			HandleSessionSettingsGet(rec, req)
			var got managedThreadSettingsWire
			_ = json.Unmarshal(rec.Body.Bytes(), &got)
			if rec.Code != http.StatusOK || got.Model != "mv-model" || !got.DynamicModel {
				t.Errorf("GET /settings = %d %s, want 200 with the stored model", rec.Code, rec.Body.String())
			}
			if cur, _ := session.ReadMeta(name); cur.Model != "mv-model" {
				t.Errorf("model after the refused POST = %q, want it unchanged", cur.Model)
			}
			var moved *lifegate.MovedError
			if err := injectSessionPrompt(name, "from discord", agents.Origin{Kind: agents.OriginDiscord}); !errors.As(err, &moved) || moved.Target != target {
				t.Errorf("chat-bridge / auto-resume injection = %v, want a MovedError naming %s", err, target)
			}
			if msg, err := answerInteraction(bridge.ParsedInteraction{Session: name, Kind: "question"}); err != nil || !strings.Contains(msg, target) {
				t.Errorf("chat-bridge answer = %q, %v; want a refusal naming %s", msg, err, target)
			}
			if got := typeInitialPrompt(name, "first task"); got != session.InitialPromptFailed {
				t.Errorf("initial prompt = %s, want failed", got)
			}
			// A spooled peer message stays in the spool, where the move's snapshot reads it.
			p := agents.PendingPeer{ID: "pp1", From: "mvrefuse-peer", Intent: "request", Message: "queued earlier", QueuedAt: time.Now()}
			if err := agents.PutPendingPeer(name, p); err != nil {
				t.Fatal(err)
			}
			if deliverPendingPeer(name, p) {
				t.Error("a spooled peer message was delivered to a closed session")
			}
			if list := agents.PendingPeers(name); len(list) != 1 || list[0].ID != "pp1" {
				t.Errorf("spool after the refused delivery = %+v, want pp1 still there", list)
			}
			agents.DropPendingPeers(name, "test end")
			assertNothingSent(t, d)
		})
	}
}

// A second move, and a move of a moved source, are refused; a source mid-question is refused.
func TestBeginRefusesWhatCannotMove(t *testing.T) {
	_, m := moveEnv(t, "mvbegin")
	moveSourceState = func(session.Meta) (string, string) { return "working", "" }
	var se *moveStateError
	if _, err := beginSessionMove(context.Background(), m.Name, "mvbegin-t"); !errors.As(err, &se) || se.State != "working" {
		t.Fatalf("working source: %v", err)
	}
	moveSourceState = func(session.Meta) (string, string) { return "idle", "question" }
	if _, err := beginSessionMove(context.Background(), m.Name, "mvbegin-t"); !errors.As(err, &se) || se.State != "question" {
		t.Fatalf("source with a pending question: %v", err)
	}
	moveSourceState = func(session.Meta) (string, string) { return "limited", "" }
	mv, err := beginSessionMove(context.Background(), m.Name, "mvbegin-t")
	if err != nil {
		t.Fatalf("limited source: %v", err)
	}
	var moved *lifegate.MovedError
	if _, err := beginSessionMove(context.Background(), m.Name, "other"); !errors.As(err, &moved) || moved.Target != "mvbegin-t" {
		t.Fatalf("second move: %v", err)
	}
	if err := mv.rollback(time.Now()); err != nil {
		t.Fatal(err)
	}
	if cur, _ := session.ReadMeta(m.Name); cur.Moving != "" {
		t.Fatalf("moving = %q after rollback", cur.Moving)
	}
	if rec := postInput(t, m.Name, `{"prompt":"back"}`); rec.Code != http.StatusOK {
		t.Fatalf("input after rollback = %d %s", rec.Code, rec.Body.String())
	}
}

// A source whose state still reads idle while its handle has an entry in flight (the pump gave
// its lease back once the runtime had the input, before the runtime's own record shows the turn)
// is refused as working, and the refusal changes nothing. What the drivers report in that window
// is pinned by each driver's TestPump...BeforeAMove.
func TestBeginRefusesASourceWithAnEntryInFlight(t *testing.T) {
	_, m := moveEnv(t, "mvinflight")
	prev := moveSourceInFlight
	t.Cleanup(func() { moveSourceInFlight = prev })
	inFlight := true
	moveSourceInFlight = func(got session.Meta) bool { return got.Name == m.Name && inFlight }

	var se *moveStateError
	if _, err := beginSessionMove(context.Background(), m.Name, "mvinflight-t"); !errors.As(err, &se) || se.State != "working" {
		t.Fatalf("source with an entry in flight: %v, want refused as working", err)
	}
	if cur, _ := session.ReadMeta(m.Name); cur.Moving != "" || cur.MovedTo != "" {
		t.Fatalf("meta after the refusal = moving %q movedTo %q, want neither", cur.Moving, cur.MovedTo)
	}
	if lifegate.Closed(m.Name) {
		t.Fatal("the refused move left input closed")
	}

	inFlight = false // the turn settled: the same source can move now
	mv, err := beginSessionMove(context.Background(), m.Name, "mvinflight-t")
	if err != nil {
		t.Fatalf("source with nothing in flight: %v", err)
	}
	if err := mv.rollback(time.Now()); err != nil {
		t.Fatal(err)
	}
}

// A sweep that read its ListMetas before the move closed input must not book from that stale
// snapshot: the gate re-reads the meta.
func TestRateLimitTickFromAStaleSnapshotBooksNothing(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	f.codexReset, f.codexResetOK = now.Add(time.Hour), true
	_, m := moveEnv(t, "mvtick1")
	setRateLimitPref(t, true)
	stale := m
	// A booking that failed earlier leaves an episode the follow-up would book for.
	if err := RateLimitStates.Write(m.Name, rateLimitState{At: now.Add(-time.Minute).Format(time.RFC3339), ScheduleTries: 1}); err != nil {
		t.Fatal(err)
	}
	mustBegin(t, beginAsync(m.Name, "mvtick1-target"))
	rateLimitTickSession(stale, now)
	if f.scheduled != 0 {
		t.Fatalf("a sweep booked %d resume(s) for a session whose input is closed", f.scheduled)
	}
}

// A sweep that is booking when the move starts finishes first and records its id; the commit
// then cancels it. Nothing booked survives the move.
func TestRateLimitTickRacingTheMoveLeavesNoBooking(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	f.codexReset, f.codexResetOK = now.Add(time.Hour), true
	_, m := moveEnv(t, "mvtick2")
	setRateLimitPref(t, true)
	if err := RateLimitStates.Write(m.Name, rateLimitState{At: now.Add(-time.Minute).Format(time.RFC3339), ScheduleTries: 1}); err != nil {
		t.Fatal(err)
	}
	inPut := make(chan struct{})
	goOn := make(chan struct{})
	origPut := putRateLimitSchedule
	putRateLimitSchedule = func(mm session.Meta, at time.Time) (string, error) {
		close(inPut)
		<-goOn
		return origPut(mm, at)
	}
	ticked := make(chan struct{})
	go func() {
		rateLimitTickSession(m, now)
		close(ticked)
	}()
	<-inPut
	moved := beginAsync(m.Name, "mvtick2-target")
	waitWriterWaiting(t, m.Name)
	close(goOn)
	<-ticked
	mv := mustBegin(t, moved)
	if err := mv.commit(); err != nil {
		t.Fatal(err)
	}
	if f.scheduled != 1 || len(f.deleted) != 1 || f.deleted[0] != "sch_test" {
		t.Fatalf("booked %d, cancelled %v; want the one booking cancelled", f.scheduled, f.deleted)
	}
	if _, has := RateLimitStates.Read(m.Name); has {
		t.Error("the moved source still has a usage-limit episode")
	}
	if p := pendingRateLimitCleanups(); len(p) != 0 {
		t.Errorf("cancellations pending: %v", p)
	}
	rateLimitTickSession(m, now.Add(time.Minute))
	if f.scheduled != 1 {
		t.Errorf("a sweep after the move booked again (%d)", f.scheduled)
	}
}

// A cancellation that fails while the CP is unreachable is kept on disk, finished by the sweep
// after an Agent restart, and the booking that fires meanwhile does not wake the source.
func TestFailedCancelIsFinishedAfterRestart(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	d, m := moveEnv(t, "mvcancel")
	setRateLimitPref(t, true)
	if err := RateLimitStates.Write(m.Name, rateLimitState{At: now.Format(time.RFC3339),
		ResumeAt: now.Add(time.Hour).Format(time.RFC3339), ScheduleID: "sch_live"}); err != nil {
		t.Fatal(err)
	}
	f.dropErr = errors.New("dial tcp: connection refused")
	mv := mustBegin(t, beginAsync(m.Name, "mvcancel-target"))
	if err := mv.commit(); err != nil {
		t.Fatal(err)
	}
	if p := pendingRateLimitCleanups(); len(p) != 1 || p["sch_live"].Session != m.Name {
		t.Fatalf("pending cancellations = %v, want sch_live kept", p)
	}
	if _, has := RateLimitStates.Read(m.Name); has {
		t.Error("the episode stayed open for display")
	}

	// The Agent restarts with the CP still unreachable: the record survives a failed retry.
	lifegate.ResetForTest()
	rateLimitTick(now.Add(time.Minute))
	if p := pendingRateLimitCleanups(); p["sch_live"].Tries != 2 {
		t.Fatalf("after a failed retry: %v", p)
	}
	// The booking fires before the CP is back: refused, and the source is not woken.
	assertMovedRefusal(t, postInput(t, m.Name, `{"prompt":"`+rateLimitResumePrompt()+`","source":"schedule","schedule_id":"sch_live"}`),
		"mvcancel-target")
	// The CP is back: the next sweep finishes the cancellation.
	f.dropErr = nil
	rateLimitTick(now.Add(2 * time.Minute))
	if p := pendingRateLimitCleanups(); len(p) != 0 {
		t.Fatalf("pending cancellations after the CP came back = %v", p)
	}
	if n := len(f.deleted); n != 3 || f.deleted[n-1] != "sch_live" {
		t.Errorf("deletes = %v, want three attempts at sch_live", f.deleted)
	}
	if f.scheduled != 0 {
		t.Errorf("booked %d resume(s) for a moved source", f.scheduled)
	}
	assertNothingSent(t, d)
}

// A booking that fired during a move that then rolled back is booked again; without the repair
// the episode would keep the spent id and the watch would never book.
func TestRollbackRebooksABookingTheMoveRefused(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	d, m := moveEnv(t, "mvrebook")
	setRateLimitPref(t, true)
	if err := RateLimitStates.Write(m.Name, rateLimitState{At: now.Add(-time.Hour).Format(time.RFC3339),
		ResumeAt: now.Add(-time.Minute).Format(time.RFC3339), ScheduleID: "sch_spent", ScheduleTries: 1}); err != nil {
		t.Fatal(err)
	}
	mv := mustBegin(t, beginAsync(m.Name, "mvrebook-target"))
	assertMovedRefusal(t, postInput(t, m.Name, `{"prompt":"`+rateLimitResumePrompt()+`","source":"schedule","schedule_id":"sch_spent"}`),
		"mvrebook-target")
	if st := stateOf(t, m.Name); st.FiredDuringMove != "sch_spent" {
		t.Fatalf("the refused booking was not recorded: %+v", st)
	}
	if err := mv.rollback(now); err != nil {
		t.Fatal(err)
	}
	st := stateOf(t, m.Name)
	if f.scheduled != 1 || st.ScheduleID != "sch_test" || st.FiredDuringMove != "" {
		t.Fatalf("after rollback: booked %d, episode %+v; want one new booking", f.scheduled, st)
	}
	if want := now.Add(rateLimitResumeLead); !f.scheduleAt.Equal(want) {
		t.Errorf("booked at %v, want as soon as the scheduler allows (%v)", f.scheduleAt, want)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "sch_spent" {
		t.Errorf("deletes = %v, want the spent row removed", f.deleted)
	}
	assertNothingSent(t, d)

	// A rollback with no refused booking leaves the episode alone.
	mv = mustBegin(t, beginAsync(m.Name, "mvrebook-target"))
	if err := mv.rollback(now); err != nil {
		t.Fatal(err)
	}
	if f.scheduled != 1 {
		t.Errorf("a rollback with nothing refused booked again (%d)", f.scheduled)
	}
}

// The user's explicit reopen is the only way back in.
func TestReopenMovedSessionOpensInput(t *testing.T) {
	_, m := moveEnv(t, "mvreopen")
	mv := mustBegin(t, beginAsync(m.Name, "mvreopen-target"))
	if err := mv.commit(); err != nil {
		t.Fatal(err)
	}
	assertMovedRefusal(t, postInput(t, m.Name, `{"prompt":"x"}`), "mvreopen-target")
	if err := reopenMovedSession(m.Name); err != nil {
		t.Fatal(err)
	}
	if rec := postInput(t, m.Name, `{"prompt":"x"}`); rec.Code != http.StatusOK {
		t.Fatalf("after reopen = %d %s", rec.Code, rec.Body.String())
	}
}

// scheduleRefusal is the source's own booking firing into a closed source.
func scheduleRefusal(id string) string {
	return `{"prompt":"` + rateLimitResumePrompt() + `","source":"schedule","schedule_id":"` + id + `"}`
}

func spentEpisode(t *testing.T, name string, now time.Time) {
	t.Helper()
	if err := RateLimitStates.Write(name, rateLimitState{At: now.Add(-time.Hour).Format(time.RFC3339),
		ResumeAt: now.Add(-time.Minute).Format(time.RFC3339), ScheduleID: "sch_spent", ScheduleTries: 1}); err != nil {
		t.Fatal(err)
	}
}

func useSeam(t *testing.T, seam *func(), fn func()) {
	t.Helper()
	*seam = fn
	t.Cleanup(func() { *seam = nil })
}

// A refusal that reaches its record while a rollback is between its repair and the reopening
// waits for the rollback, and then repairs the booking itself: recorded in that gap, it would
// be lost, the rollback having already repaired.
func TestRefusalDuringTheRollbackIsRepaired(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	_, m := moveEnv(t, "mvgap1")
	setRateLimitPref(t, true)
	spentEpisode(t, m.Name, now)
	mv := mustBegin(t, beginAsync(m.Name, "mvgap1-target"))
	atNote := make(chan struct{}, 1)
	useSeam(t, &beforeNoteRefusal, func() { atNote <- struct{}{} })
	var refused <-chan *httptest.ResponseRecorder
	useSeam(t, &afterMoveRepair, func() {
		refused = inputAsync(m.Name, scheduleRefusal("sch_spent"))
		<-atNote
		time.Sleep(50 * time.Millisecond) // let it reach the episode lock, or record without it
	})
	if err := mv.rollback(now); err != nil {
		t.Fatal(err)
	}
	assertMovedRefusal(t, recvRec(t, refused, "the refused booking"), "mvgap1-target")
	if st := stateOf(t, m.Name); f.scheduled != 1 || st.ScheduleID != "sch_test" || st.FiredDuringMove != "" {
		t.Fatalf("booked %d, episode %+v; want the spent booking replaced", f.scheduled, st)
	}
}

// A refused request that stalls between its refusal and its record until the rollback is over
// repairs the booking itself, since no rollback will come for it.
func TestRefusalRecordedAfterTheRollbackIsRepaired(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	_, m := moveEnv(t, "mvgap2")
	setRateLimitPref(t, true)
	spentEpisode(t, m.Name, now)
	mv := mustBegin(t, beginAsync(m.Name, "mvgap2-target"))
	atNote, goOn := make(chan struct{}), make(chan struct{})
	useSeam(t, &beforeNoteRefusal, func() {
		close(atNote)
		<-goOn
	})
	refused := inputAsync(m.Name, scheduleRefusal("sch_spent"))
	<-atNote
	if err := mv.rollback(now); err != nil {
		t.Fatal(err)
	}
	if f.scheduled != 0 {
		t.Fatalf("the rollback booked with nothing recorded (%d)", f.scheduled)
	}
	close(goOn)
	assertMovedRefusal(t, recvRec(t, refused, "the refused booking"), "mvgap2-target")
	if st := stateOf(t, m.Name); f.scheduled != 1 || st.ScheduleID != "sch_test" {
		t.Fatalf("booked %d, episode %+v; want the spent booking replaced", f.scheduled, st)
	}
}

// The commit records the booking for cancellation before the episode lets go of it, so an
// Agent that stops while the CP call is in flight still finds the id after the restart.
func TestCommitKeepsTheBookingAcrossARestartMidCancel(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	_, m := moveEnv(t, "mvmid")
	setRateLimitPref(t, true)
	if err := RateLimitStates.Write(m.Name, rateLimitState{At: now.Format(time.RFC3339),
		ResumeAt: now.Add(time.Hour).Format(time.RFC3339), ScheduleID: "sch_live"}); err != nil {
		t.Fatal(err)
	}
	mv := mustBegin(t, beginAsync(m.Name, "mvmid-target"))
	inFlight := false
	dropRateLimitSchedule = func(id string) error {
		f.deleted = append(f.deleted, id)
		if !inFlight {
			inFlight = true
			// What a restart right now would find on disk.
			all, _ := rateLimitCleanups.Read(rateLimitCleanupKey)
			if _, ok := all[id]; !ok {
				t.Errorf("the CP is being called for %s with no durable record of it", id)
			}
			if _, has := RateLimitStates.Read(m.Name); has {
				t.Error("the episode is still open while the cancellation is in flight")
			}
			return errors.New("agent stopped mid-call")
		}
		return f.dropErr
	}
	if err := mv.commit(); err != nil {
		t.Fatal(err)
	}
	lifegate.ResetForTest()
	rateLimitTick(now.Add(time.Minute))
	if p := pendingRateLimitCleanups(); len(p) != 0 {
		t.Fatalf("pending after the restart's sweep = %v", p)
	}
	if len(f.deleted) != 2 || f.deleted[1] != "sch_live" {
		t.Errorf("deletes = %v", f.deleted)
	}
}

// A cancellation that cannot be recorded fails the commit with nothing changed: closing the
// episode then would leave the booking in no store at all.
func TestCommitFailsWhenTheCancellationCannotBeRecorded(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	_, m := moveEnv(t, "mvnorec")
	if err := RateLimitStates.Write(m.Name, rateLimitState{At: now.Format(time.RFC3339), ScheduleID: "sch_live"}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, rateLimitCleanups.Dir(), "not a directory")
	mv := mustBegin(t, beginAsync(m.Name, "mvnorec-target"))
	if err := mv.commit(); err == nil {
		t.Fatal("commit succeeded with no record of the cancellation")
	}
	if st, has := RateLimitStates.Read(m.Name); !has || st.ScheduleID != "sch_live" {
		t.Errorf("episode = %+v, %v; want it kept with its id", st, has)
	}
	if cur, _ := session.ReadMeta(m.Name); cur.Moving != "mvnorec-target" || cur.MovedTo != "" {
		t.Errorf("meta = moving %q movedTo %q; want still moving", cur.Moving, cur.MovedTo)
	}
	if len(f.deleted) != 0 {
		t.Errorf("deletes = %v, want none before the record", f.deleted)
	}
}

// A stopped Managed cursor source is refused with a reason: its conversation lived only in the
// handle. Read through the real liveness check, not the test's state stand-in.
func TestStoppedManagedCursorSourceIsRefused(t *testing.T) {
	moveEnv(t, "mvcursor-unused")
	session.WriteMeta(session.Meta{Name: "mvcursor", Dir: t.TempDir(), Kind: session.KindCursor, Driver: session.DriverManaged})
	if _, err := beginSessionMove(context.Background(), "mvcursor", "mvcursor-target"); !errors.Is(err, errMoveCursorStopped) {
		t.Fatalf("begin = %v, want errMoveCursorStopped", err)
	}
	if cur, _ := session.ReadMeta("mvcursor"); cur.Moving != "" {
		t.Fatalf("moving = %q after a refused begin", cur.Moving)
	}
	// A stopped Terminal cursor keeps its transcript file, so it may move.
	session.WriteMeta(session.Meta{Name: "mvcursor2", Dir: t.TempDir(), Kind: session.KindCursor})
	t.Cleanup(func() { lifegate.Reopen("mvcursor2") })
	if _, err := beginSessionMove(context.Background(), "mvcursor2", "mvcursor2-target"); err != nil {
		t.Fatalf("Terminal cursor: %v", err)
	}
}

// A refusal repaired after a rollback and the sweep both rewrite the episode. The sweep's step
// waits for the repair under the episode lock and then reads the episode the repair wrote; had
// it read earlier, its write (here the stale episode's removal) would drop the new booking,
// which would then fire with neither the episode nor a commit knowing its id.
func TestSweepWaitsForARepairAfterTheRollback(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	_, m := moveEnv(t, "mvsweep")
	setRateLimitPref(t, true)
	spentEpisode(t, m.Name, now.Add(-time.Hour)) // stale by now: the sweep would retire it
	mv := mustBegin(t, beginAsync(m.Name, "mvsweep-target"))
	if err := mv.rollback(now); err != nil {
		t.Fatal(err)
	}
	inPut, goOn := make(chan struct{}), make(chan struct{})
	origPut := putRateLimitSchedule
	putRateLimitSchedule = func(mm session.Meta, at time.Time) (string, error) {
		close(inPut)
		<-goOn
		return origPut(mm, at)
	}
	noted := make(chan struct{})
	go func() {
		noteBookingRefusedDuringMove(m.Name, "schedule", "sch_spent", now)
		close(noted)
	}()
	<-inPut
	ticked := make(chan struct{})
	go func() {
		rateLimitTickSession(m, now)
		close(ticked)
	}()
	select {
	case <-ticked:
		t.Fatal("the sweep's step ran while the repair was rewriting the episode")
	case <-time.After(50 * time.Millisecond):
	}
	close(goOn)
	<-noted
	<-ticked
	if st, ok := RateLimitStates.Read(m.Name); !ok || st.ScheduleID != "sch_test" {
		t.Fatalf("after the sweep: has=%v %+v; want the repaired booking kept", ok, st)
	}
	if f.scheduled != 1 {
		t.Errorf("booked %d, want 1", f.scheduled)
	}
}

// A commit that recorded the cancellation and then stopped: after the restart the sweep cancels
// the booking, and a rollback of that move must still see the id as dead and book again.
func TestRollbackAfterARestartThatFinishedTheCancel(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	_, m := moveEnv(t, "mvhalf")
	setRateLimitPref(t, true)
	spentEpisode(t, m.Name, now)
	mv := mustBegin(t, beginAsync(m.Name, "mvhalf-target"))
	if err := recordRateLimitCancel(m.Name, "sch_spent"); err != nil { // commit's first step
		t.Fatal(err)
	}
	lifegate.ResetForTest()
	rateLimitTick(now.Add(time.Minute))
	if len(f.deleted) != 1 || len(pendingRateLimitCleanups()) != 0 {
		t.Fatalf("after the restart's sweep: deletes %v, pending %v", f.deleted, pendingRateLimitCleanups())
	}
	if err := mv.rollback(now); err != nil {
		t.Fatal(err)
	}
	if st := stateOf(t, m.Name); st.ScheduleID != "sch_test" || f.scheduled != 1 {
		t.Fatalf("after rollback: booked %d, episode %+v; want the dead id replaced", f.scheduled, st)
	}
	// The confirmed id is forgotten once nothing names it.
	rateLimitTick(now.Add(2 * time.Minute))
	if rateLimitCancelRecorded("sch_spent") {
		t.Error("the confirmed cancellation outlived the episode that named it")
	}
}

// A transition returns success only once it is on disk: after a restart the meta is the only
// record that a source is closed. A write that fails leaves the meta and the in-memory state
// agreeing, and the move where it was.
func TestMoveTransitionsPersistBeforeTheyReturn(t *testing.T) {
	newRateLimitFixture(t)
	now := time.Now()
	_, m := moveEnv(t, "mvpersist")
	setRateLimitPref(t, true)
	if err := RateLimitStates.Write(m.Name, rateLimitState{At: now.Format(time.RFC3339), ScheduleID: "sch_live"}); err != nil {
		t.Fatal(err)
	}
	// A read-only meta file is replaced, not written in place.
	p := session.MetaPath(m.Name)
	if err := os.Chmod(p, 0o400); err != nil {
		t.Fatal(err)
	}
	mv := mustBegin(t, beginAsync(m.Name, "mvpersist-target"))
	if cur, _ := session.ReadMeta(m.Name); cur.Moving != "mvpersist-target" {
		t.Fatalf("begin returned with moving %q on disk", cur.Moving)
	}
	dir := session.MetaDir()
	readOnly := func() { _ = os.Chmod(dir, 0o500) }
	writable := func() { _ = os.Chmod(dir, 0o700) }
	t.Cleanup(writable)
	restartRefuses := func(what string) {
		t.Helper()
		lifegate.ResetForTest()
		var moved *lifegate.MovedError
		if l, err := lifegate.Enter(context.Background(), m.Name); !errors.As(err, &moved) {
			l.Release()
			t.Fatalf("%s: after a restart Enter = %v, want the source still closed", what, err)
		}
	}

	readOnly()
	if err := mv.commit(); err == nil {
		t.Fatal("commit succeeded with movedTo not written")
	}
	if cur, _ := session.ReadMeta(m.Name); cur.Moving != "mvpersist-target" || cur.MovedTo != "" {
		t.Fatalf("after a failed commit: moving %q movedTo %q", cur.Moving, cur.MovedTo)
	}
	if _, has := RateLimitStates.Read(m.Name); !has {
		t.Fatal("a failed commit closed the episode")
	}
	if err := mv.rollback(now); err == nil {
		t.Fatal("rollback succeeded with moving not cleared on disk")
	}
	assertMovedRefusal(t, postInput(t, m.Name, `{"prompt":"x"}`), "mvpersist-target")
	restartRefuses("failed rollback")

	writable()
	if err := mv.rollback(now); err != nil {
		t.Fatal(err)
	}
	// A begin that cannot write moving fails, and leaves the source open everywhere.
	readOnly()
	if _, err := beginSessionMove(context.Background(), m.Name, "mvpersist-target"); err == nil {
		t.Fatal("begin succeeded with moving not written")
	}
	writable()
	if rec := postInput(t, m.Name, `{"prompt":"still open"}`); rec.Code != http.StatusOK {
		t.Fatalf("after a failed begin = %d %s", rec.Code, rec.Body.String())
	}
}

// A booking refused while the meta cannot be read is still recorded as spent: the gate refused
// it, so it is dead whichever state the move is in, and the rollback books it again.
func TestRefusalDuringAMetaRewriteIsKept(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	_, m := moveEnv(t, "mvtorn")
	setRateLimitPref(t, true)
	spentEpisode(t, m.Name, now)
	mv := mustBegin(t, beginAsync(m.Name, "mvtorn-target"))
	p := session.MetaPath(m.Name)
	saved, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"name":`), 0o600); err != nil { // a rewrite caught half-done
		t.Fatal(err)
	}
	assertMovedRefusal(t, postInput(t, m.Name, scheduleRefusal("sch_spent")), "mvtorn-target")
	if err := os.WriteFile(p, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mv.rollback(now); err != nil {
		t.Fatal(err)
	}
	if st := stateOf(t, m.Name); f.scheduled != 1 || st.ScheduleID != "sch_test" {
		t.Fatalf("booked %d, episode %+v; want the refused booking replaced", f.scheduled, st)
	}
}

// A refusal recorded on a session that is open again without a rollback having repaired it is
// repaired by the sweep.
func TestSweepRepairsARecordedRefusalOnAnOpenSession(t *testing.T) {
	f := newRateLimitFixture(t)
	now := time.Now()
	_, m := moveEnv(t, "mvsweepfix")
	setRateLimitPref(t, true)
	if err := RateLimitStates.Write(m.Name, rateLimitState{At: now.Add(-time.Hour).Format(time.RFC3339),
		ResumeAt: now.Add(-time.Minute).Format(time.RFC3339), ScheduleID: "sch_spent", FiredDuringMove: "sch_spent"}); err != nil {
		t.Fatal(err)
	}
	rateLimitTickSession(m, now)
	if st := stateOf(t, m.Name); f.scheduled != 1 || st.ScheduleID != "sch_test" || st.FiredDuringMove != "" {
		t.Fatalf("booked %d, episode %+v; want the refused booking replaced", f.scheduled, st)
	}
}
