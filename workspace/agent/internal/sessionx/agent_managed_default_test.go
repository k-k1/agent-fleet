package sessionx

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The kinds that start managed by default are the hand list the callers kept before they were
// derived (codex opencode copilot cursor kiro) plus lcpp and muse, which have no Terminal route.
// The literal is the point: a kind registered with ManagedDefault, or one dropped, changes the
// set and has to be decided here.
func TestManagedDefaultKindFollowsTheRegistry(t *testing.T) {
	want := []string{"codex", "copilot", "cursor", "kiro", "lcpp", "muse", "opencode"}
	var got []string
	for _, k := range RegisteredKinds() {
		if ManagedDefaultKind(k) {
			got = append(got, k)
		}
		c := AgentOf(k).Caps()
		if c.ManagedOnly && !ManagedDefaultKind(k) {
			t.Errorf("%s: managed-only but not a managed default", k)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("managed-default kinds = %v, want %v", got, want)
	}
}

// An unregistered or alternate spelling gets no managed driver. NormalizeKind would fold these
// onto claude, which is not a managed default either — but "codex" spelled "Codex" must not
// reach the Agent as a managed request for a kind it will launch as claude.
func TestManagedDefaultKindRefusesAlternateSpellings(t *testing.T) {
	for _, k := range []string{"", "claude", "agy", "shell", "ssm", "Codex", "CODEX", " codex", "codex ",
		"kiro-cli", "cursor-agent", "github-copilot", "llamacpp", "llama.cpp", "Muse", "open-code", "antigravity"} {
		if ManagedDefaultKind(k) {
			t.Errorf("ManagedDefaultKind(%q) = true, want false", k)
		}
	}
}

// A CP newer than its Agent may name a kind the Agent has not registered yet. The create is
// refused as bad_kind (#2297) whatever the driver: folding the kind to claude would start a
// claude TUI under the wrong kind when no driver is named, and would answer driver_unsupported
// for the wrong reason when managed is. Driven through HandleCreateSession, because the refusal
// is a branch of the handler, not a property of the registry.
func TestUnregisteredKindWithManagedDriverIsRefused(t *testing.T) {
	logPath := fakeTmux(t)
	// allocSessionName asks tmux has-session; the default stub says every name is taken, which
	// would hang a request that gets past the driver check instead of failing it.
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TMUX_TEST_LOG\"\ncase \"$1\" in has-session) exit 1 ;; esac\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(logPath), "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	const unknown = "kind-this-agent-has-not-registered"
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		HandleCreateSession(rec, httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(body)))
		return rec
	}

	for _, body := range []string{
		`{"kind":"` + unknown + `","driver":"managed"}`,
		`{"kind":"` + unknown + `","dir":"/nonexistent-af-test-dir"}`,
	} {
		rec := post(body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_kind") {
			t.Fatalf("unknown kind, %s: status = %d, body = %s, want 400 bad_kind", body, rec.Code, rec.Body.String())
		}
	}
	if got, _ := os.ReadFile(logPath); strings.Contains(string(got), "new-session") {
		t.Fatalf("a refused request started a tmux session: %q", got)
	}
}
