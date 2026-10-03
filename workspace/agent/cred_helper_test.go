package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// What the CP injects as AF_INTERNAL_GIT_HOST is the key the credential is stored
// under, and git asks with `host=<name>:<port>` whenever the clone URL carries a port.
// The seed and the helper have to meet on that key.
func TestInternalGitCredentialResolvesWithPort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	t.Setenv("AF_INTERNAL_GIT_HOST", "127.0.0.1:8080")
	t.Setenv("AF_INTERNAL_GIT_TOKEN", "tok-1")
	seedInternalGit()

	var out bytes.Buffer
	credHelperGet(strings.NewReader("protocol=http\nhost=127.0.0.1:8080\n\n"), &out)
	if want := "username=x-access-token\npassword=tok-1\n"; out.String() != want {
		t.Fatalf("helper answered %q, want %q", out.String(), want)
	}
}

// A store seeded by a CP that dropped the port holds the same token under the bare
// host name; re-seeding moves it rather than leaving it where git hands it to any
// server on the default port.
func TestSeedInternalGitDropsThePortlessEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	if err := secrets.Update(func(s *secrets.Data) error {
		s.Git["127.0.0.1"] = secrets.GitEntry{User: "x-access-token", Token: "tok-1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_INTERNAL_GIT_HOST", "127.0.0.1:8080")
	t.Setenv("AF_INTERNAL_GIT_TOKEN", "tok-1")
	seedInternalGit()

	s, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Git["127.0.0.1"]; ok {
		t.Fatalf("the portless entry survived: %v", s.Git)
	}
	if e := s.Git["127.0.0.1:8080"]; e.Token != "tok-1" {
		t.Fatalf("the credential was not stored under host:port: %v", s.Git)
	}
}

// A rotated token pushed by the CP (issue #1199) replaces the stored one at once, and an
// Agent restart in the same container — whose env still holds the dead token — does not
// seed the dead one back. A new container start with a new token seeds normally.
func TestPushedInternalGitTokenSurvivesAnAgentRestart(t *testing.T) {
	withAgentHome(t)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("AF_INTERNAL_GIT_HOST", "127.0.0.1:8080")
	t.Setenv("AF_INTERNAL_GIT_TOKEN", "afg_old")
	seedInternalGit()

	stored := func() string {
		var out bytes.Buffer
		credHelperGet(strings.NewReader("protocol=http\nhost=127.0.0.1:8080\n\n"), &out)
		return out.String()
	}
	mux := buildMux()
	put := func(body string) int {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/internal-git/token", strings.NewReader(body)))
		return w.Code
	}
	if code := put(`{"token":"ghp_not_ours"}`); code != http.StatusBadRequest {
		t.Fatalf("non-internal token = %d, want 400", code)
	}
	if code := put(`{"token":"afg_new"}`); code != http.StatusOK {
		t.Fatalf("push = %d, want 200", code)
	}
	if want := "username=x-access-token\npassword=afg_new\n"; stored() != want {
		t.Fatalf("after the push the helper answers %q, want %q", stored(), want)
	}

	seedInternalGit() // the Agent restarts; env still says afg_old
	if want := "username=x-access-token\npassword=afg_new\n"; stored() != want {
		t.Fatalf("an Agent restart seeded the dead token back: %q", stored())
	}

	t.Setenv("AF_INTERNAL_GIT_TOKEN", "afg_next") // a new container start
	seedInternalGit()
	if want := "username=x-access-token\npassword=afg_next\n"; stored() != want {
		t.Fatalf("a new start's token was not seeded: %q", stored())
	}
}

// Without an injected host there is no internal git to hold a token for.
func TestPushInternalGitTokenWithoutHost(t *testing.T) {
	withAgentHome(t)
	t.Setenv("AF_INTERNAL_GIT_HOST", "")
	w := httptest.NewRecorder()
	buildMux().ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/internal-git/token", strings.NewReader(`{"token":"afg_x"}`)))
	if w.Code != http.StatusConflict {
		t.Fatalf("push without a host = %d, want 409", w.Code)
	}
}
