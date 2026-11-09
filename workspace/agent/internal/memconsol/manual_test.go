package memconsol

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin/memberorigintest"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

// lintOnly adds a memory with one fresh lint reason: open, but not ripe (decision 6).
func (f *fakeStore) lintOnly(name, body string) {
	f.put("project", testProject, memoryx.ConsolMemory{Name: name, Description: "d " + name, Type: "project", Revision: 1, Body: body,
		Reasons: []memoryx.ConsolReason{{By: "lint", Rule: "missing-path", Note: name + " mentions a missing path", Revision: 1}}})
}

// waitIdle waits for the background run to release the run lock.
func waitIdle(t *testing.T) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if !currentProgress().Running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the run did not finish")
}

// manualEnv wires the trigger Deps with the fake models and makes sure no run state leaks between
// tests.
func manualEnv(t *testing.T, small, large *scriptModel) (enabled, busy *bool, last *time.Time) {
	t.Helper()
	e, b, l := true, false, time.Time{}
	tickEnv(t, &e, &b, &l, small, large)
	t.Cleanup(func() { waitIdle(t); endRun() })
	return &e, &b, &l
}

func TestManualRunBypassesIdleAndIntervalAndLeavesTheWindowAlone(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	small := &scriptModel{classify: classifyAs("no-op")}
	_, busy, last := manualEnv(t, small, nil)
	*busy, *last = true, t0 // a session is working and a turn just started

	// The automatic trigger keeps its rule: busy, so no run.
	lastRun := t0.Add(-30 * time.Minute) // inside MinInterval
	st := stateOf(t)
	st.Trigger.LastRun = lastRun
	if err := st.save(); err != nil {
		t.Fatal(err)
	}
	if _, ran := Tick(context.Background(), t0); ran || small.calls() != 0 {
		t.Fatal("the automatic trigger ran while busy")
	}

	if err := StartManual(false, t0); err != nil {
		t.Fatal(err)
	}
	waitIdle(t)
	rep, ok := LastReport()
	if !ok || !rep.Manual || rep.Status != RunOK || small.calls() != 1 {
		t.Fatalf("report = %+v ok=%v calls=%d", rep, ok, small.calls())
	}
	tr := stateOf(t).Trigger
	if !tr.LastRun.Equal(lastRun) {
		t.Fatalf("a manual run moved Trigger.LastRun: %v -> %v", lastRun, tr.LastRun)
	}
	if !tr.LastManualRun.Equal(t0) {
		t.Fatalf("LastManualRun = %v", tr.LastManualRun)
	}
}

// With no automatic run on record, a manual run does not leave one: the first idle tick still runs.
func TestManualRunDoesNotConsumeTheAutomaticWindow(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	small := &scriptModel{classify: classifyAs("no-op")}
	manualEnv(t, small, nil)
	if err := StartManual(false, t0); err != nil {
		t.Fatal(err)
	}
	waitIdle(t)
	f.wrong("b", "y", 1)
	if _, ran := Tick(context.Background(), t0.Add(time.Minute)); !ran {
		t.Fatal("a manual run consumed the 3 h window")
	}
}

// Other sessions' turns do not defer a manual run; they do defer an automatic one.
func TestManualRunIsNotDeferredByTurns(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	f.wrong("b", "y", 1)
	small := &scriptModel{classify: classifyAs("no-op")}
	_, busy, _ := manualEnv(t, small, nil)
	*busy = true // every gate sees a working session
	if err := StartManual(false, t0); err != nil {
		t.Fatal(err)
	}
	waitIdle(t)
	rep, _ := LastReport()
	if rep.Status != RunOK || len(rep.Handled) != 2 || len(rep.Delayed) != 0 || small.calls() != 2 {
		t.Fatalf("report = %+v calls=%d", rep, small.calls())
	}
	// The same activity cuts an automatic run (the control: the guard under test is what differs).
	f.wrong("c", "z", 1)
	rep2 := mustRun(t, RunInput{Models: Models{Small: small}, Config: DefaultConfig(), Now: t0.Add(time.Hour), Turns: fakeTurns{started: func() bool { return true }}})
	if rep2.Status != RunCut {
		t.Fatalf("automatic control = %+v", rep2)
	}
}

