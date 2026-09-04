// The exemption table for map sites that are not converted to structs, plus the reverse
// checks that bound each exemption's lifetime.
//
// README §4: an exemption table ships with the reverse check that forces the exemption out
// once it is no longer needed, in the same commit. Without it, "exempt, so nobody looks"
// piles up and the check is a name only. Both ways an exemption can stop being needed are
// covered here, because covering only one leaves the other silently green.
//
// Every exemption states its own reason. An exemption with no reason is indistinguishable
// from a hole to the next reader.
package main

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/wiretest"
)

// wiremapExemption declares that a given map site is not converted to a struct.
type wiremapExemption struct {
	// Func is the golden's second column (<receiver>.<method>, or a function name).
	Func string
	// Why is the reason for the exemption. If you cannot write one, this is not an
	// exemption — it is a site nobody has looked at yet.
	Why string
	// NeedsFlag is how that reason must show up in the golden: dyn = the key set is not
	// statically determined, partial = the walk could not open it fully. This is the axis
	// the reverse check turns on: if the flag is gone, so is the reason.
	NeedsFlag string
}

// wiremapExemptions lists the CP-side sites that are deliberately left as maps.
//
// A map site absent from this table is merely not converted yet; that is not an exemption.
// An exemption is limited to what cannot be converted structurally.
func wiremapExemptions() []wiremapExemption {
	return []wiremapExemption{
		{
			Func: "workspaceAPI.stats",
			// workspaceStats in metrics.go merges the whole map that came from the
			// Agent into containerStats()'s map with a for k, v := range. The key set
			// is whatever the Agent answered, so it is not statically determined, and
			// converting to a struct would silently drop any field the Agent added.
			// The header comment of wire_golden_test.go names this path as one the
			// golden cannot capture.
			Why:       "Agent 応答の map を range で合流させるためキー集合が静的に確定しない",
			NeedsFlag: "dyn",
		},
		{
			Func: "adminAPI.memberStats",
			// Goes through the same workspaceStats, so the same reason applies.
			Why:       "workspaceAPI.stats と同じ workspaceStats を経由する",
			NeedsFlag: "dyn",
		},
		{
			Func: "sessionShareAPI.messages",
			// Relays the JSON received from the owner Workspace's Agent, narrowed by
			// an allowlist. The keys are decided by that response.
			Why:       "所有者 Agent の応答をそのまま中継するためキー集合が静的に確定しない",
			NeedsFlag: "dyn",
		},
	}
}

// exemptionStillNeeded decides whether an exemption is still needed.
//
// This must stay the only implementation of that decision: the shipped reverse check
// (TestWiremapExemptionsAreStillNeeded) and the control that checks the reverse check itself
// (TestWiremapExemptionReverseCheckActuallyFires) both call it. When the decision was
// duplicated, breaking the shipped copy left the control green (measured: turning the real
// `if !still {` into `if false {` still passed all four exemption tests). Duplicate the
// checker and the checked, and only the copy gets tested.
//
// Returns (still needed, why it was judged unnecessary).
func exemptionStillNeeded(ex wiremapExemption, byFunc map[string][]wireMapSite) (bool, string) {
	got, ok := byFunc[ex.Func]
	if !ok {
		return false, "the target site no longer exists (once converted or deleted, remove it from the exemption table too)"
	}
	for _, s := range got {
		switch ex.NeedsFlag {
		case "dyn":
			if s.DynKey {
				return true, ""
			}
		case "partial":
			if s.Partial {
				return true, ""
			}
		}
	}
	return false, ex.NeedsFlag + " flag is not set (either the key set became statically determined, or the walk got thinner)"
}

// wiremapSitesByFunc reshapes the scan result so it can be looked up by function name.
func wiremapSitesByFunc(t *testing.T) map[string][]wireMapSite {
	t.Helper()
	byFunc := map[string][]wireMapSite{}
	for _, s := range scanWireMapSites(t, ".") {
		byFunc[s.Func] = append(byFunc[s.Func], s)
	}
	return byFunc
}

// TestWiremapExemptionsAreStillNeeded is reverse check 1: the structural direction.
//
// An exemption's reason is that the key set is not statically determined, and the evidence
// for it is the golden's `dyn` / `partial` flag. If the flag is gone, the reason is gone too,
// which means either
//
//	(a) the implementation changed and the keys are now determined (so it can be
//	    converted and the exemption should be dropped), or
//	(b) the walk got thinner and lost the flag (so the tool is broken).
//
// Neither may pass silently.
func TestWiremapExemptionsAreStillNeeded(t *testing.T) {
	reportStaleExemptions(t, wiremapExemptions(), wiremapSitesByFunc(t))
}

// reportStaleExemptions is the body of the shipped reverse check: decision plus reporting.
//
// The body lives here rather than in the test function so the control can drive it end to
// end. Sharing only the decision and writing the reporting in the test function would leave
// the control green when the reporting side (t.Errorf) is deleted — the same hole in another
// shape. t is an interface so the control can pass a wiretest.Recorder and see whether a
// report was actually made.
func reportStaleExemptions(t wiretest.TB, exs []wiremapExemption, byFunc map[string][]wireMapSite) {
	t.Helper()
	for _, ex := range exs {
		if ok, why := exemptionStillNeeded(ex, byFunc); !ok {
			t.Errorf("exemption %q is no longer needed (why: %s).\n"+
				"  the ground for the exemption was %q.\n"+
				"  if the implementation changed and the key set is now determined, **drop the exemption and convert the site**.\n"+
				"  if the walk got thinner and lost the flag, **fix the tool**. Either way this does not stay green.",
				ex.Func, why, ex.Why)
		}
	}
}

