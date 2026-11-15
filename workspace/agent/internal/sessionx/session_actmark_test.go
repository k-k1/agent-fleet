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
			NotePersonActPane("a")
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
	NotePersonActPane("a")
	if fi, _ := os.Stat(p); fi.ModTime().Unix() == 1 {
		t.Fatal("a keystroke at a new mark did not write the meta")
	}
}

// The pane's writer does not wait for a retire that is stopping the session for long.
func TestPaneTypingDoesNotWaitForeverOnARetire(t *testing.T) {
	newActEnv(t)
	writeMeta(t, session.Meta{Name: "a"})
	g := retireGate("a")
	g.Lock()
	defer g.Unlock()
	done := make(chan struct{})
	go func() { NotePersonActPane("a"); close(done) }()
	select {
	case <-done:
	case <-time.After(paneActWait + 3*time.Second):
		t.Fatal("the pane's writer is stuck behind the retire gate")
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
		{"typing in the terminal pane", session.KindClaude, "", func(t *testing.T, _ *actEnv, name string) { NotePersonActPane(name) }, true},
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