func TestManualRunRefusedWhenOffAndNothingIsCalled(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	small := &scriptModel{classify: classifyAs("no-op")}
	enabled, _, _ := manualEnv(t, small, nil)
	*enabled = false
	if err := StartManual(false, t0); err != ErrDisabled {
		t.Fatalf("err = %v", err)
	}
	if small.calls() != 0 || !stateOf(t).Trigger.LastManualRun.IsZero() || currentProgress().Running {
		t.Fatal("a refused run left a trace")
	}
	// Unwired Deps never run.
	old := currentDeps()
	Configure(Deps{})
	defer Configure(old)
	if err := StartManual(false, t0); err != ErrDisabled {
		t.Fatalf("unwired err = %v", err)
	}
}

// One run at a time: a second request is refused (not queued), an automatic tick does not start
// beside it nor spend its window, and the status shows running with progress.
func TestOneRunAtATime(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	f.wrong("b", "y", 1)
	gate, entered := make(chan struct{}), make(chan struct{}, 4)
	small := &scriptModel{classify: func(string) (string, error) {
		entered <- struct{}{}
		<-gate
		return classifyAs("no-op")("")
	}}
	manualEnv(t, small, nil)
	if err := StartManual(false, t0); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := StartManual(true, t0); err != ErrRunInFlight {
		t.Fatalf("second request err = %v", err)
	}
	if _, ran := Tick(context.Background(), t0.Add(time.Minute)); ran {
		t.Fatal("an automatic run started beside a manual one")
	}
	if !stateOf(t).Trigger.LastRun.IsZero() {
		t.Fatal("the refused automatic run spent the window")
	}
	v, err := BuildStatus(t0)
	if err != nil || !v.Run.Running || !v.Run.Manual || v.Run.Total != 2 || v.Run.Done != 0 {
		t.Fatalf("status run = %+v, %v", v.Run, err)
	}
	gate <- struct{}{}
	<-entered
	if v, _ := BuildStatus(t0); v.Run.Done != 1 {
		t.Fatalf("progress after one topic = %+v", v.Run)
	}
	close(gate)
	waitIdle(t)
	if v, _ := BuildStatus(t0); v.Run.Running || v.LastRun == nil || !v.LastRun.Manual {
		t.Fatalf("status after = %+v", v)
	}
	if small.calls() != 2 {
		t.Fatalf("calls = %d (the refused request ran something)", small.calls())
	}
}

// An automatic run in flight refuses a manual request too, and the other way round is covered above.
func TestManualRunRefusedWhileAnAutomaticOneRuns(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	small := &scriptModel{classify: func(string) (string, error) {
		entered <- struct{}{}
		<-gate
		return classifyAs("no-op")("")
	}}
	manualEnv(t, small, nil)
	done := make(chan bool)
	go func() { _, ran := Tick(context.Background(), t0); done <- ran }()
	<-entered
	if err := StartManual(false, t0); err != ErrRunInFlight {
		t.Fatalf("err = %v", err)
	}
	if v, _ := BuildStatus(t0); !v.Run.Running || v.Run.Manual {
		t.Fatalf("status run = %+v", v.Run)
	}
	close(gate)
	if !<-done {
		t.Fatal("the automatic run did not run")
	}
}

