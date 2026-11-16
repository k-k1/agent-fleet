package kiro

import (
	"context"
	"errors"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

func always([]string, string) bool { return true }

// whoami exits non-zero both signed out and broken, so after a logout its failure proves
// nothing: with the token row still in kiro's database the outcome is unknown, a failure; only
// the row's absence proves the login gone.
func TestRemoveCredentialNeedsProvenAbsence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir()) // no kiro-cli: its logout fails too
	credbind.ResetForTest()
	oldWho, oldRow := freshWhoami, tokenRowPresent
	t.Cleanup(func() { freshWhoami, tokenRowPresent = oldWho, oldRow })
	binding := credbind.AccountOrGeneration(routes.RouteKiroLogin, "dev@example.invalid")

	run := func(rowAfter bool) error {
		calls := 0
		freshWhoami = func() (statusResult, bool) {
			calls++
			if calls == 1 {
				return statusResult{loggedIn: true, email: "dev@example.invalid"}, true
			}
			return statusResult{}, false
		}
		tokenRowPresent = func() (bool, error) { return rowAfter, nil }
		return (agentImpl{}).RemoveCredential(context.Background(), "login", binding, always)
	}
	if err := run(true); !errors.Is(err, agents.ErrAbsenceUnknown) {
		t.Errorf("logout and probe failed, token row kept: %v, want ErrAbsenceUnknown", err)
	}
	if err := run(false); err != nil {
		t.Errorf("control, token row gone: %v", err)
	}
	tokenRowPresent = func() (bool, error) { return false, errors.New("database is locked") }
	freshWhoami = func() (statusResult, bool) { return statusResult{}, false }
	if _, _, st := currentLogin(); st != agents.LoginUnknown {
		t.Errorf("unreadable database: %v, want unknown", st)
	}
}
