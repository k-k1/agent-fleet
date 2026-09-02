package mcpx

// mcpOwningSession の cwd fallback（managed セッションは AF_SESSION_NAME を持てない —
// 共有 daemon の子として MCP が起動するため）。作業フォルダは複数セッションで共有される
// のが普通なので、生存でひとつに絞れるかどうかが実用上の分かれ目になる。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// ownerTestEnv points the meta store at a temp dir, moves the process into a temp
// cwd, clears AF_SESSION_NAME's effect, and serves /sessions/{name}/status from the
// given liveness map. A name mapped to no entry answers 500 (probe failure).
func ownerTestEnv(t *testing.T, alive map[string]bool) (cwd string, probed *[]string) {
	t.Helper()
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	dir := t.TempDir()
	t.Chdir(dir)

	old := mcpSourceSession
	mcpSourceSession = ""
	t.Cleanup(func() { mcpSourceSession = old })

	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Base(strings.TrimSuffix(r.URL.Path, "/status"))
		calls = append(calls, name)
		a, ok := alive[name]
		if !ok {
			http.Error(w, `{"code":"boom"}`, http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(w, `{"alive":%t,"ready":%t}`, a, a)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)

	// The meta's Dir must be the same string mcpOwningSession compares against, and
	// t.Chdir's argument can differ from it (/tmp is a symlink on some hosts).
	resolved, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return resolved, &calls
}

func writeOwnerMeta(t *testing.T, name, dir string, archived bool) {
	t.Helper()
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: "codex", Archived: archived})
}

func TestMCPOwningSessionPrefersEnvOverCwd(t *testing.T) {
	cwd, _ := ownerTestEnv(t, nil)
	writeOwnerMeta(t, "cwdmate", cwd, false)
	mcpSourceSession = "named01"

	got, err := mcpOwningSession()
	if err != nil || got != "named01" {
		t.Fatalf("mcpOwningSession() = %q, %v; want the AF_SESSION_NAME slot", got, err)
	}
}

// The shape that broke a real handoff on 2026-08-09: a managed session sharing its
// worktree with a stopped session. Only Archived was filtered, so both counted and
// the tool refused.
func TestMCPOwningSessionNarrowsSharedFolderByLiveness(t *testing.T) {
	cwd, probed := ownerTestEnv(t, map[string]bool{"livesess": true, "deadsess": false})
	writeOwnerMeta(t, "livesess", cwd, false)
	writeOwnerMeta(t, "deadsess", cwd, false)

	got, err := mcpOwningSession()
	if err != nil || got != "livesess" {
		t.Fatalf("mcpOwningSession() = %q, %v; want the live session in the shared folder", got, err)
	}
	if len(*probed) != 2 {
		t.Fatalf("liveness probes = %v, want both candidates probed", *probed)
	}
}

func TestMCPOwningSessionSkipsProbeWhenFolderIsUnambiguous(t *testing.T) {
	cwd, probed := ownerTestEnv(t, nil)
	writeOwnerMeta(t, "onlyone", cwd, false)
	writeOwnerMeta(t, "elsewhere", cwd+"-other", false)
	writeOwnerMeta(t, "archived0", cwd, true)

	got, err := mcpOwningSession()
	if err != nil || got != "onlyone" {
		t.Fatalf("mcpOwningSession() = %q, %v; want the only session in this folder", got, err)
	}
	if len(*probed) != 0 {
		t.Fatalf("liveness probes = %v, want none for an unambiguous folder", *probed)
	}
}

func TestMCPOwningSessionRefusesWhenSeveralAreLive(t *testing.T) {
	cwd, _ := ownerTestEnv(t, map[string]bool{"liveaaa": true, "livebbb": true})
	writeOwnerMeta(t, "liveaaa", cwd, false)
	writeOwnerMeta(t, "livebbb", cwd, false)

	_, err := mcpOwningSession()
	if err == nil {
		t.Fatal("mcpOwningSession() succeeded; want an ambiguity error, never a guess")
	}
	// The names are what makes the refusal actionable in the mirror.
	if !strings.Contains(err.Error(), "liveaaa") || !strings.Contains(err.Error(), "livebbb") {
		t.Fatalf("error = %q, want both candidate names", err)
	}
}

// A failed probe must not narrow: the unreachable one could be the caller itself, and
// dropping it would file the handoff under somebody else's session.
func TestMCPOwningSessionDoesNotNarrowOnProbeFailure(t *testing.T) {
	cwd, _ := ownerTestEnv(t, map[string]bool{"livesess": true}) // "brokensess" → 500
	writeOwnerMeta(t, "livesess", cwd, false)
	writeOwnerMeta(t, "brokensess", cwd, false)

	got, err := mcpOwningSession()
	if err == nil {
		t.Fatalf("mcpOwningSession() = %q; want a refusal when liveness is unknown", got)
	}
}

func TestMCPOwningSessionReportsNoSessionForForeignFolder(t *testing.T) {
	cwd, _ := ownerTestEnv(t, nil)
	writeOwnerMeta(t, "elsewhere", cwd+"-other", false)

	_, err := mcpOwningSession()
	if err == nil || !strings.Contains(err.Error(), "AF_SESSION_NAME") {
		t.Fatalf("error = %v, want the AF_SESSION_NAME diagnosis", err)
	}
}
