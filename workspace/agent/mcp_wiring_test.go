package main

// One end-to-end check that mcp_wiring.go's wiring is actually live.
//
// `mcpx.Configure` only catches wiring that is MISSING (nil / zero value); it cannot catch
// wiring that is WRONG. Three forms of that are reachable in practice:
//
//   - `ApprovalGate` turned into "always approve" → operator approval disappears entirely
//   - `WriteEnabled` pinned to `return false`     → the write tools never appear
//   - `ConvID` pinned to `return ""`              → completion reports lose their destination
//
// Each is a one-line change to the wiring, and that line is written for the good reason of
// "a closure, to avoid the copy trap", which makes it an easy target for a future cleanup.
//
// Two shapes of check:
//
//   - plain functions are compared by function-pointer identity (a different function or a
//     closure fails)
//   - the four pairs held in closures (values that cannot be copied) are checked round-trip
//     (main's variable → mcpx's getter, mcpx's setter → main's variable)
//
// The set of checks is then reconciled against Deps' set of fields, so adding a field
// without adding a check fails here.

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"reflect"
	"runtime"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

func TestMCPWiringIsLive(t *testing.T) {
	w := mcpx.Wired()

	checks := map[string]func(t *testing.T){
		// --- passed by value (must equal the real constant) ---
		"SessionTitleMaxRunes": func(t *testing.T) {
			if w.SessionTitleMaxRunes != sessionx.SessionTitleMaxRunes {
				t.Fatalf("session title limit = %d, want %d", w.SessionTitleMaxRunes, sessionx.SessionTitleMaxRunes)
			}
		},
		"ReportKindSelfReport": func(t *testing.T) {
			if w.ReportKindSelfReport != chatx.ReportKindSelfReport {
				t.Fatalf("report kind = %q, want %q", w.ReportKindSelfReport, chatx.ReportKindSelfReport)
			}
		},
		"PeerIntentNames": func(t *testing.T) {
			if !reflect.DeepEqual(w.PeerIntentNames, sessionx.PeerIntentNames) {
				t.Fatalf("intent list = %v, want %v", w.PeerIntentNames, sessionx.PeerIntentNames)
			}
		},

		// --- functions (must be the real one) ---
		"CleanTitle":                 func(t *testing.T) { sameFunc(t, w.CleanTitle, sessionx.CleanTitle) },
		"PeerReachableSessions":      func(t *testing.T) { sameFunc(t, w.PeerReachableSessions, sessionx.PeerReachableSessions) },
		"ApprovalGate":               func(t *testing.T) { sameFunc(t, w.ApprovalGate, sessionx.BridgeApprovalGate) },
		"ApprovalLabel":              func(t *testing.T) { sameFunc(t, w.ApprovalLabel, sessionx.ApprovalLabel) },
		"ShellCreateTarget":          func(t *testing.T) { sameFunc(t, w.ShellCreateTarget, sessionx.ShellCreateTarget) },
		"ShellSendTarget":            func(t *testing.T) { sameFunc(t, w.ShellSendTarget, sessionx.ShellSendTarget) },
		"SessionIsShell":             func(t *testing.T) { sameFunc(t, w.SessionIsShell, sessionx.SessionIsShell) },
		"ReadUIPrefs":                func(t *testing.T) { sameFunc(t, w.ReadUIPrefs, uiprefs.Read) },
		"EnsureClaudeSettingsWiring": func(t *testing.T) { sameFunc(t, w.EnsureClaudeSettingsWiring, sessionx.EnsureClaudeSettingsWiring) },
		"RepoAnyDirFromPath":         func(t *testing.T) { sameFunc(t, w.RepoAnyDirFromPath, gitx.RepoAnyDirFromPath) },
		"ReadBuildPins":              func(t *testing.T) { sameFunc(t, w.ReadBuildPins, readBuildPins) },
		"AgentFleetShareDir":         func(t *testing.T) { sameFunc(t, w.AgentFleetShareDir, agentFleetShareDir) },
		"InstallGrafanaMCP":          func(t *testing.T) { sameFunc(t, w.InstallGrafanaMCP, installGrafanaMCP) },
		"WriteSSMConfig":             func(t *testing.T) { sameFunc(t, w.WriteSSMConfig, sessionx.WriteSSMConfig) },

		// --- the four pairs that cannot be copied (checked round-trip) ---
		"WriteEnabled":      func(t *testing.T) { roundTripBool(t, &mcpWriteEnabled, w.WriteEnabled, w.SetWriteEnabled) },
		"SetWriteEnabled":   func(t *testing.T) { roundTripBool(t, &mcpWriteEnabled, w.WriteEnabled, w.SetWriteEnabled) },
		"SelfReportOnly":    func(t *testing.T) { roundTripBool(t, &mcpSelfReportOnly, w.SelfReportOnly, w.SetSelfReportOnly) },
		"SetSelfReportOnly": func(t *testing.T) { roundTripBool(t, &mcpSelfReportOnly, w.SelfReportOnly, w.SetSelfReportOnly) },
		"SessionChromiumEnabled": func(t *testing.T) {
			roundTripBool(t, &mcpSessionChromiumEnabled, w.SessionChromiumEnabled, w.SetSessionChromiumEnabled)
		},
		"SetSessionChromiumEnabled": func(t *testing.T) {
			roundTripBool(t, &mcpSessionChromiumEnabled, w.SessionChromiumEnabled, w.SetSessionChromiumEnabled)
		},
		"ConvID":    func(t *testing.T) { roundTripConvID(t) },
		"SetConvID": func(t *testing.T) { roundTripConvID(t) },
	}

	// Reconcile the set of checks with Deps' set of fields. A new field always fails here,
	// so "wiring added but no check added" cannot happen.
	typ := reflect.TypeOf(w)
	seen := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		seen[name] = true
		if _, ok := checks[name]; !ok {
			t.Errorf("Deps.%s wiring is not checked (add a check whenever you add a field)", name)
		}
	}
	for name := range checks {
		if !seen[name] {
			t.Errorf("Deps has no %s (the check alone is stale)", name)
		}
	}
	for name, run := range checks {
		t.Run(name, run)
	}
}

