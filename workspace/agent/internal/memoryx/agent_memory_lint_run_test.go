package memoryx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func postLintApply(t *testing.T, pid string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/agents/memory/entries/lint-apply",
		strings.NewReader(`{"scope":"project","project":"`+pid+`"}`))
	rec := httptest.NewRecorder()
	HandleAgentMemoryLintApply(rec, req)
	return rec
}

// A manual lint --apply and the scheduled run never overlap: while one holds the run lock the other
// is refused without doing anything (the manual one with 409, the scheduled one with ErrLintRunning).
func TestLintApplyRunsDoNotOverlap(t *testing.T) {
	e := newLintApplyEnv(t, map[string]string{"docs/real.md": "x\n"})
	e.save("m", "uses docs/gone.md", 0)
	AgentMemoryEnabled = func() bool { return true }

	inRun, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	var once sync.Once
	go func() {
		_, err := LintApplyAll(laNow, func() bool {
			once.Do(func() { close(inRun) })
			<-release
			return true
		})
		done <- err
	}()
	<-inRun // the scheduled run is inside, holding the lock

	head := e.head()
	if rec := postLintApply(t, e.pid); rec.Code != http.StatusConflict {
		t.Errorf("manual run during a scheduled one: %d %s", rec.Code, rec.Body)
	}
	if _, err := LintApplyAll(laNow, nil); err != ErrLintRunning {
		t.Errorf("second scheduled run: %v", err)
	}
	if e.head() != head {
		t.Error("a refused run wrote")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The lock is free again: the manual run now goes through and finds nothing left to mark.
	if rec := postLintApply(t, e.pid); rec.Code != http.StatusOK {
		t.Errorf("manual run after: %d %s", rec.Code, rec.Body)
	}
	if len(lintReasons(e.stored("m"), "lint")) != 1 {
		t.Errorf("reasons = %+v", e.stored("m").Reasons)
	}
}

// One scheduled run has one write budget across the user scope and every project, so the cap
// bounds the run and not each scope; what does not fit is Deferred for the next run.
func TestLintApplyAllSharesOneBudgetAcrossScopes(t *testing.T) {
	e := newLintApplyEnv(t, map[string]string{"docs/real.md": "x\n"})
	AgentMemoryEnabled = func() bool { return true }
	e.save("p", "uses docs/gone.md", 0)
	if _, err := agentMemSave(e.c, agentMemSaveReq{Scope: agentMemScopeUser, Name: "u", Description: "d", Type: "user", Body: "see [[nothing-here]]"}, laNow); err != nil {
		t.Fatal(err)
	}
	old := agentMemLintApplyMax
	agentMemLintApplyMax = 1
	t.Cleanup(func() { agentMemLintApplyMax = old })

	out, err := LintApplyAll(laNow, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Scopes != 2 || out.Marked != 1 || out.Deferred != 1 {
		t.Fatalf("first run = %+v, want 2 scopes, 1 marked, 1 deferred", out)
	}
	out, err = LintApplyAll(laNow.Add(time.Hour), nil)
	if err != nil || out.Marked != 1 || out.Deferred != 0 {
		t.Fatalf("second run = %+v, %v", out, err)
	}
}

// cont false stops the run before the next scope and writes nothing more.
func TestLintApplyAllStopsWhenToldTo(t *testing.T) {
	e := newLintApplyEnv(t, map[string]string{"docs/real.md": "x\n"})
	AgentMemoryEnabled = func() bool { return true }
	e.save("p", "uses docs/gone.md", 0)
	head := e.head()
	out, err := LintApplyAll(laNow, func() bool { return false })
	if err != nil || !out.Stopped || out.Scopes != 0 || e.head() != head {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}
