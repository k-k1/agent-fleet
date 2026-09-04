package mcpx

// The mcpx unit tests have no package main, so the outward dependencies are wired to fakes
// here. Neither the numbers nor the wording are the real ones (main's mcp_wiring.go wires
// those) — copying the real values in would make this a second source for them.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Where the mcpx unit tests keep this state (in production main's mcp_wiring.go holds it).
var (
	testWrite, testSelfReport, testChromium bool
	testConvID                              string
)

func init() { Configure(testDeps()) }

// testDeps is the whole set of fakes for the mcpx unit tests. It sits in one place because
// the completeness check below uses the same set.
func testDeps() Deps {
	return Deps{
		WriteEnabled:              func() bool { return testWrite },
		SetWriteEnabled:           func(v bool) { testWrite = v },
		SelfReportOnly:            func() bool { return testSelfReport },
		SetSelfReportOnly:         func(v bool) { testSelfReport = v },
		SessionChromiumEnabled:    func() bool { return testChromium },
		SetSessionChromiumEnabled: func(v bool) { testChromium = v },
		ConvID:                    func() string { return testConvID },
		SetConvID:                 func(v string) { testConvID = v },

		CleanTitle: func(s string) (string, bool) {
			s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
			return s, s != ""
		},
		SessionTitleMaxRunes:  80,
		PeerIntentNames:       []string{"request", "question", "answer", "notice"},
		PeerReachableSessions: func(string) []session.Meta { return nil },
		ReportKindSelfReport:  "self-report",

		// The fake gate approves by default. The tests that examine approval itself live in
		// main, where the real gate is — checking a fake one here would measure nothing.
		ApprovalGate:      func(string, string) error { return nil },
		ApprovalLabel:     func(op string) string { return op },
		ShellCreateTarget: func(dir, prompt string) string { return dir + " " + prompt },
		ShellSendTarget:   func(name, prompt string) string { return name + " " + prompt },
		SessionIsShell:    func(string) bool { return false },

		ReadUIPrefs:                func() map[string]any { return map[string]any{} },
		EnsureClaudeSettingsWiring: func(string) {},

		RepoAnyDirFromPath: func(w http.ResponseWriter, r *http.Request) (string, bool) {
			dir := r.URL.Query().Get("dir")
			if dir == "" {
				http.Error(w, "no dir", http.StatusBadRequest)
				return "", false
			}
			return dir, true
		},

		ReadBuildPins:      func() map[string]string { return map[string]string{} },
		AgentFleetShareDir: func() string { return filepath.Join(os.Getenv("HOME"), ".local", "share", "agent-fleet") },
		InstallGrafanaMCP:  func(string) (string, error) { return "", os.ErrNotExist },

		WriteSSMConfig: func(string, session.SSMMeta) error { return nil },
	}
}

// TestConfigureRejectsEveryUnwiredField checks that Configure panics when ANY single field
// of Deps is left unwired.
//
// The completeness check itself runs through reflect, so a new field is covered
// automatically. A hand-written list (on either the implementation or the test side) is
// forgotten when a field is added, and forgetting it raises nothing — what breaks is the
// silent side, e.g. a title limit that quietly becomes 0.
func TestConfigureRejectsEveryUnwiredField(t *testing.T) {
	good := testDeps()
	v := reflect.ValueOf(good)
	typ := v.Type()
	if typ.NumField() < 20 {
		t.Fatalf("Deps has only %d fields (the wrong struct is being measured)", typ.NumField())
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		t.Run(f.Name, func(t *testing.T) {
			// Always restore the correct wiring (deps is shared by the whole package).
			defer Configure(good)
			broken := reflect.New(typ).Elem()
			broken.Set(v)
			broken.Field(i).Set(reflect.Zero(f.Type))
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("Configure accepted %s left unwired (a missing wiring passes silently)", f.Name)
				}
				if !strings.Contains(fmt.Sprint(r), f.Name) {
					t.Fatalf("the panic does not name %s: %v", f.Name, r)
				}
			}()
			Configure(broken.Interface().(Deps))
		})
	}
}

// TestConfigureRejectsHollowValues checks that a wiring which is non-zero but empty is
// refused too (walking the `unwired` branches one by one). Looking at the zero value alone
// lets `[]string{}` or a negative limit pass as "wired".
func TestConfigureRejectsHollowValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		bend func(*Deps)
	}{
		{"SessionTitleMaxRunes is negative", func(d *Deps) { d.SessionTitleMaxRunes = -1 }},
		{"PeerIntentNames is an empty slice", func(d *Deps) { d.PeerIntentNames = []string{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer Configure(testDeps())
			d := testDeps()
			tc.bend(&d)
			defer func() {
				if recover() == nil {
					t.Fatalf("Configure accepted it even with %s", tc.name)
				}
			}()
			Configure(d)
		})
	}
}

// mcpToolWait bounds the wait for a tool to hit the Agent. It exists so the wait is never a
// bare `<-ch`; on the happy path nothing waits this long (it returns the instant the tool
// hits).
const mcpToolWait = 5 * time.Second

// awaitHit waits, with an upper bound, for a tool to hit the Agent.
//
// A bare `<-ch` hangs instead of failing when the tool is not advertised at all (a broken
// `--write` wiring, a missing advertisement, a renamed tool). In CI that is not a red test
// but a job timeout, and nothing records which check was waiting for what. Measured
// 2026-09-02: with a mutation where `withWriteEnabled` sets no value,
// `TestMCPCreateSessionForwardsWorktreeOptions` only stopped at `panic: test timed out after
// 45s`, while its sibling tests FAILed normally on the same mutation.
//
// The job here is to bound the wait and turn it into a red with a visible reason.
func awaitHit[T any](t *testing.T, ch <-chan T, tool string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(mcpToolWait):
		t.Fatalf("%s did not hit the Agent within %s (the tool is not being advertised: suspect the --write wiring / a missing advertisement / a rename)", tool, mcpToolWait)
		var zero T
		return zero
	}
}

// withTempHome points HOME at a temp dir so the fstore stores write under the test's
// own tree (the same four lines as the identically named helper in main; test helpers cannot
// cross packages, so this package keeps its own).
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

// withMCPFlags sets the tool-set flags temporarily, cleanup included.
func withMCPFlags(t *testing.T, write, selfReport, chromium bool) {
	t.Helper()
	ow, os, oc := writeEnabled(), selfReportOnly(), sessionChromiumEnabled()
	setFlags(write, selfReport, chromium)
	t.Cleanup(func() { setFlags(ow, os, oc) })
}

// withWriteEnabled sets the `--write` flag temporarily.
//
// Restoring the old value is part of this one helper on purpose: when the code was moved, the
// original `old := mcpWriteEnabled / … / t.Cleanup(restore)` was replaced by a bare
// `setWriteEnabled(true)` and the restore was lost. It was harmless only because no test
// happened to depend on the default false; the moment one does, the suite becomes
// order-dependent and nothing warns about it.
func withWriteEnabled(t *testing.T, v bool) {
	t.Helper()
	old := writeEnabled()
	setWriteEnabled(v)
	t.Cleanup(func() { setWriteEnabled(old) })
}

func setFlags(write, selfReport, chromium bool) {
	setWriteEnabled(write)
	setSelfReportOnly(selfReport)
	setSessionChromiumEnabled(chromium)
}
