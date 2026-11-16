package sessionx

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

// listActMark answers GET /sessions and returns the actMark it carried (0: none).
func listActMark(t *testing.T) int64 {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleListSessions(rec, httptest.NewRequest(http.MethodGet, "/sessions", nil))
	var env struct {
		ActMark int64 `json:"actMark"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("list answer: %v: %s", err, rec.Body)
	}
	return env.ActMark
}

// postRetireIf is postRetire with the body {"ifNoActSince": n}; n < 0 sends no body.
func postRetireIf(t *testing.T, name string, n int64) (int, retireResp) {
	t.Helper()
	var body *bytes.Reader
	if n < 0 {
		body = bytes.NewReader(nil)
	} else {
		b, _ := json.Marshal(retireReq{IfNoActSince: n})
		body = bytes.NewReader(b)
	}
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/retire", body)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleSessionRetire(rec, req)
	var out retireResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func reportOf(out retireResp, name string) retireReport {
	for _, r := range out.Sessions {
		if r.Name == name {
			return r
		}
	}
	return retireReport{}
}

// actEnv is a retire environment with a tmux that says every session is alive and answers every
// query with one pane, and a managed fake driver for codex.
// actHandle is a managed handle that takes every kind of delivery.
type actHandle struct{ *queueingFakeHandle }

func (h actHandle) Steer(in agents.TurnInput) error { h.got <- in; return nil }

type actDriver struct {
	agents.Driver
	h agents.ThreadHandle
}

func (d *actDriver) Resume(session.Meta) (agents.ThreadHandle, error) { return d.h, nil }

type actEnv struct {
	*retireEnv
	handle *queueingFakeHandle
}

func newActEnv(t *testing.T) *actEnv {
	t.Helper()
	r := newRetireEnv(t)
	withTempHome(t)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	resetActMarks()
	t.Cleanup(resetActMarks)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\necho 1 %1\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	h := &queueingFakeHandle{got: make(chan agents.TurnInput, 16)}
	prev, had := managedDrivers[session.KindCodex]
	managedDrivers[session.KindCodex] = &actDriver{h: actHandle{h}}
	t.Cleanup(func() {
		if had {
			managedDrivers[session.KindCodex] = prev
			return
		}
		delete(managedDrivers, session.KindCodex)
	})
	return &actEnv{r, h}
}

func postTo(t *testing.T, h http.HandlerFunc, name, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/"+path, strings.NewReader(body))
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	h(rec, req)
	WaitInputMirrors()
	return rec
}

// The mark: new for every list answer, never reused and never lower across a restart, whatever the
// clock did.
func TestActMarkIsNewPerListAndNeverLowerAfterARestart(t *testing.T) {
	newActEnv(t)
	a, b := listActMark(t), listActMark(t)
	if a == 0 || b <= a {
		t.Fatalf("marks %d then %d", a, b)
	}
	resetActMarks() // a restart
	if c := listActMark(t); c <= b {
		t.Fatalf("after a restart %d, before it %d", c, b)
	}
	// The clock went back behind what the disk reserved: the reservation wins.
	st, _ := actMarkStore.Read(actMarkKey)
	if err := actMarkStore.Write(actMarkKey, actMarkState{Reserved: st.Reserved + 1<<40}); err != nil {
		t.Fatal(err)
	}
	resetActMarks()
	if d := listActMark(t); d <= st.Reserved+1<<40 {
		t.Fatalf("mark %d is not above the reserved ceiling %d", d, st.Reserved+1<<40)
	}
}

// An act reads the current mark and a list takes a new one under one lock, so an act is after a
// list exactly when it carries the list's mark or higher, across a restart in between too.
func TestPersonActMarkIsOrderedAgainstTheList(t *testing.T) {
	newActEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	before := listActMark(t)
	notePersonAct("a")
	m, _ := session.ReadMeta("a")
	if m.PersonActMark < before {
		t.Fatalf("an act after a list carries %d, the list %d", m.PersonActMark, before)
	}
	after := listActMark(t)
	if m.PersonActMark >= after {
		t.Fatalf("an act before a list carries %d, the list %d", m.PersonActMark, after)
	}
	resetActMarks()
	notePersonAct("a") // after a restart, after the list
	m, _ = session.ReadMeta("a")
	if m.PersonActMark < after {
		t.Fatalf("an act after a list (and a restart) carries %d, the list %d", m.PersonActMark, after)
	}
}

// Typing in the pane writes the meta at most once per mark.
func TestPaneTypingWritesTheMetaOncePerMark(t *testing.T) {
	newActEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	notePersonAct("a")
	p := session.MetaPath("a")
	if err := os.Chtimes(p, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	// At an unchanged mark a keystroke is a map lookup: it takes neither the gate nor the meta lock
	// (held here), and writes nothing.
	sessionLockMu.Lock()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			_ = NotePersonActPane("a")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		sessionLockMu.Unlock()
		t.Fatal("a keystroke at an unchanged mark went to the meta")
	}
	sessionLockMu.Unlock()
	if fi, _ := os.Stat(p); fi.ModTime().Unix() != 1 {
		t.Fatal("a keystroke at an unchanged mark wrote the meta")
	}
	listActMark(t)
	_ = NotePersonActPane("a")
	if fi, _ := os.Stat(p); fi.ModTime().Unix() == 1 {
		t.Fatal("a keystroke at a new mark did not write the meta")
	}
}

// A keystroke waits while a retire holds the write side, and records on the marked session when
// the retire is done: it is never sent on the strength of a failed try.
func TestPaneTypingWaitsForTheRetireAndRecordsOnTheMarkedSession(t *testing.T) {
	newActEnv(t)
	writeMeta(t, session.Meta{Name: "a", RetiredAt: time.Now().UTC().Format(time.RFC3339), RetireID: "r"})
	g := retireGate("a")
	g.Lock() // the retire is still stopping it
	done := make(chan bool, 1)
	go func() { done <- NotePersonActPane("a") }()
	select {
	case <-done:
		t.Fatal("the key went on before the retire was done")
	case <-time.After(2500 * time.Millisecond): // past the 2 s the unrecorded bypass used to wait
	}
	g.Unlock()
	if ok := <-done; !ok {
		t.Fatal("the key was dropped")
	}
	if m, _ := session.ReadMeta("a"); m.PersonActMark == 0 {
		t.Fatal("the key was not recorded")
	}
}

// A key that cannot be recorded within the hold bound is dropped: not sent, not recorded.
func TestPaneTypingIsDroppedWhenTheGateCannotBeTaken(t *testing.T) {
	newActEnv(t)
	prev := paneActHold
	paneActHold = 200 * time.Millisecond
	t.Cleanup(func() { paneActHold = prev })
	writeMeta(t, session.Meta{Name: "a"})
	g := retireGate("a")
	g.Lock()
	defer g.Unlock()
	if NotePersonActPane("a") {
		t.Fatal("a key that could not be recorded was sent")
	}
	if m, _ := session.ReadMeta("a"); m.PersonActMark != 0 {
		t.Fatal("a dropped key was recorded")
	}
}

// Failing to take the read side is not evidence that the session is marked, and an older retire's
// mark in the meta is not evidence that the retire that is waiting has marked it: with a long
// /start holding the read side, a pending retire and an old mark, the key is held until the
// writer has run.
func TestPaneTypingIsNotSentOnAFailedTryOrAnOldMark(t *testing.T) {
	newActEnv(t)
	writeMeta(t, session.Meta{Name: "a", RetiredAt: time.Now().UTC().Format(time.RFC3339), RetireID: "old"})
	listed := listActMark(t)
	g := retireGate("a")
	g.RLock() // a person's /start, mid-launch, under the old mark
	retired := make(chan retireResp, 1)
	go func() { _, out := postRetireIf(t, "a", listed); retired <- out }()
	for i := 0; i < 200; i++ { // the retire is queued on the write side
		if g.TryRLock() {
			g.RUnlock()
			time.Sleep(5 * time.Millisecond)
			continue
		}
		break
	}
	done := make(chan bool, 1)
	go func() { done <- NotePersonActPane("a") }()
	select {
	case <-done:
		t.Fatal("the key went through unrecorded behind a pending retire and an old mark")
	case <-time.After(2500 * time.Millisecond): // past the 2 s the unrecorded bypass used to wait
	}
	g.RUnlock()
	<-retired
	if ok := <-done; !ok {
		t.Fatal("the key was dropped")
	}
}

// A condition above the Agent's own counter was not handed out by it (a restored older state): it
// proves nothing, so the tree is released, never retired.
func TestRetireWithANumberFromTheFutureReleasesTheTree(t *testing.T) {
	newActEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	writeMeta(t, session.Meta{Name: "c", Origin: session.OriginSession, OriginSession: "p"})
	listed := listActMark(t)
	// The state file is replaced by an older copy and the Agent restarts below the number the
	// Control Plane still holds.
	if err := actMarkStore.Write(actMarkKey, actMarkState{Reserved: 5}); err != nil {
		t.Fatal(err)
	}
	actMarks.mu.Lock()
	actMarks.loaded, actMarks.cur, actMarks.reserved = true, 5, 5
	actMarks.mu.Unlock()
	_, out := postRetireIf(t, "p", listed+1000)
	if !reportOf(out, "p").Released {
		t.Fatalf("%+v", out.Sessions)
	}
	for _, n := range []string{"p", "c"} {
		if m, _ := session.ReadMeta(n); m.RetiredAt != "" {
			t.Fatalf("%s was retired by a number the Agent never handed out", n)
		}
	}
}

// Only an input with none of the automatic origin fields is the member's own.
func TestInputOriginFieldsDecideWhetherItIsAnAct(t *testing.T) {
	bodies := map[string]bool{
		`{"prompt":"x"}`:                                        true,
		`{"keys":["Down"]}`:                                     true,
		`{"prompt":"x","source":"operator"}`:                    false,
		`{"prompt":"x","source":"spawn"}`:                       false,
		`{"prompt":"x","source":"queue"}`:                       false,
		`{"prompt":"x","source":"whatever"}`:                    false,
		`{"prompt":"x","source":"schedule"}`:                    false,
		`{"prompt":"x","schedule_id":"s1"}`:                     false,
		`{"prompt":"x","schedule_id":"s1","schedule_slot":"2"}`: false,
		`{"prompt":"x","report_to":"conv"}`:                     false,
	}
	for body, isAct := range bodies {
		e := newActEnv(t)
		_ = e
		writeMeta(t, session.Meta{Name: "s"})
		listed := listActMark(t)
		postTo(t, HandleSessionInput, "s", "input", body)
		_, out := postRetireIf(t, "s", listed)
		if got := reportOf(out, "s").Released; got != isAct {
			t.Errorf("%s: released=%v, want %v", body, got, isAct)
		}
	}
}

// A relaunch by a person that goes round /start (a driver switch) is refused on a session a retire
// has marked, so none leaves it marked and running.
func TestDriverSwitchRefusesAMarkedSession(t *testing.T) {
	newActEnv(t)
	writeMeta(t, session.Meta{Name: "s", Kind: session.KindCodex})
	postRetire(t, "s")
	rec := postTo(t, HandleSessionDriver, "s", "driver", `{"driver":"managed"}`)
	m, _ := session.ReadMeta("s")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "session_retired") || m.RetiredAt == "" || m.Driver == session.DriverManaged {
		t.Fatalf("%d %s retiredAt=%q driver=%q", rec.Code, rec.Body, m.RetiredAt, m.Driver)
	}
}

// Each kind of act after the list answer whose mark the retire carries leaves the session alone
// and says released; the same act before that list answer does not; an input with an origin of its
// own never counts.
func TestRetireWithACondition(t *testing.T) {
	type kase struct {
		name    string
		kind    string
		driver  string
		act     func(t *testing.T, e *actEnv, name string)
		isAnAct bool
	}
	input := func(body string) func(*testing.T, *actEnv, string) {
		return func(t *testing.T, _ *actEnv, name string) {
			if rec := postTo(t, HandleSessionInput, name, "input", body); rec.Code != http.StatusOK {
				t.Fatalf("input %s = %d %s", body, rec.Code, rec.Body)
			}
		}
	}
	cases := []kase{
		{"console start", session.KindClaude, "", func(t *testing.T, _ *actEnv, name string) {
			if rec := postTo(t, HandleStartSession, name, "start", ""); rec.Code != http.StatusOK {
				t.Fatalf("start = %d %s", rec.Code, rec.Body)
			}
		}, true},
		{"composer input to a Terminal session", session.KindClaude, "", input(`{"prompt":"go on"}`), true},
		{"a key press through /input", session.KindClaude, "", input(`{"keys":["Down"]}`), true},
		{"composer input to a Managed session", session.KindCodex, session.DriverManaged, input(`{"prompt":"go on"}`), true},
		{"a turn steer to a Managed session", session.KindCodex, session.DriverManaged, func(t *testing.T, _ *actEnv, name string) {
			if rec := postTo(t, HandleSessionTurn, name, "turn", `{"op":"steer","prompt":"go on"}`); rec.Code != http.StatusOK {
				t.Fatalf("turn = %d %s", rec.Code, rec.Body)
			}
		}, true},
		{"typing in the terminal pane", session.KindClaude, "", func(t *testing.T, _ *actEnv, name string) { _ = NotePersonActPane(name) }, true},
		{"the chat bridge", session.KindCodex, session.DriverManaged, func(t *testing.T, _ *actEnv, name string) {
			if err := injectSessionPrompt(name, "go on", turnOrigin(TurnSourceDiscord, "")); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"a schedule's input", session.KindClaude, "", input(`{"prompt":"go on","source":"schedule"}`), false},
		{"an operator's input with report_to", session.KindClaude, "", input(`{"prompt":"go on","report_to":"conv"}`), false},
		{"a notice", session.KindCodex, session.DriverManaged, func(t *testing.T, _ *actEnv, name string) {
			if err := injectSessionPrompt(name, "a watch fired", agents.Origin{Kind: agents.OriginNotice}); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"an automatic start", session.KindClaude, "", func(t *testing.T, _ *actEnv, name string) {
			req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/start", nil)
			req.SetPathValue("name", name)
			rec := httptest.NewRecorder()
			HandleStartSession(rec, asAutomaticStart(req))
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newActEnv(t)
			writeMeta(t, session.Meta{Name: "s", Kind: c.kind, Driver: c.driver})
			listed := listActMark(t)
			c.act(t, e, "s")
			code, out := postRetireIf(t, "s", listed)
			rep := reportOf(out, "s")
			m, _ := session.ReadMeta("s")
			if c.isAnAct {
				if code != http.StatusOK || !rep.Released || rep.Stopped || out.Complete || m.RetiredAt != "" {
					t.Fatalf("an act after the list: %d %+v complete=%v retiredAt=%q", code, rep, out.Complete, m.RetiredAt)
				}
				if m.RetireClearedID == "" {
					t.Fatal("the release is not recorded the way a person's start records it")
				}
			} else if rep.Released || !rep.Stopped || m.RetiredAt == "" || m.PersonActMark != 0 {
				t.Fatalf("not an act, yet: %+v retiredAt=%q personActMark=%d", rep, m.RetiredAt, m.PersonActMark)
			}
		})
		if !c.isAnAct {
			continue
		}
		t.Run(c.name+" before the list", func(t *testing.T) {
			e := newActEnv(t)
			writeMeta(t, session.Meta{Name: "s", Kind: c.kind, Driver: c.driver})
			c.act(t, e, "s")
			listed := listActMark(t)
			_, out := postRetireIf(t, "s", listed)
			if rep := reportOf(out, "s"); rep.Released || !rep.Stopped {
				t.Fatalf("an act before the list released the session: %+v", rep)
			}
		})
	}
}

// Without a body, or with 0, a session a person acted on is retired as before.
func TestRetireWithoutAConditionIgnoresActs(t *testing.T) {
	for _, n := range []int64{-1, 0} {
		newActEnv(t)
		writeMeta(t, session.Meta{Name: "s"})
		listActMark(t)
		notePersonAct("s")
		_, out := postRetireIf(t, "s", n)
		if rep := reportOf(out, "s"); rep.Released || !rep.Stopped {
			t.Fatalf("n=%d: %+v", n, rep)
		}
	}
}

// A person acts on a child only: the child and what is under it are released, the rest is retired.
func TestRetireLeavesAChildAPersonActedOnAndWhatIsUnderIt(t *testing.T) {
	r := newActEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	writeMeta(t, session.Meta{Name: "c", Origin: session.OriginSession, OriginSession: "p"})
	writeMeta(t, session.Meta{Name: "g", Origin: session.OriginSession, OriginSession: "c"})
	writeMeta(t, session.Meta{Name: "other", Origin: session.OriginSession, OriginSession: "p"})
	r.hold(t, "g")
	listed := listActMark(t)
	notePersonAct("c")
	_, out := postRetireIf(t, "p", listed)
	if !reportOf(out, "c").Released {
		t.Fatalf("the child was not released: %+v", out.Sessions)
	}
	if rep := reportOf(out, "g"); rep.Name != "" && (rep.Stopped || rep.Ended != (watch.Ended{})) {
		t.Fatalf("what is under the child was retired: %+v", rep)
	}
	if m, _ := session.ReadMeta("g"); m.RetiredAt != "" {
		t.Fatal("the grandchild carries the mark")
	}
	if w, tm, _ := r.e.Wakers("g"); w != 1 || tm != 1 {
		t.Fatalf("the grandchild's subscriptions were ended: %d %d", w, tm)
	}
	for _, n := range []string{"p", "other"} {
		if m, _ := session.ReadMeta(n); m.RetiredAt == "" {
			t.Fatalf("%s was not retired", n)
		}
	}
	if out.Complete {
		t.Fatal("complete with a released member")
	}
}

// A released root ends the retire.
func TestRetireOfAReleasedRootLeavesTheTree(t *testing.T) {
	newActEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	writeMeta(t, session.Meta{Name: "c", Origin: session.OriginSession, OriginSession: "p"})
	listed := listActMark(t)
	notePersonAct("p")
	_, out := postRetireIf(t, "p", listed)
	if !reportOf(out, "p").Released || out.Complete {
		t.Fatalf("%+v", out)
	}
	if m, _ := session.ReadMeta("c"); m.RetiredAt != "" {
		t.Fatal("a child of a released root was retired")
	}
	if _, still := retireIntents.Read("p"); still {
		t.Fatal("the intent of a retire that ended at a released root stays")
	}
}

// The condition is kept with the intent, and the recovery applies it.
func TestRecoveryAppliesTheStoredCondition(t *testing.T) {
	newActEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	writeMeta(t, session.Meta{Name: "c", Origin: session.OriginSession, OriginSession: "p"})
	listed := listActMark(t)
	notePersonAct("c")
	in, err := beginRetire("p", listed) // the process died after the intent, before any mark
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := retireIntents.Read("p"); got.IfNoActSince != listed || got.ID != in.ID {
		t.Fatalf("intent %+v", got)
	}
	resetActMarks() // the restart
	ReconcileRetired(context.Background())
	if m, _ := session.ReadMeta("c"); m.RetiredAt != "" || m.RetireClearedID != in.ID {
		t.Fatalf("the recovery retired a session a person acted on: %+v", m)
	}
	if m, _ := session.ReadMeta("p"); m.RetiredAt == "" {
		t.Fatal("the recovery left the rest of the tree")
	}
}

// An act that races the retire of the same session ends in one of two states and no third.
// First order: the act is past its gate when the retire's check runs, so the check sees it.
func TestActBeforeTheRetireChecksIsSeenByTheCheck(t *testing.T) {
	newActEnv(t)
	writeMeta(t, session.Meta{Name: "s"})
	listed := listActMark(t)
	g := retireGate("s")
	g.RLock() // the act's side: it holds the read side from its record to its end
	if err := recordPersonAct("s"); err != nil {
		t.Fatal(err)
	}
	var out retireResp
	done := make(chan struct{})
	go func() { _, out = postRetireIf(t, "s", listed); close(done) }()
	select {
	case <-done:
		t.Fatal("the retire went past an act holding the read side")
	case <-time.After(200 * time.Millisecond):
	}
	g.RUnlock()
	<-done
	if rep := reportOf(out, "s"); !rep.Released || rep.Stopped {
		t.Fatalf("the retire did not see the act: %+v", rep)
	}
}

// Second order: the retire holds the write side from the check to the end of the stop, so an act
// that arrives meanwhile waits, and lands on a session that is already marked and stopped.
func TestActDuringTheRetireLandsOnARetiredSession(t *testing.T) {
	r := newActEnv(t)
	writeMeta(t, session.Meta{Name: "s"})
	r.hold(t, "s")
	listed := listActMark(t)
	inRetire, release := make(chan struct{}), make(chan struct{})
	r.w.cancel = nil
	hook := &cancelHook{retireWaker: r.w, fn: func() { close(inRetire); <-release }}
	r.e.SetWaker(hook)

	var out retireResp
	retired := make(chan struct{})
	go func() { _, out = postRetireIf(t, "s", listed); close(retired) }()
	<-inRetire // inside the gate, marked and stopping

	acted := make(chan struct{})
	go func() {
		postTo(t, HandleSessionInput, "s", "input", `{"keys":["Down"]}`)
		close(acted)
	}()
	select {
	case <-acted:
		t.Fatal("the act went through while the retire held the session's gate")
	case <-time.After(300 * time.Millisecond):
	}
	if m, _ := session.ReadMeta("s"); m.PersonActMark != 0 {
		t.Fatal("the act was recorded under the write side")
	}
	close(release)
	<-retired
	<-acted
	m, _ := session.ReadMeta("s")
	if rep := reportOf(out, "s"); rep.Released || !rep.Stopped || m.RetiredAt == "" {
		t.Fatalf("not retired: %+v retiredAt=%q", rep, m.RetiredAt)
	}
	if m.PersonActMark < listed {
		t.Fatalf("the act that arrived at the retired session is not recorded: %d", m.PersonActMark)
	}
	// A second retire on the same list does not turn the session's retirement into a release.
	_, again := postRetireIf(t, "s", listed)
	if rep := reportOf(again, "s"); rep.Released {
		t.Fatalf("an act that arrived at a retired session released it: %+v", rep)
	}
}

// The release is written under the member's gate: a start in flight (read side held) is seen, and
// the walk reads a parent's release before the child's own check, so one released above releases
// all below.
func TestReleaseByAnActReachesWhatIsUnderItWithinOneRound(t *testing.T) {
	newActEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	writeMeta(t, session.Meta{Name: "c", Origin: session.OriginSession, OriginSession: "p"})
	writeMeta(t, session.Meta{Name: "g", Origin: session.OriginSession, OriginSession: "c"})
	listed := listActMark(t)
	notePersonAct("c")
	in, _ := beginRetire("p", listed)
	reps := retireTree(context.Background(), "p", in)
	byName := map[string]retireReport{}
	for _, r := range reps {
		byName[r.Name] = r
	}
	if !byName["c"].Released || byName["g"].Stopped || byName["p"].Released {
		t.Fatalf("%+v", reps)
	}
}

// A request with a body that is not the one the route takes is refused and ends nothing.
func TestRetireRefusesABadBody(t *testing.T) {
	newActEnv(t)
	writeMeta(t, session.Meta{Name: "s"})
	req := httptest.NewRequest(http.MethodPost, "/sessions/s/retire", strings.NewReader("{nope"))
	req.SetPathValue("name", "s")
	rec := httptest.NewRecorder()
	HandleSessionRetire(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if m, _ := session.ReadMeta("s"); m.RetiredAt != "" {
		t.Fatal("a refused request retired the session")
	}
}

// A read of the meta that lands in the middle of an ordinary writer (which truncates and rewrites
// the file under the meta lock) must not stand for "no act": once the writer is done the retire
// sees the act and releases the session, for the member and for what is above it.
func TestRetireDoesNotReadAHalfWrittenMetaAsNoAct(t *testing.T) {
	newActEnv(t)
	writeMeta(t, session.Meta{Name: "p"})
	writeMeta(t, session.Meta{Name: "c", Origin: session.OriginSession, OriginSession: "p"})
	listed := listActMark(t)
	notePersonAct("p")
	notePersonAct("c")
	in, err := beginRetire("p", listed)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"p", "c"} {
		path := session.MetaPath(name)
		full, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sessionLockMu.Lock() // an ordinary writer, between its truncate and its write
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		rep := &retireReport{Name: name, Unended: []string{}}
		go func() { retireOne(context.Background(), rep, in); close(done) }()
		select {
		case <-done:
			sessionLockMu.Unlock()
			t.Fatalf("%s: the retire went on with a meta it could not read", name)
		case <-time.After(300 * time.Millisecond):
		}
		if err := os.WriteFile(path, full, 0o600); err != nil {
			t.Fatal(err)
		}
		sessionLockMu.Unlock()
		<-done
		if m, _ := session.ReadMeta(name); m.RetiredAt != "" || !rep.Released {
			t.Fatalf("%s: marked or not released after the writer finished: %+v %+v", name, rep, m)
		}
	}
}

// A meta that exists but cannot be read (a writer that is not under the meta lock, caught between
// truncate and write) is in doubt: the member, or a member below an ancestor like that, is
// neither marked nor stopped, the answer says so, and a repeat once the file is whole decides
// on the act as it should.
func TestRetireLeavesAnUnreadableMetaInDoubt(t *testing.T) {
	for _, which := range []string{"member", "ancestor"} {
		t.Run(which, func(t *testing.T) {
			newActEnv(t)
			writeMeta(t, session.Meta{Name: "p"})
			writeMeta(t, session.Meta{Name: "c", Origin: session.OriginSession, OriginSession: "p"})
			listed := listActMark(t)
			notePersonAct("c")
			path := session.MetaPath("c")
			if which == "ancestor" {
				path = session.MetaPath("p")
			}
			full, _ := os.ReadFile(path)
			if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			in, _ := beginRetire("p", listed)
			rep := &retireReport{Name: "c", Unended: []string{}}
			retireOne(context.Background(), rep, in)
			if rep.Stopped || rep.Released || len(rep.Unended) == 0 {
				t.Fatalf("an unreadable meta was not left in doubt: %+v", rep)
			}
			if err := os.WriteFile(path, full, 0o600); err != nil {
				t.Fatal(err)
			}
			if m, _ := session.ReadMeta("c"); m.RetiredAt != "" {
				t.Fatal("marked while in doubt")
			}
			rep = &retireReport{Name: "c", Unended: []string{}}
			retireOne(context.Background(), rep, in)
			if !rep.Released || rep.Stopped {
				t.Fatalf("the repeat did not see the act: %+v", rep)
			}
		})
	}
}

// Only focus reports are not a person's act. Every other write is, including the terminal's other
// replies (a byte grammar cannot tell them from keys: Shift+F3 is ESC [ 1 ; 2 R).
func TestPaneOnlyFocusReportsAreNotActs(t *testing.T) {
	notActs := map[string]string{
		"focus in":  "\x1b[I",
		"focus out": "\x1b[O",
		"both":      "\x1b[I\x1b[O",
		"several":   "\x1b[O\x1b[I\x1b[O",
	}
	acts := map[string]string{
		"Shift+F3 (a CPR by grammar)": "\x1b[1;2R",
		"device attributes reply":     "\x1b[?62;4c",
		"OSC colour reply":            "\x1b]11;rgb:0000/0000/0000\x07",
		"DCS reply":                   "\x1bP>|xterm.js(5.5.0)\x1b\\",
		"focus then a byte":           "\x1b[Ix",
		"byte then focus":             "x\x1b[I",
		"focus cut (first write)":     "\x1b[",
		"focus cut (second write)":    "I",
		"a key":                       "a",
		"arrow":                       "\x1b[A",
		"mouse report":                "\x1b[<0;10;5M",
	}
	check := func(name, in string, wantAct bool) {
		newActEnv(t)
		writeMeta(t, session.Meta{Name: "s"})
		listed := listActMark(t)
		if !NotePersonActInput("s", []byte(in)) {
			t.Fatalf("%s: dropped", name)
		}
		_, out := postRetireIf(t, "s", listed)
		if got := reportOf(out, "s").Released; got != wantAct {
			t.Errorf("%s: act=%v, want %v", name, got, wantAct)
		}
	}
	for n, in := range notActs {
		check(n, in, false)
	}
	for n, in := range acts {
		check(n, in, true)
	}
}

// A mark or a release that cannot be written, or a meta that cannot be read inside the update, is
// in doubt: nothing is ended or stopped, and the member is reported unended.
func TestRetireEndsAndStopsOnlyAfterTheMarkIsDurable(t *testing.T) {
	cases := map[string]struct {
		cond   bool // a person acted: the update would write a release
		break_ func(t *testing.T)
	}{
		"mark cannot be written": {false, func(t *testing.T) {
			if err := os.Chmod(session.MetaDir(), 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(session.MetaDir(), 0o700) })
		}},
		"release cannot be written": {true, func(t *testing.T) {
			if err := os.Chmod(session.MetaDir(), 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(session.MetaDir(), 0o700) })
		}},
		"meta unreadable": {false, func(t *testing.T) {
			if err := os.WriteFile(session.MetaPath("s"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newActEnv(t)
			if os.Geteuid() == 0 && strings.Contains(name, "written") {
				t.Skip("root writes into a read-only directory")
			}
			writeMeta(t, session.Meta{Name: "s"})
			r.hold(t, "s")
			listed := listActMark(t)
			if c.cond {
				notePersonAct("s")
			}
			in, _ := beginRetire("s", listed)
			c.break_(t)
			rep := &retireReport{Name: "s", Unended: []string{}}
			retireOne(context.Background(), rep, in)
			if rep.Stopped || len(rep.Unended) == 0 || rep.Ended != (watch.Ended{}) {
				t.Fatalf("went on with a mark that was not durable: %+v", rep)
			}
			if w, tm, _ := r.e.Wakers("s"); w != 1 || tm != 1 {
				t.Fatalf("the subscriptions were ended: %d %d", w, tm)
			}
		})
	}
}

// A meta that becomes unreadable between the walk's read and the update is in doubt inside the
// update too: markOrRelease says so, and writes nothing.
func TestMarkOrReleaseSaysDoubtOnAnUnreadableMeta(t *testing.T) {
	newActEnv(t)
	writeMeta(t, session.Meta{Name: "s"})
	in, _ := beginRetire("s", 0)
	if err := os.WriteFile(session.MetaPath("s"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := &retireReport{Name: "s", Unended: []string{}}
	if state, _ := markOrRelease(rep, in); state != markDoubt || len(rep.Unended) == 0 {
		t.Fatalf("state %v, %+v", state, rep)
	}
	if b, _ := os.ReadFile(session.MetaPath("s")); string(b) != "{" {
		t.Fatalf("the meta was written: %q", b)
	}
}

// A meta that decodes with a type error still leaves a name behind, and the checked updater does not
// run the callback for it: that is in doubt too, with a reason in unended, so the retire is not
// complete and its intent stays.
func TestMarkOrReleaseSaysDoubtOnATypeInvalidMeta(t *testing.T) {
	newActEnv(t)
	writeMeta(t, session.Meta{Name: "s"})
	in, _ := beginRetire("s", 0)
	bad := `{"name":"s","personActMark":"bad"}`
	if err := os.WriteFile(session.MetaPath("s"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := &retireReport{Name: "s", Unended: []string{}}
	state, _ := markOrRelease(rep, in)
	if state != markDoubt || len(rep.Unended) == 0 {
		t.Fatalf("state %v, %+v", state, rep)
	}
	if retireComplete([]retireReport{*rep}) {
		t.Fatal("a member in doubt was reported complete")
	}
	if b, _ := os.ReadFile(session.MetaPath("s")); string(b) != bad {
		t.Fatalf("the meta was written: %q", b)
	}
}

// listActEnvelope answers GET /sessions and returns the mark and the incarnation it carried.
func listActEnvelope(t *testing.T) (int64, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleListSessions(rec, httptest.NewRequest(http.MethodGet, "/sessions", nil))
	var env struct {
		ActMark        int64  `json:"actMark"`
		ActIncarnation string `json:"actIncarnation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("list answer: %v: %s", err, rec.Body)
	}
	return env.ActMark, env.ActIncarnation
}

