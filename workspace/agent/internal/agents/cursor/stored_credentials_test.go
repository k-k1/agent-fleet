package cursor

import (
	"context"
	"errors"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

// A status probe that fails after the logout is unknown, never signed out: the removal is a
// failure while the login may still be there, and only a probe that answers "not
// authenticated" proves it gone.
func TestRemoveCredentialNeedsProvenAbsence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir()) // no cursor-agent: its logout fails too
	credbind.ResetForTest()
	old := freshStatus
	t.Cleanup(func() { freshStatus = old })
	binding := credbind.AccountOrGeneration(routes.RouteCursorLogin, "dev@example.invalid")
	signedIn := statusOut{IsAuthenticated: true}
	signedIn.UserInfo.Email = "dev@example.invalid"

	run := func(after func() (statusOut, bool)) error {
		calls := 0
		freshStatus = func() (statusOut, bool) {
			calls++
			if calls == 1 {
				return signedIn, true
			}
			return after()
		}
		return (agentImpl{}).RemoveCredential(context.Background(), "login", binding, func([]string, string) bool { return true })
	}
	if err := run(func() (statusOut, bool) { return statusOut{}, false }); !errors.Is(err, agents.ErrAbsenceUnknown) {
		t.Errorf("probe failed after the logout: %v, want ErrAbsenceUnknown", err)
	}
	if err := run(func() (statusOut, bool) { return signedIn, true }); !errors.Is(err, agents.ErrStillPresent) {
		t.Errorf("still authenticated after the logout: %v, want ErrStillPresent", err)
	}
	if err := run(func() (statusOut, bool) { return statusOut{}, true }); err != nil {
		t.Errorf("control, signed out: %v", err)
	}
}
