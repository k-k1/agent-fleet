package sessionx

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/ingresstest"
)

// A suggestion the Console waits for sends no byte until the model answers, so a deadline at or
// past the ingress idle timeout lets the Agent succeed while the browser gets a gateway error.
func TestSyncSuggestBudgetStaysUnderTheIngressIdleTimeout(t *testing.T) {
	idle := ingresstest.IdleTimeout(t)
	for name, d := range map[string]time.Duration{
		"TitleSuggestTimeout": TitleSuggestTimeout, // session title, branch name, chat title
		"ReplySuggestTimeout": ReplySuggestTimeout, // session and chat reply suggestions
	} {
		if d+ingresstest.HopMargin > idle {
			t.Errorf("%s = %s; with the %s hop margin it reaches the ingress idle timeout %s", name, d, ingresstest.HopMargin, idle)
		}
	}
}