func postRetireWith(t *testing.T, name string, req retireReq) (int, string) {
	t.Helper()
	b, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/retire", bytes.NewReader(b))
	r.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleSessionRetire(rec, r)
	return rec.Code, rec.Body.String()
}

// The case the identity exists for: the state file is restored from an older copy, the counter
// passes again through numbers it handed out, a person acts under a reused number below the old
// retire's, and the retire decided on the old list arrives. Without the identity it stops the
// session the person acted on.
func TestRetireFromBeforeAStateRestoreStopsNothing(t *testing.T) {
	setup := func(t *testing.T) (oldMark int64, oldInc string) {
		newActEnv(t)
		writeMeta(t, session.Meta{Name: "a"})
		// The run that made the list: its counter is far ahead of the clock.
		if err := actMarkStore.Write(actMarkKey, actMarkState{Reserved: time.Now().UnixMilli() + 1<<40}); err != nil {
			t.Fatal(err)
		}
		resetActMarks()
		oldMark, oldInc = listActEnvelope(t)
		// The restore: an older copy of the file, and a restart.
		if err := actMarkStore.Write(actMarkKey, actMarkState{Reserved: oldMark - 10}); err != nil {
			t.Fatal(err)
		}
		resetActMarks()
		notePersonAct("a") // under a reused number, below oldMark
		for i := 0; i < 20; i++ {
			listActMark(t) // the counter passes oldMark again
		}
		if m, _ := session.ReadMeta("a"); m.PersonActMark == 0 || m.PersonActMark >= oldMark {
			t.Fatalf("setup: the act carries %d, want a reused number below %d", m.PersonActMark, oldMark)
		}
		return oldMark, oldInc
	}
	t.Run("with the identity of the old run it is refused", func(t *testing.T) {
		mark, inc := setup(t)
		code, body := postRetireWith(t, "a", retireReq{IfNoActSince: mark, ActIncarnation: inc})
		if code != http.StatusConflict || !strings.Contains(body, errActIncarnationChanged) {
			t.Fatalf("retire = %d %s, want 409 %s", code, body, errActIncarnationChanged)
		}
		if m, _ := session.ReadMeta("a"); m.RetiredAt != "" || m.RetireID != "" {
			t.Fatalf("a refused retire marked the session: %+v", m)
		}
	})
	t.Run("a Control Plane that sends no identity is read as before", func(t *testing.T) {
		mark, _ := setup(t)
		if code, _ := postRetireWith(t, "a", retireReq{IfNoActSince: mark}); code != http.StatusOK {
			t.Fatalf("retire = %d", code)
		}
		if m, _ := session.ReadMeta("a"); m.RetiredAt == "" {
			t.Fatal("documented gap closed by accident: an identity-less retire is meant to behave as before")
		}
	})
}

