// runtime_glue_test.go — the CP's half of the Runtime seam.
//
// Three tests used to live inside the runtime family and could not travel with it: each
// one exercises code that stayed here (ensureWorkspaceReady, manager.poolStatus) against
// an adapter. They are rewritten against a stand-in factory rather than the AWS harness,
// which is strictly better for what they are checking — the CP's contribution to the
// answer, not the adapter's.
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// --- ensureWorkspaceReady (was runtime_health_test.go) -------------------------------

func glueUnreadyAgent(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func glueReadyAgent(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestEnsureWorkspaceReadyAnswersStartingInsteadOfFailing — Agent を要する API
// （セッション作成・fork・再開・持ち越し回答・SSM）が起動途中に当たったときの答え。
// 500 + 生の "agent did not become healthy within 15s" ではなく、409 workspace_starting
// （Console が「起動中です」と訳せて、再試行が意味を持つ形）にする。
func TestEnsureWorkspaceReadyAnswersStartingInsteadOfFailing(t *testing.T) {
	t.Setenv("AF_AGENT_READY_WAIT_SEC", "1") // 55 秒の既定を待たない
	_, ws, mgr := reaperLifecycleFixture(t)
	api := newWorkspaceAPI(mgr, true)
	ctx := context.Background()
	mv := MembershipView{MembershipID: ws.MembershipID, TenantID: ws.TenantID}

	booting := glueUnreadyAgent(t)
	res := &resolved{rt: stubRuntime{endpoint: booting.URL, state: "starting"}, ws: ws, mv: mv}
	aerr := api.ensureWorkspaceReady(ctx, res)
	if aerr == nil {
		t.Fatal("still-booting agent: want a retryable error, got success")
	}
	if aerr.status != http.StatusConflict || aerr.code != "workspace_starting" {
		t.Fatalf("got %d/%s (%s), want 409/workspace_starting", aerr.status, aerr.code, aerr.message)
	}

	// 上がっていれば素通し。既に running なら probe すら足さない。
	up := glueReadyAgent(t)
	resUp := &resolved{rt: stubRuntime{endpoint: up.URL, state: "running"}, ws: ws, mv: mv}
	if aerr := api.ensureWorkspaceReady(ctx, resUp); aerr != nil {
		t.Fatalf("running workspace: %+v", aerr)
	}
	// 起動途中でも、待っているあいだに上がれば成功として通す。
	resLate := &resolved{rt: stubRuntime{endpoint: up.URL, state: "starting"}, ws: ws, mv: mv}
	if aerr := api.ensureWorkspaceReady(ctx, resLate); aerr != nil {
		t.Fatalf("agent that came up during the wait: %+v", aerr)
	}
}

// --- manager.poolStatus (was runtime_ecs_ec2_test.go) --------------------------------

// noPoolFactory is a runtime that has no pool — every profile but ecs-ec2.
type noPoolFactory struct{}

func (noPoolFactory) New(runtime.Workspace, string, []string) Runtime { return nil }

// fakePoolFactory answers PoolStatus with a canned reply, standing in for the ecs-ec2
// adapter. The adapter's own half of the answer is tested in internal/runtime; what is
// under test here is the two things it CANNOT know — whether the baker is switched on,
// and how the tenants' quotas add up against the pool cap — which the CP fills in
// afterwards (manager.poolStatus).
//
// It reports MaxSlots for the second of those: without it the CP's quota comparison
// returns ok=false and the Budget branch never runs, which would leave the sentence
// above describing something the test does not do.
type fakePoolFactory struct {
	st       ec2PoolStatus
	maxSlots int
}

func (fakePoolFactory) New(runtime.Workspace, string, []string) Runtime { return nil }

func (f fakePoolFactory) MaxSlots() int { return f.maxSlots }

// The reply is rebuilt per call, like the adapter's: manager.poolStatus rewrites
// Goldens[i].Phase in place, so a fake handing out the same backing array twice would
// let the first call's rewrite leak into the second.
func (f fakePoolFactory) PoolStatus(context.Context) (ec2PoolStatus, error) {
	st := f.st
	st.Goldens = append([]runtime.EC2GoldenView(nil), f.st.Goldens...)
	return st, nil
}

// poolStatusFixture wires the fake to a REAL store: manager.poolStatus reads the tenant
// table for the budget, and a nil store is a state production never has — the call would
// panic if the comparison ever started running, which is exactly the branch under test.
func poolStatusFixture(t *testing.T, maxSlots int, quotas map[string]int) *manager {
	t.Helper()
	ctx := context.Background()
	st := p3Store(t)
	m := p3Manager(t, st)
	m.rtFactory = fakePoolFactory{
		st: ec2PoolStatus{
			Runtime: "ecs-ec2",
			Goldens: []runtime.EC2GoldenView{{Arch: ec2ArchX86, Phase: ec2BakePhaseIdle}},
		},
		maxSlots: maxSlots,
	}
	for slug, n := range quotas {
		tn, err := st.CreateTenant(ctx, slug, slug)
		if err != nil {
			t.Fatalf("tenant %s: %v", slug, err)
		}
		if err := st.SetTenantLimits(ctx, tn.ID, fmt.Sprintf(`{"max_workspaces":%d}`, n)); err != nil {
			t.Fatalf("limits %s: %v", slug, err)
		}
	}
	return m
}

// Nothing else in the product has a pool, and an empty table on a Fargate deployment
// reads as "my slots all vanished".
func TestPoolStatusIsAbsentOnOtherRuntimes(t *testing.T) {
	m := &manager{rtFactory: noPoolFactory{}}
	if _, ok, err := m.poolStatus(context.Background()); ok || err != nil {
		t.Errorf("poolStatus on a runtime with no pool = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

// AF_ECS_EC2_GOLDEN_AUTOBAKE=0 is the one thing about the golden that is not visible in
// AWS. Left unsaid, "no golden, nothing under way" reads as "wait a few minutes".
func TestPoolStatusSaysWhenAutoBakeIsOff(t *testing.T) {
	ctx := context.Background()
	m := poolStatusFixture(t, 10, map[string]int{"acme": 2})
	m.autoBakeGolden = false

	st, ok, err := m.poolStatus(ctx)
	if !ok || err != nil {
		t.Fatalf("poolStatus = (ok=%v, err=%v)", ok, err)
	}
	if st.AutoBake || st.Goldens[0].Phase != ec2BakePhaseOff {
		t.Errorf("auto_bake=%v phase=%q, want the screen to say the baker is switched off",
			st.AutoBake, st.Goldens[0].Phase)
	}

	m.autoBakeGolden = true
	if st, _, err = m.poolStatus(ctx); err != nil {
		t.Fatalf("poolStatus: %v", err)
	}
	if !st.AutoBake || st.Goldens[0].Phase != ec2BakePhaseIdle {
		t.Errorf("auto_bake=%v phase=%q, want a bake to be expected", st.AutoBake, st.Goldens[0].Phase)
	}
}

// The budget is attached ONLY when it says something (limits.go): a deployment whose
// tenant quotas fit its pool is not news, and the screen already shows both numbers it
// is made of. Both directions are checked, because "always absent" would pass a test
// that only looked at the happy one — and an over-subscribed pool that says nothing is
// the failure this comparison exists to prevent (ADR 0045 決定 25).
func TestPoolStatusCarriesTheTenantBudgetOnlyWhenItDoesNotFit(t *testing.T) {
	ctx := context.Background()

	// 10 slots less the 2 a bake reserves = 8 to share out; 20 asked for.
	over := poolStatusFixture(t, 10, map[string]int{"acme": 20})
	st, _, err := over.poolStatus(ctx)
	if err != nil {
		t.Fatalf("poolStatus: %v", err)
	}
	if st.Budget == nil {
		t.Fatal("an over-subscribed pool reported no budget — the warning has nowhere to appear")
	}
	if !st.Budget.Over || st.Budget.Capacity != 8 || st.Budget.Allocated != 20 {
		t.Errorf("budget = %+v, want 20 allocated against a capacity of 8, over", *st.Budget)
	}

	fits := poolStatusFixture(t, 10, map[string]int{"acme": 4, "beta": 4})
	st, _, err = fits.poolStatus(ctx)
	if err != nil {
		t.Fatalf("poolStatus: %v", err)
	}
	if st.Budget != nil {
		t.Errorf("budget = %+v on a pool that fits, want it left off", *st.Budget)
	}
}

// --- the AWS credential seam --------------------------------------------------------

// awsConfigFor has to reach runtime.AWSConfigFor at CALL time, not bind to it once.
//
// It is the only name alias_runtime.go borrows whose far side is a variable, and it is a
// variable precisely so the live AWS harness can point a whole run at a test account
// (docs/log/64 §64.23). Bound once with `var awsConfigFor = runtime.AWSConfigFor`, the
// swap would reach the four adapters — they read the variable from inside its own package
// on every call — and NOT the CP's own two readers, Cost Explorer and the store's Secrets
// Manager, which would keep the credentials this file captured at init. Nothing fails in
// that state; half the process just talks to the wrong account. The failure below names
// the same direction.
func TestAWSConfigForDispatchesPerCall(t *testing.T) {
	prev := runtime.AWSConfigFor
	t.Cleanup(func() { runtime.AWSConfigFor = prev })

	var gotRegion string
	runtime.AWSConfigFor = func(_ context.Context, region string) (aws.Config, error) {
		gotRegion = region
		return aws.Config{Region: "swapped"}, nil
	}

	ac, err := awsConfigFor(context.Background(), "ap-northeast-1")
	if err != nil {
		t.Fatalf("awsConfigFor: %v", err)
	}
	if gotRegion != "ap-northeast-1" || ac.Region != "swapped" {
		t.Fatalf("region in=%q out=%q — the swap did not reach the CP side; awsConfigFor has "+
			"gone back to being a copy taken at init", gotRegion, ac.Region)
	}
}
