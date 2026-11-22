package opencode

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The approved custom endpoint is served to Managed sessions on the shared serve only: a Terminal
// launch, a private route server and a foreign config root are refused for that provider, and
// nothing else changes.
func TestCustomEndpointIsManagedOnly(t *testing.T) {
	useIsolationPolicy(t, approvedCustom())
	useCustom(t, selfHosted)
	v := readView()
	home := filepath.Dir(paths.OpencodeConfigDir())
	custom := session.Meta{Model: "mine/m"}
	other := session.Meta{Model: "anthropic/m"}

	if err := customManagedOnlyErrFrom(custom, true, v, ""); err != nil {
		t.Errorf("managed on the shared serve: %v", err)
	}
	if err := customManagedOnlyErrFrom(custom, true, v, home); err != nil {
		t.Errorf("managed with the pinned config root: %v", err)
	}
	if err := customManagedOnlyErrFrom(custom, false, v, ""); !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("terminal / private server: %v", err)
	}
	if err := customManagedOnlyErrFrom(custom, true, v, "/elsewhere"); !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("foreign config root: %v", err)
	}
	if err := customManagedOnlyErrFrom(other, false, v, "/elsewhere"); err != nil {
		t.Errorf("another provider is not affected: %v", err)
	}
	// Not approved: nothing is pinned, so nothing to refuse here.
	useIsolationPolicy(t, routepolicy.Policy{Revision: 6, Restricted: true, Allowed: []string{}})
	if err := customManagedOnlyErrFrom(custom, false, readView(), ""); err != nil {
		t.Errorf("unapproved endpoint: %v", err)
	}
}
