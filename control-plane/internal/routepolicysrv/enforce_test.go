package routepolicysrv

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
)

// A create's explicit route is judged against the member's policy: nothing while the policy
// restricts nothing, an allowed route passes, a forbidden one is refused, and an empty selection
// is left to the Agent. While the policy cannot be computed, a restricting ceiling refuses.
func TestRouteRefusal(t *testing.T) {
	ctx := context.Background()
	st, _, tn := routePolicyFixture(t, "classes=provided")
	if e := RouteRefusal(ctx, st, tn.ID, "bedrock-deployment"); e != nil {
		t.Errorf("an allowed route: %+v", e)
	}
	if e := RouteRefusal(ctx, st, tn.ID, " anthropic-api "); e == nil || e.Status != http.StatusForbidden || e.Code != CodePolicyForbidden {
		t.Errorf("a forbidden route: %+v", e)
	}
	if e := RouteRefusal(ctx, st, tn.ID, ""); e != nil {
		t.Errorf("an empty selection is the Agent's: %+v", e)
	}
	if e := RouteRefusal(ctx, layersFailStore{st}, tn.ID, "bedrock-deployment"); e == nil || e.Code != CodePolicyUnconfirmed {
		t.Errorf("unreadable policy under a restricting ceiling: %+v", e)
	}
	if !Restricted(ctx, st, tn.ID) || !Restricted(ctx, layersFailStore{st}, tn.ID) {
		t.Error("a restricting ceiling, read or unreadable, must count as restricting")
	}

	open, _, tn2 := routePolicyFixture(t, "")
	if e := RouteRefusal(ctx, open, tn2.ID, "anthropic-subscription"); e != nil {
		t.Errorf("an unrestricted tenant: %+v", e)
	}
	if e := RouteRefusal(ctx, layersFailStore{open}, tn2.ID, "anthropic-subscription"); e != nil {
		t.Errorf("unreadable policy with no ceiling is the Agent's to judge: %+v", e)
	}
	if Restricted(ctx, open, tn2.ID) {
		t.Error("an unrestricted tenant counted as restricting")
	}
}

// D12's Upgrade rule: only an Agent that reports the enforcement capability is handed a
// restricted member's create.
func TestAgentRefusal(t *testing.T) {
	if e := AgentRefusal(AgentEnforceCapability, true); e != nil {
		t.Errorf("an enforcing Agent: %+v", e)
	}
	if e := AgentRefusal("", true); e == nil || e.Code != CodeAgentUnenforcing {
		t.Errorf("an Agent from before enforcement: %+v", e)
	}
	if e := AgentRefusal("", false); e == nil || e.Code != CodePolicyUnconfirmed {
		t.Errorf("an Agent that does not answer: %+v", e)
	}
}

// The capability value is spelled once in each module; the two must agree, or every restricted
// create would be refused (or an old Agent trusted).
func TestAgentEnforceCapabilityMatchesTheAgent(t *testing.T) {
	b, err := os.ReadFile("../../../workspace/agent/internal/routepolicy/shared.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `const EnforceCapability = "`+AgentEnforceCapability+`"`) {
		t.Errorf("the Agent's EnforceCapability is not %q", AgentEnforceCapability)
	}
}
