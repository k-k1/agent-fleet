package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// With AF_CP_INTERNAL_URL injected, the public clone URL the Console hands out is fetched
// through the CP's workspace listener, authenticated with the internal git token, and the
// rewrite follows the internal URL when it changes or goes away.
func TestInternalGitRewriteFollowsTheInternalURL(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	withAgentHome(t)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	t.Setenv("AF_CP_BASE_URL", "https://af.invalid")
	t.Setenv("AF_INTERNAL_GIT_HOST", "af.invalid")
	t.Setenv("AF_INTERNAL_GIT_TOKEN", "afg_tok")
	t.Setenv("AF_CP_INTERNAL_URL", srv.URL)

	rewrites := func() string {
		out, _ := gitx.Run("", "config", "--global", "--get-regexp", `^url\..*\.insteadof$`)
		return out
	}
	syncInternalGitRewrite()
	syncInternalGitRewrite() // idempotent
	if got, want := rewrites(), "url."+srv.URL+"/git/.insteadof https://af.invalid/git/"; got != want {
		t.Fatalf("rewrites = %q, want %q", got, want)
	}

	// git really goes to the listener for the public URL.
	_ = gitx.Cmd("", "ls-remote", "https://af.invalid/git/default/shared.git").Run()
	mu.Lock()
	hit := len(paths) > 0 && strings.HasPrefix(paths[0], "/git/default/shared.git/")
	mu.Unlock()
	if !hit {
		t.Fatalf("ls-remote of the public URL did not reach the workspace listener (paths %v)", paths)
	}

	// The credential is served for the host git now asks about.
	seedInternalGit()
	u, _ := url.Parse(srv.URL)
	var out bytes.Buffer
	credHelperGet(strings.NewReader("protocol=http\nhost="+u.Host+"\n\n"), &out)
	if !strings.Contains(out.String(), "password=afg_tok") {
		t.Errorf("credential for %s = %q, want the internal git token", u.Host, out.String())
	}

	// A changed internal URL replaces the rewrite; an unset one removes it.
	t.Setenv("AF_CP_INTERNAL_URL", "http://af-cp-internal.ns.svc:8098")
	syncInternalGitRewrite()
	if got, want := rewrites(), "url.http://af-cp-internal.ns.svc:8098/git/.insteadof https://af.invalid/git/"; got != want {
		t.Errorf("after a change, rewrites = %q, want %q", got, want)
	}
	t.Setenv("AF_CP_INTERNAL_URL", "")
	syncInternalGitRewrite()
	if got := rewrites(); got != "" {
		t.Errorf("after unset, rewrites = %q, want none", got)
	}
}

// Only what the Agent added is ever touched: a person's own rewrite of the same public git
// base, and an entry identical to the Agent's that existed before it, survive the first start
// without an internal URL, enabling, changing and unsetting it.
func TestInternalGitRewriteLeavesThePersonsEntries(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	withAgentHome(t)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("AF_CP_BASE_URL", "https://af.invalid")
	t.Setenv("AF_INTERNAL_GIT_HOST", "af.invalid")
	t.Setenv("AF_CP_INTERNAL_URL", "")
	set := func(key, val string) {
		t.Helper()
		if out, err := gitx.Combined("", "config", "--global", "--add", key, val); err != nil {
			t.Fatalf("git config %s: %v: %s", key, err, out)
		}
	}
	mine := "url.https://git-route.invalid/.insteadof https://af.invalid/git/"
	preexisting := "url.http://af-cp-internal.ns.svc:8098/git/.insteadof https://af.invalid/git/"
	set("url.https://git-route.invalid/.insteadOf", "https://af.invalid/git/")
	set("url.http://af-cp-internal.ns.svc:8098/git/.insteadOf", "https://af.invalid/git/")
	// Lines, not a set: a second copy of an entry is exactly what borrowing must not add.
	entries := func() map[string]int {
		out, _ := gitx.Run("", "config", "--global", "--get-regexp", `^url\..*\.insteadof$`)
		m := map[string]int{}
		for _, l := range strings.Split(out, "\n") {
			if l != "" {
				m[l]++
			}
		}
		return m
	}
	check := func(stage string, extra ...string) {
		t.Helper()
		got := entries()
		want := map[string]bool{mine: true, preexisting: true}
		for _, e := range extra {
			want[e] = true
		}
		if len(got) != len(want) {
			t.Errorf("%s: entries = %v, want %v", stage, got, want)
		}
		for e := range want {
			if got[e] != 1 {
				t.Errorf("%s: %q appears %d times, want once (entries %v)", stage, e, got[e], got)
			}
		}
	}

	syncInternalGitRewrite()
	check("first start without an internal URL")

	// Enabled with the same URL the person already rewrites to: borrowed, not owned.
	t.Setenv("AF_CP_INTERNAL_URL", "http://af-cp-internal.ns.svc:8098")
	syncInternalGitRewrite()
	check("enabled onto the existing entry")

	t.Setenv("AF_CP_INTERNAL_URL", "http://af-cp-internal2.ns.svc:8098")
	syncInternalGitRewrite()
	check("changed", "url.http://af-cp-internal2.ns.svc:8098/git/.insteadof https://af.invalid/git/")

	t.Setenv("AF_CP_INTERNAL_URL", "")
	syncInternalGitRewrite()
	check("unset")

	// The public base going away still clears what the Agent owned.
	t.Setenv("AF_CP_INTERNAL_URL", "http://af-cp-internal3.ns.svc:8098")
	syncInternalGitRewrite()
	check("re-enabled", "url.http://af-cp-internal3.ns.svc:8098/git/.insteadof https://af.invalid/git/")
	t.Setenv("AF_CP_BASE_URL", "")
	syncInternalGitRewrite()
	check("public base removed")
}