// sameFunc checks that the function itself is what got wired. A closure or a different
// function has a different code pointer and fails.
func sameFunc(t *testing.T, got, want any) {
	t.Helper()
	g, w := reflect.ValueOf(got).Pointer(), reflect.ValueOf(want).Pointer()
	if g != w {
		t.Fatalf("wired to the wrong function: got %s, want %s", funcName(g), funcName(w))
	}
}

func funcName(pc uintptr) string {
	if f := runtime.FuncForPC(pc); f != nil {
		return f.Name()
	}
	return "?"
}

// roundTripBool checks that main's variable and mcpx's read/write refer to the same object.
// One direction alone (only the getter, or only the setter) cannot catch wiring that returns
// a fixed value.
func roundTripBool(t *testing.T, home *bool, get func() bool, set func(bool)) {
	t.Helper()
	old := *home
	t.Cleanup(func() { *home = old })

	*home = true
	if !get() {
		t.Fatal("a value set on main's side is not visible from mcpx (the getter returns a fixed value)")
	}
	*home = false
	if get() {
		t.Fatal("a value cleared on main's side is not visible from mcpx (the getter returns a fixed value)")
	}
	set(true)
	if !*home {
		t.Fatal("mcpx's setter does not write main's variable (it got a copy)")
	}
}

func roundTripConvID(t *testing.T) {
	t.Helper()
	w := mcpx.Wired()
	old := mcpConvID
	t.Cleanup(func() { mcpConvID = old })

	mcpConvID = "conv-wiring-probe"
	if got := w.ConvID(); got != "conv-wiring-probe" {
		t.Fatalf("conversation id as seen from mcpx = %q (main's assignment did not reach it)", got)
	}
	w.SetConvID("conv-from-mcpx")
	if mcpConvID != "conv-from-mcpx" {
		t.Fatalf("conversation id on main's side = %q (mcpx's setter did not reach it)", mcpConvID)
	}
}
