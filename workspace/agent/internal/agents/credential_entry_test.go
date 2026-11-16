package agents

import (
	"net/http"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

func useEntryPolicy(t *testing.T) *routepolicy.Store {
	t.Helper()
	s := routepolicy.Open(filepath.Join(t.TempDir(), "rp.json"), "")
	old := routepolicy.Active
	routepolicy.Active = func() *routepolicy.Store { return s }
	t.Cleanup(func() { routepolicy.Active = old })
	return s
}

// Adding a credential is refused only when the policy forbids every route it opens; while the
// policy cannot be confirmed nothing new is added.
func TestCredentialEntryRefusal(t *testing.T) {
	s := useEntryPolicy(t)
	if ref := CredentialEntryRefusal(routes.RouteAnthropicSubscription); ref != nil {
		t.Errorf("nothing held: %+v", ref)
	}
	if _, err := s.Accept(routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteOpencodeGo}}); err != nil {
		t.Fatal(err)
	}
	if ref := CredentialEntryRefusal(routes.RouteOpencodeZen, routes.RouteOpencodeGo); ref != nil {
		t.Errorf("a key with one of its routes allowed: %+v", ref)
	}
	ref := CredentialEntryRefusal(routes.RouteAnthropicSubscription)
	if ref == nil || ref.Status != http.StatusForbidden || ref.Code != routes.CodePolicyForbidden {
		t.Errorf("a forbidden login: %+v", ref)
	}
	stale := useEntryPolicy(t)
	if _, err := stale.Seed(routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteOpencodeGo}}, time.Now().Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if ref := CredentialEntryRefusal(routes.RouteOpencodeGo); ref == nil || ref.Code != routes.CodePolicyUnconfirmed {
		t.Errorf("an unconfirmed policy: %+v", ref)
	}
}

// A tighter policy closes the running login flows of a route it forbids, killing their process,
// and leaves the flows of an allowed route alone.
func TestTighterPolicyClosesForbiddenLogins(t *testing.T) {
	forbidden := NewFlowStore(time.Hour).GuardLogins(routes.RouteAgyLogin)
	allowed := NewFlowStore(time.Hour).GuardLogins(routes.RouteOpenAIAPI)
	start := func(st *FlowStore) (*Flow, string) {
		f, err := StartFlow(exec.Command("sleep", "60"))
		if err != nil {
			t.Fatal(err)
		}
		return f, st.Put(f)
	}
	fForbidden, idForbidden := start(forbidden)
	fAllowed, idAllowed := start(allowed)
	t.Cleanup(func() { fAllowed.Close(); fForbidden.Close() })

	s := useEntryPolicy(t)
	if _, err := s.Accept(routepolicy.Policy{Revision: 3, Restricted: true, Allowed: []string{routes.RouteOpenAIAPI}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for forbidden.Get(idForbidden) != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if forbidden.Get(idForbidden) != nil {
		t.Fatal("the forbidden route's login flow is still running")
	}
	// The store drops the flow before Close reaps it; reap's Once orders this read after the Wait.
	_ = fForbidden.reap()
	if fForbidden.Cmd.ProcessState == nil {
		t.Error("the forbidden login's process was not reaped")
	}
	if allowed.Get(idAllowed) == nil {
		t.Error("an allowed route's login flow was closed")
	}
}
