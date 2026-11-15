package imagegen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// useImagePolicy makes imagegen judge by a store holding p, and returns it to take a later one.
func useImagePolicy(t *testing.T, p routepolicy.Policy) *routepolicy.Store {
	t.Helper()
	s := routepolicy.Open(filepath.Join(t.TempDir(), "rp.json"), "")
	if _, err := s.Accept(p); err != nil {
		t.Fatal(err)
	}
	routepolicy.WaitListeners()
	old := routepolicy.Active
	routepolicy.Active = func() *routepolicy.Store { return s }
	t.Cleanup(func() {
		routepolicy.WaitListeners()
		routepolicy.Active = old
	})
	return s
}

func onEngineImage(t *testing.T) {
	t.Helper()
	old := providerLaunch
	providerLaunch = func(Provider) routes.Launch { return routes.Launch{Route: routes.RouteAFEngineImage} }
	t.Cleanup(func() { providerLaunch = old })
}

var engineImageOnly = routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteAFEngineImage}}

// A queued job running when the policy is tightened to forbid its route is stopped at once: the
// engine is asked to drop it as for a cancel, the job fails with the policy's reason, and nothing
// is recorded. A change that still allows the route leaves it running.
func TestARunningJobIsStoppedWhenThePolicyForbidsItsRoute(t *testing.T) {
	t.Setenv("AF_USAGE_DIR", t.TempDir())
	q := withJobQueue(t)
	p := newGateProvider(t)
	withStubProvider(t, p)
	onEngineImage(t)
	s := useImagePolicy(t, engineImageOnly)
	job := enqueue(t, q, JobSpec{Request: Request{Prompt: "a fox"}})
	<-p.begun
	if _, err := s.Accept(routepolicy.Policy{Revision: 3, Restricted: true, Allowed: []string{routes.RouteAFEngineImage, routes.RouteOpenAIAPI}}); err != nil {
		t.Fatal(err)
	}
	routepolicy.WaitListeners()
	time.Sleep(300 * time.Millisecond) // a cancel would have failed the job by now
	if st := stateOf(q, job.Jobs[0].ID); st != JobRunning {
		t.Fatalf("a policy that still allows the route changed the job to %v", st)
	}
	if _, err := s.Accept(routepolicy.Policy{Revision: 4, Restricted: true, Allowed: []string{routes.RouteOpenAIAPI}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the job to fail", func() bool { return stateOf(q, job.Jobs[0].ID) == JobFailed })
	if got := p.cancels(); !slices.Equal(got, []string{"engine-prompt-1"}) {
		t.Errorf("the engine was asked to cancel %v", got)
	}
	if rows := usagex.ReadRows(); len(rows) != 0 {
		t.Errorf("the stopped job left ledger rows: %+v", rows)
	}
}

// A generation running when the policy is tightened to forbid its route is stopped, and reported
// with the policy's code, with no ledger row.
func TestAGenerationInFlightIsStoppedWhenThePolicyForbidsItsRoute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_USAGE_DIR", filepath.Join(home, "usage"))
	p := newGateProvider(t)
	withStubProvider(t, p)
	onEngineImage(t)
	s := useImagePolicy(t, engineImageOnly)
	ch := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), Job{Session: "slot01", SID: "sid-1", Pref: p.ID(), Request: Request{Op: OpGenerate, Prompt: "a cat"}})
		ch <- err
	}()
	<-p.begun
	if _, err := s.Accept(routepolicy.Policy{Revision: 3, Restricted: true, Allowed: []string{routes.RouteOpenAIAPI}}); err != nil {
		t.Fatal(err)
	}
	var err error
	select {
	case err = <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("the generation was not stopped")
	}
	if !errors.Is(err, ErrRoutePolicy) || !errors.Is(err, routepolicy.ErrCancelled) {
		t.Fatalf("the stopped generation answered %v", err)
	}
	rec := httptest.NewRecorder()
	writeGenerateErr(rec, err)
	if rec.Code != http.StatusForbidden {
		t.Errorf("reported as %d %s", rec.Code, rec.Body.String())
	}
	if rows := usagex.ReadRows(); len(rows) != 0 {
		t.Errorf("the stopped generation left ledger rows: %+v", rows)
	}
}

// A run whose candidates failed for other reasons too is not reported as the policy's.
func TestPolicyOnlyNeedsEveryReasonToBeThePolicys(t *testing.T) {
	pol := attemptFailure{id: "a", err: policyCancelErr()}
	other := attemptFailure{id: "b", err: errors.New("out of quota")}
	if !policyOnly(errors.Join(pol, pol)) || policyOnly(errors.Join(pol, other)) || policyOnly(other) || !policyOnly(pol) {
		t.Error("policyOnly")
	}
}
