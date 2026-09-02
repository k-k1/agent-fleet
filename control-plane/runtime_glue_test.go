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
	"net/http"
	"net/http/httptest"
	"testing"

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
type fakePoolFactory struct{ st ec2PoolStatus }

func (fakePoolFactory) New(runtime.Workspace, string, []string) Runtime { return nil }

func (f fakePoolFactory) PoolStatus(context.Context) (ec2PoolStatus, error) { return f.st, nil }

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
	f := fakePoolFactory{st: ec2PoolStatus{
		Runtime: "ecs-ec2",
		Goldens: []runtime.EC2GoldenView{{Arch: ec2ArchX86, Phase: ec2BakePhaseIdle}},
	}}
	m := &manager{rtFactory: f, autoBakeGolden: false}

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