// wiremapDeferred is the other kind of exemption: a site that could be converted
// structurally but is deliberately left alone for now.
//
// Its lifetime ends differently from a structural exemption, so it needs its own reverse
// check: the one above asks whether the reason is gone, this one asks whether the site is
// still unconverted. Without a machine holding it, a conversion candidate drops out of the
// plan and nobody notices.
var wiremapDeferred = []wiremapExemption{
	{
		Func: "registerAuthRoutes",
		// DeploymentVersion at routes.go:141. A J=1.0 conversion candidate (the Console
		// has a hand-written type for it), but it is an inline map inside
		// registerAuthRoutes with a high degree of sharing, and the coordinating owner
		// left it out of the CONTRACT-MAP in owners.tsv.
		Why: "control-plane/routes.go は CONTRACT-MAP の所有外（司令塔判断・共有度が高い）",
	},
}

// TestWiremapDeferredAreStillMaps is reverse check 2: the deferred direction.
//
// If a deferred site is no longer a map, somebody converted or deleted it and the reason for
// deferring is gone, so it has to leave the table. Without this, "deferred because it is not
// ours" fossilizes and nobody notices that a conversion candidate left the plan.
func TestWiremapDeferredAreStillMaps(t *testing.T) {
	sites := scanWireMapSites(t, ".")
	byFunc := map[string]bool{}
	for _, s := range sites {
		byFunc[s.Func] = true
	}
	for _, d := range wiremapDeferred {
		if !byFunc[d.Func] {
			t.Errorf("deferred site %q is no longer a map site (%s).\n"+
				"  once converted or deleted, **take it out of the deferred table**. "+
				"Left there, a conversion candidate drops out of the plan in silence.", d.Func, d.Why)
		}
		if strings.TrimSpace(d.Why) == "" {
			t.Errorf("deferred site %q has no reason", d.Func)
		}
	}
}

// TestWiremapExemptionsHaveReasons keeps anyone from adding an exemption with no reason.
// An exemption whose reason cannot be written down means "not investigated", not "exempt".
func TestWiremapExemptionsHaveReasons(t *testing.T) {
	seen := map[string]bool{}
	for _, ex := range wiremapExemptions() {
		if strings.TrimSpace(ex.Why) == "" {
			t.Errorf("exemption %q has no reason", ex.Func)
		}
		if ex.NeedsFlag != "dyn" && ex.NeedsFlag != "partial" {
			t.Errorf("exemption %q has a NeedsFlag that is neither dyn nor partial: %q", ex.Func, ex.NeedsFlag)
		}
		if seen[ex.Func] {
			t.Errorf("exemption %q is duplicated", ex.Func)
		}
		seen[ex.Func] = true
	}
	if len(wiremapExemptions()) == 0 {
		t.Fatal("zero exemptions (with an empty table the reverse check is looking at nothing)")
	}
}

// TestWiremapExemptionReverseCheckActuallyFires checks the reverse check itself.
//
// README §4: a green result counts only once the check is shown to be picking up its target.
// The reverse check goes red only when an exemption stops being needed, so in normal times it
// is always green — indistinguishable from green because it is broken. So a synthetic
// exemption whose reason nothing backs is fed to it, and it must actually go red.
func TestWiremapExemptionReverseCheckActuallyFires(t *testing.T) {
	byFunc := wiremapSitesByFunc(t)
	// Drive the shipped reverse check end to end. Rewriting a copy of the decision here
	// would leave this control green while the real one is broken (measured). Passing a
	// wiretest.Recorder makes it go red whether the decision or the reporting is broken.
	check := func(ex wiremapExemption) bool { // true = actually reported, i.e. should go red
		rec := &wiretest.Recorder{}
		reportStaleExemptions(rec, []wiremapExemption{ex}, byFunc)
		return len(rec.Errs()) > 0
	}

	t.Run("an exemption whose site is gone goes red", func(t *testing.T) {
		if !check(wiremapExemption{Func: "nonexistent.handler", Why: "synthetic", NeedsFlag: "dyn"}) {
			t.Error("let an exemption with no target site through = it cannot catch 'converted, yet the exemption is still there'")
		}
	})

	t.Run("an exemption the flags do not back goes red", func(t *testing.T) {
		// Pick one real site that carries no dyn flag and synthesize an exemption that
		// claims dyn as its reason.
		var plain string
		for fn, ss := range byFunc {
			clean := true
			for _, s := range ss {
				if s.DynKey || s.Partial {
					clean = false
				}
			}
			if clean {
				plain = fn
				break
			}
		}
		if plain == "" {
			t.Skip("not a single site without dyn/partial (no control can be built)")
		}
		if !check(wiremapExemption{Func: plain, Why: "synthetic", NeedsFlag: "dyn"}) {
			t.Errorf("%s carries no dyn flag, yet an exemption claiming dyn as its ground was let through", plain)
		}
	})

	t.Run("a real exemption passes", func(t *testing.T) {
		// Positive control as well as negative: a check that reddens everything protects
		// nothing.
		for _, ex := range wiremapExemptions() {
			if check(ex) {
				t.Errorf("reddened the real exemption %q (the check is too strict)", ex.Func)
			}
		}
	})
}
