package main

// One end-to-end check that session_wiring.go's wiring is live.
//
// `sessionx.Configure` catches only unwired fields (nil / zero value), never wrong wiring,
// and the session family's Deps is dense with same-typed fields:
//
//   - 12 of `string` (the error codes)
//   - 2 of `func() string` (BrowseRoot / ToolchainShellPrefix)
//
// Swapping two fields of the same type sets off neither the type checker nor `Configure`'s
// reflect coverage check. Three independent instances have been hit (#312→#319, #333, #332),
// and every one of them was measured with the whole suite green. Concretely:
//
//   - swap `ErrCodePasteTooLarge` with `ErrCodePasteUnsupportedKind`
//     → pasting an image reports "this kind cannot be pasted" instead of "too large". Both
//     are real codes, so the Console's i18n resolves them and the screen lies while looking
//     entirely natural.
//   - swap `BrowseRoot` with `ToolchainShellPrefix`
//     → attachments are saved under the shell prefix string, and pasted images go missing.
//   - substitute another `func() int64` for `MaxUploadBytes`
//     → the limit changes silently (0 rejects everything; a huge value removes the guard).
//
// Two shapes of check:
//
//   - functions are compared by function-pointer identity, so another function or a closure
//     fails
//   - values must equal the real constant
//
// The set of checks is then reconciled against Deps' set of fields, so adding a field
// without adding a check fails here.

import (
	"reflect"
	"runtime"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

func TestSessionWiringIsLive(t *testing.T) {
	w := sessionx.Wired()

	checks := map[string]func(t *testing.T){
		"EnvOr":            func(t *testing.T) { sameSessionFunc(t, w.EnvOr, envOr) },
		"FirstNonEmpty":    func(t *testing.T) { sameSessionFunc(t, w.FirstNonEmpty, firstNonEmpty) },
		"SplitFrontmatter": func(t *testing.T) { sameSessionFunc(t, w.SplitFrontmatter, splitFrontmatter) },

		"BrowseRoot":     func(t *testing.T) { sameSessionFunc(t, w.BrowseRoot, browseRoot) },
		"MaxUploadBytes": func(t *testing.T) { sameSessionFunc(t, w.MaxUploadBytes, maxUploadBytes) },

		"IsSvnRepo":       func(t *testing.T) { sameSessionFunc(t, w.IsSvnRepo, isSvnRepo) },
		"RepoJobsRunning": func(t *testing.T) { sameSessionFunc(t, w.RepoJobsRunning, repoJobsRunning) },

		"FinalizeSessionUsage":  func(t *testing.T) { sameSessionFunc(t, w.FinalizeSessionUsage, finalizeSessionUsage) },
		"MaybeFoldSessionUsage": func(t *testing.T) { sameSessionFunc(t, w.MaybeFoldSessionUsage, maybeFoldSessionUsage) },

		"RemoveTerminalHistory": func(t *testing.T) { sameSessionFunc(t, w.RemoveTerminalHistory, removeTerminalHistory) },
		"ToolchainShellPrefix":  func(t *testing.T) { sameSessionFunc(t, w.ToolchainShellPrefix, toolchainShellPrefix) },

		"RunOperatorTurn": func(t *testing.T) { sameSessionFunc(t, w.RunOperatorTurn, runOperatorTurn) },

		// MCPConvID alone is not the real function: it is a closure reading a var, so
		// pointer identity means nothing. Check the behaviour instead - that it reads the
		// current value. Going back to wiring by copied value (a fixed
		// `MCPConvID: func() string { return "" }`, or holding a string in Deps) fails here.
		"MCPConvID": func(t *testing.T) {
			old := mcpConvID
			t.Cleanup(func() { mcpConvID = old })
			mcpConvID = "conv-wiring-probe"
			if got := w.MCPConvID(); got != "conv-wiring-probe" {
				t.Fatalf("MCPConvID() = %q, want %q (if the wiring copies the value, the "+
					"approval prompt freezes on the conversation ID present at wiring time)", got, "conv-wiring-probe")
			}
		},

		// An error code must be spelled exactly as in errcodes.go; otherwise the Console's
		// i18n cannot resolve it and the raw code reaches the screen. All 12 share one type,
		// so these 12 lines are the only thing standing between them and a swap.
		"ErrCodeAgentNotConnected": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodeAgentNotConnected, errCodeAgentNotConnected)
		},
		"ErrCodeChatConversationNotFnd": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodeChatConversationNotFnd, errCodeChatConversationNotFnd)
		},
		"ErrCodeForkAtUnsupported": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodeForkAtUnsupported, errCodeForkAtUnsupported)
		},
		"ErrCodeForkBadAnchor": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodeForkBadAnchor, errCodeForkBadAnchor)
		},
		"ErrCodeForkMissingDir": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodeForkMissingDir, errCodeForkMissingDir)
		},
		"ErrCodeForkUnsupportedKind": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodeForkUnsupportedKind, errCodeForkUnsupportedKind)
		},
		"ErrCodeLocked": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodeLocked, errCodeLocked)
		},
		"ErrCodePasteTooLarge": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodePasteTooLarge, errCodePasteTooLarge)
		},
		"ErrCodePasteUnsupportedAgent": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodePasteUnsupportedAgent, errCodePasteUnsupportedAgent)
		},
		"ErrCodePasteUnsupportedKind": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodePasteUnsupportedKind, errCodePasteUnsupportedKind)
		},
		"ErrCodeTitleFeatureDisabled": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodeTitleFeatureDisabled, errCodeTitleFeatureDisabled)
		},
		"ErrCodeTitleNoContent": func(t *testing.T) {
			sameSessionCode(t, w.ErrCodeTitleNoContent, errCodeTitleNoContent)
		},
	}

	// Reconcile the set of checks against Deps' set of fields. A new field always fails here,
	// so "wiring added, check not added" cannot happen.
	typ := reflect.TypeOf(w)
	seen := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		seen[name] = true
		if _, ok := checks[name]; !ok {
			t.Errorf("the wiring of sessionx.Deps.%s is unchecked (add a check when you add a field)", name)
		}
	}
	for name := range checks {
		if !seen[name] {
			t.Errorf("sessionx.Deps has no %s (only the check is stale)", name)
		}
	}
	for name, run := range checks {
		t.Run(name, run)
	}
}

