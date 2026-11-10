package sessionx

// Equivalence proof for agents.ProgressSignaler: the three progress predicates against verbatim
// copies of the kind switches they replaced, over every registered kind x driver, plus an
// unknown and an empty kind (both read as claude).

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func oldProgressTrusted(m session.Meta) bool {
	if m.DriverKind() == session.DriverManaged {
		return managedProgressMeasured(m)
	}
	switch NormalizeKind(m.Kind) {
	case session.KindClaude, session.KindAgy:
		return true
	}
	return oldProgressPaneOnly(m)
}

func oldProgressPaneOnly(m session.Meta) bool {
	if m.DriverKind() == session.DriverManaged {
		return false
	}
	switch NormalizeKind(m.Kind) {
	case session.KindCodex, session.KindCursor, session.KindCopilot, session.KindKiro, session.KindOpencode:
		return true
	}
	return false
}

func oldStatusMarksBoundary(m session.Meta) bool {
	if m.DriverKind() == session.DriverManaged {
		return true
	}
	switch NormalizeKind(m.Kind) {
	case session.KindAgy, session.KindCopilot, session.KindCursor, session.KindKiro:
		return false
	}
	return true
}

func TestProgressTraitsMatchOldSwitch(t *testing.T) {
	kinds := append(RegisteredKinds(), "", "no-such-kind")
	checked := 0
	for _, k := range kinds {
		for _, drv := range []string{"", session.DriverTUI, session.DriverManaged} {
			m := session.Meta{Name: "progtraits-" + k, Dir: t.TempDir(), Kind: k, Driver: drv}
			if got, want := progressTrusted(m), oldProgressTrusted(m); got != want {
				t.Errorf("progressTrusted(kind=%q driver=%q) = %v, old switch %v", k, drv, got, want)
			}
			if got, want := progressPaneOnly(m), oldProgressPaneOnly(m); got != want {
				t.Errorf("progressPaneOnly(kind=%q driver=%q) = %v, old switch %v", k, drv, got, want)
			}
			if got, want := statusMarksBoundary(m), oldStatusMarksBoundary(m); got != want {
				t.Errorf("statusMarksBoundary(kind=%q driver=%q) = %v, old switch %v", k, drv, got, want)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no kind was checked")
	}
}

// TestProgressTraitsDeclared pins that the sweep above is not vacuous: the answers it compares
// are not all the same.
func TestProgressTraitsDeclared(t *testing.T) {
	var trusted, paneOnly, hookless int
	for _, k := range RegisteredKinds() {
		m := session.Meta{Name: "progtraits-d", Dir: t.TempDir(), Kind: k, Driver: session.DriverTUI}
		if progressTrusted(m) {
			trusted++
		}
		if progressPaneOnly(m) {
			paneOnly++
		}
		if !statusMarksBoundary(m) {
			hookless++
		}
	}
	if trusted != 7 || paneOnly != 5 || hookless != 4 {
		t.Errorf("trusted/paneOnly/hookless = %d/%d/%d, want 7/5/4", trusted, paneOnly, hookless)
	}
}