// Ripeness: only ripe topics by default; "all" takes every waiting topic, except one that already
// has a proposal pending.
func TestManualRunRipenessAndAll(t *testing.T) {
	f := env(t)
	f.wrong("ripe", "x", 1)
	f.lintOnly("young", "y")
	small := &scriptModel{classify: classifyAs("no-op")}
	manualEnv(t, small, nil)
	if err := StartManual(false, t0); err != nil {
		t.Fatal(err)
	}
	waitIdle(t)
	if rep, _ := LastReport(); len(rep.Handled) != 1 || rep.All || small.calls() != 1 {
		t.Fatalf("default run = %+v calls=%d", rep, small.calls())
	}
	if err := StartManual(true, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	waitIdle(t)
	if rep, _ := LastReport(); len(rep.Handled) != 1 || !rep.All || small.calls() != 2 {
		t.Fatalf("all run = %+v calls=%d", rep, small.calls())
	}
}

func TestAllSkipsATopicWithAPendingProposal(t *testing.T) {
	f := env(t)
	f.wrong("p", "trunk is develop\nrest", 1)
	small := &scriptModel{classify: classifyAs("patch", "p"), patch: patchEdit("trunk is develop", "trunk is main")}
	mustRun(t, input(small, nil, t0))
	f.lintOnly("p", "trunk is develop\nrest") // same topic gets a new open observation, proposal still pending
	small2 := &scriptModel{classify: classifyAs("no-op")}
	manualEnv(t, small2, nil)
	if err := StartManual(true, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	waitIdle(t)
	if small2.calls() != 0 {
		t.Fatalf("a topic with a pending proposal was taken: %d calls", small2.calls())
	}
}

// The budget still applies to a manual run: a topic whose estimate is over it is delayed, no call.
func TestManualRunKeepsTheBudget(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	f.wrong("b", "y", 1)
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// One topic costs about 1.9k tokens, so the budget takes one and delays the other.
	if err := os.WriteFile(filepath.Join(Dir(), "config.json"), []byte(`{"tokenBudget":2500}`), 0o600); err != nil {
		t.Fatal(err)
	}
	small := &scriptModel{classify: classifyAs("no-op")}
	manualEnv(t, small, nil)
	if err := StartManual(true, t0); err != nil {
		t.Fatal(err)
	}
	waitIdle(t)
	rep, _ := LastReport()
	if small.calls() != 1 || len(rep.Delayed) != 1 || rep.Delayed[0].Cause != CauseBudget || len(rep.TooLarge) != 0 {
		t.Fatalf("report = %+v calls=%d", rep, small.calls())
	}
}

// Validation still applies and nothing reaches the store before approval.
func TestManualRunKeepsValidationAndApprovalOnlyPublishing(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	f.wrong("b", "trunk is develop\nrest", 1)
	small := &scriptModel{classify: func(p string) (string, error) {
		if strings.Contains(p, "a names a retired branch") {
			return "not json", nil
		}
		return classifyAs("patch", "b")(p)
	}, patch: patchEdit("trunk is develop", "trunk is main")}
	manualEnv(t, small, nil)
	if err := StartManual(false, t0); err != nil {
		t.Fatal(err)
	}
	waitIdle(t)
	rep, _ := LastReport()
	if rep.Dropped != 1 || len(rep.Queued) != 1 || len(pendingList(t, t0)) != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if f.nPublished() != 0 {
		t.Fatal("a manual run wrote the store")
	}
}

// The route: the mark is required (a bare handler and the wrapped one both refuse), nothing starts
// without it, and with it the answer is 202 at once; a second request is 409 run_in_flight, and a
// switched-off one 409 disabled.
func TestRunRouteRequiresTheMarkAndAnswersAsDesigned(t *testing.T) {
	f := env(t)
	f.wrong("a", "x", 1)
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	small := &scriptModel{classify: func(string) (string, error) {
		entered <- struct{}{}
		<-gate
		return classifyAs("no-op")("")
	}}
	enabled, _, _ := manualEnv(t, small, nil)
	body := `{"all":true}`
	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/agents/memory-consolidation/run", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	for label, h := range map[string]http.Handler{"bare": http.HandlerFunc(HandleRun), "wrapped": memberorigin.Require(DecideMaxRequest, http.HandlerFunc(HandleRun))} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req())
		if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized && w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s without a mark: %d %s", label, w.Code, w.Body)
		}
	}
	if currentProgress().Running || small.calls() != 0 {
		t.Fatal("an unmarked request started a run")
	}

	s := memberorigintest.Install(t)
	do := func() *httptest.ResponseRecorder {
		r := req()
		s.Sign(r, []byte(body))
		w := httptest.NewRecorder()
		memberorigin.Require(DecideMaxRequest, http.HandlerFunc(HandleRun)).ServeHTTP(w, r)
		return w
	}
	w := do()
	if w.Code != http.StatusAccepted {
		t.Fatalf("marked: %d %s", w.Code, w.Body)
	}
	<-entered
	w = do()
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), CodeRunInFlight) {
		t.Fatalf("second: %d %s", w.Code, w.Body)
	}
	close(gate)
	waitIdle(t)
	*enabled = false
	if w := do(); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), CodeDisabled) {
		t.Fatalf("off: %d %s", w.Code, w.Body)
	}
	if rep, _ := LastReport(); !rep.All {
		t.Fatalf("all was not honoured: %+v", rep)
	}
}