// TestSessionWiringErrorCodesAreDistinct checks that the 12 error codes are all spelled
// differently.
//
// Why it is needed: the per-field comparison above only asks "is it the same as the real
// one", so two constants in errcodes.go holding the same string pass straight through - and
// that renders the swap check itself powerless, because either wiring then matches. The
// three paste codes and the four fork codes are spelled alike, which makes a copy-paste that
// leaves two of them equal an easy accident.
func TestSessionWiringErrorCodesAreDistinct(t *testing.T) {
	w := sessionx.Wired()
	seen := map[string]string{}
	typ := reflect.TypeOf(w)
	val := reflect.ValueOf(w)
	n := 0
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type.Kind() != reflect.String {
			continue
		}
		n++
		code := val.Field(i).String()
		if code == "" {
			t.Errorf("%s is empty (an empty code reaches the Console and i18n cannot resolve it)", f.Name)
			continue
		}
		if prev, dup := seen[code]; dup {
			t.Errorf("%s and %s hold the same code %q, which disables the swap check",
				prev, f.Name, code)
		}
		seen[code] = f.Name
	}
	// Lower bound on how much was scanned (#320: "finding nothing checks nothing").
	if n != 12 {
		t.Fatalf("only %d string fields were scanned (want 12) = this check has gone silent", n)
	}
}

// sameSessionFunc checks that the function itself is what got wired. A closure or another
// function has a different code pointer and fails.
func sameSessionFunc(t *testing.T, got, want any) {
	t.Helper()
	g, w := reflect.ValueOf(got).Pointer(), reflect.ValueOf(want).Pointer()
	if g != w {
		t.Fatalf("wired to the wrong target: got %s, want %s", sessionFuncName(g), sessionFuncName(w))
	}
}

func sessionFuncName(pc uintptr) string {
	if f := runtime.FuncForPC(pc); f != nil {
		return f.Name()
	}
	return "?"
}

func sameSessionCode(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("error code = %q, want %q", got, want)
	}
}
