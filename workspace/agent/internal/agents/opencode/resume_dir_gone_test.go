package opencode

import (
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A Managed resume whose working folder is gone is refused before anything else happens,
// like every other Managed driver (#1039): no runtime is started, no handle registered, and
// the slot's id mapping is left as it was so the conversation is reachable again once the
// folder comes back. The ledger assertion guards the ordering only in part: if the check
// regressed, Ensure fails first here (the runtime is disabled), so the test catches it by the
// error, not by the overwrite the check exists to prevent.
func TestResumeRefusesMissingDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate the sid ledger
	// Never reach a real runtime if the check regresses: Ensure would otherwise adopt a daemon
	// another session runs on the default address, before any login gate.
	t.Setenv("AF_OPENCODE_SERVE_DISABLE", "1")
	dir := filepath.Join(t.TempDir(), "gone")
	m := session.Meta{Name: "dir-gone-" + t.Name(), Kind: session.KindOpencode, Dir: dir}
	slot := session.UUID(m.Dir, m.Name)
	sids.Write(slot, "existing-id")

	_, err := managedDriver{}.Resume(m)
	if err == nil || err.Error() != agents.DirGoneErr(dir).Error() {
		t.Fatalf("Resume error = %v, want %v", err, agents.DirGoneErr(dir))
	}
	if got := sids.Read(slot); got != "existing-id" {
		t.Errorf("sid mapping = %q, want it untouched (existing-id)", got)
	}
	if h := handleFor(m.Name); h != nil {
		t.Errorf("a handle was registered for a refused resume")
	}
}