// A retire that carries this run's identity behaves exactly as before.
func TestRetireWithThisRunsIdentityBehavesAsBefore(t *testing.T) {
	newActEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	writeMeta(t, session.Meta{Name: "b"})
	mark, inc := listActEnvelope(t)
	if inc == "" {
		t.Fatal("the list carries no identity")
	}
	notePersonAct("b") // after the list: released, as without an identity
	if code, body := postRetireWith(t, "a", retireReq{IfNoActSince: mark, ActIncarnation: inc}); code != http.StatusOK {
		t.Fatalf("retire a = %d %s", code, body)
	}
	if m, _ := session.ReadMeta("a"); m.RetiredAt == "" {
		t.Fatal("a session nobody acted on was not retired")
	}
	if code, _ := postRetireWith(t, "b", retireReq{IfNoActSince: mark, ActIncarnation: inc}); code != http.StatusOK {
		t.Fatalf("retire b = %d", code)
	}
	if m, _ := session.ReadMeta("b"); m.RetiredAt != "" {
		t.Fatal("a session a person acted on after the list was retired")
	}
}

// Every start of the Agent has an identity of its own, and the mark stays below the next run's.
func TestActIncarnationChangesWithEachLoadOfTheCounter(t *testing.T) {
	newActEnv(t)
	_, a := listActEnvelope(t)
	_, again := listActEnvelope(t)
	resetActMarks()
	_, b := listActEnvelope(t)
	if a == "" || a != again || a == b {
		t.Fatalf("identities %q, %q, then after a restart %q", a, again, b)
	}
	// A retire with no condition ignores the identity.
	if code, _ := postRetireWith(t, "ghost", retireReq{ActIncarnation: a}); code == http.StatusConflict {
		t.Fatal("an unconditional retire was refused for its identity")
	}
}
