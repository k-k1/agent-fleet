package main

import (
	"bytes"
	"encoding/base64"
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

// agt is a token in the shape the CP mints for membership id, with a tag made of c.
func agt(id string, c byte) string {
	return "afg_" + base64.RawURLEncoding.EncodeToString([]byte(id)) + "." + strings.Repeat(string(c), 22)
}

func putInternalGitToken(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	buildMux().ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/internal-git/token", strings.NewReader(body)))
	return w
}

func storedInternalGitToken() string {
	var out bytes.Buffer
	credHelperGet(strings.NewReader("protocol=http\nhost=127.0.0.1:8080\n\n"), &out)
	return strings.TrimPrefix(strings.TrimSuffix(out.String(), "\n"), "username=x-access-token\npassword=")
}

func withInternalGitEnv(t *testing.T, token, epoch string) {
	t.Helper()
	t.Setenv("AF_INTERNAL_GIT_HOST", "127.0.0.1:8080")
	t.Setenv("AF_INTERNAL_GIT_TOKEN", token)
	t.Setenv("AF_INTERNAL_GIT_EPOCH", epoch)
}

// A rotated token pushed by the CP (issue #1199) replaces the stored one at once, and an
// Agent restart in the same container — whose env still holds the dead token — does not
// seed the dead one back. A new container start with a new token seeds normally.
func TestPushedInternalGitTokenSurvivesAnAgentRestart(t *testing.T) {
	withAgentHome(t)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	old, fresh, next := agt("mem-1", 'a'), agt("mem-1", 'b'), agt("mem-1", 'c')
	withInternalGitEnv(t, old, "0")
	seedInternalGit()

	if w := putInternalGitToken(t, `{"token":"`+fresh+`","epoch":1}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"updated":true`) {
		t.Fatalf("push = %d %s, want 200 updated", w.Code, w.Body.String())
	}
	if got := storedInternalGitToken(); got != fresh {
		t.Fatalf("after the push the helper answers %q, want %q", got, fresh)
	}

	seedInternalGit() // the Agent restarts; env still holds the epoch-0 token
	if got := storedInternalGitToken(); got != fresh {
		t.Fatalf("an Agent restart seeded the dead token back: %q", got)
	}

	withInternalGitEnv(t, next, "2") // a new container start
	seedInternalGit()
	if got := storedInternalGitToken(); got != next {
		t.Fatalf("a new start's token was not seeded: %q", got)
	}
}

// Two CP replicas rotating at once can deliver their pushes in the reverse order. The
// later epoch must stay stored whatever order they arrive in, or the workspace ends up
// holding a token the CP already refuses.
func TestInternalGitTokenPushOfAnOlderEpochIsSuperseded(t *testing.T) {
	withAgentHome(t)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	withInternalGitEnv(t, agt("mem-1", 'a'), "0")
	seedInternalGit()
	tok1, tok2 := agt("mem-1", 'b'), agt("mem-1", 'c')
	if w := putInternalGitToken(t, `{"token":"`+tok2+`","epoch":2}`); !strings.Contains(w.Body.String(), `"updated":true`) {
		t.Fatalf("epoch-2 push = %d %s", w.Code, w.Body.String())
	}
	w := putInternalGitToken(t, `{"token":"`+tok1+`","epoch":1}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"superseded":true`) {
		t.Fatalf("late epoch-1 push = %d %s, want 200 superseded", w.Code, w.Body.String())
	}
	if got := storedInternalGitToken(); got != tok2 {
		t.Fatalf("stored token after a late older push = %q, want the epoch-2 one", got)
	}
	// Nor does an env token of an older epoch, on an Agent restart, displace it.
	seedInternalGit()
	if got := storedInternalGitToken(); got != tok2 {
		t.Fatalf("stored token after an Agent restart = %q, want the epoch-2 one", got)
	}
}

// The Agent bearer is held by every session in the workspace, so the route only accepts a
// token of the CP's shape for this workspace's own membership, in a small body.
func TestInternalGitTokenPushRefusesWhatTheCPCouldNotHaveSent(t *testing.T) {
	withAgentHome(t)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	own := agt("mem-1", 'a')
	withInternalGitEnv(t, own, "0")
	seedInternalGit()
	for name, body := range map[string]string{
		"not ours":           `{"token":"ghp_not_ours","epoch":1}`,
		"prefix only":        `{"token":"afg_anything","epoch":1}`,
		"short tag":          `{"token":"afg_bWVtLTE.abc","epoch":1}`,
		"another membership": `{"token":"` + agt("mem-2", 'b') + `","epoch":1}`,
		"negative epoch":     `{"token":"` + agt("mem-1", 'b') + `","epoch":-1}`,
		"oversized body":     `{"token":"` + agt("mem-1", 'b') + `","epoch":1,"pad":"` + strings.Repeat("x", 8<<10) + `"}`,
	} {
		if w := putInternalGitToken(t, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: push = %d %s, want 400", name, w.Code, w.Body.String())
		}
	}
	if got := storedInternalGitToken(); got != own {
		t.Fatalf("a refused push changed the stored token to %q", got)
	}
}

// Without an injected host there is no internal git to hold a token for.
func TestPushInternalGitTokenWithoutHost(t *testing.T) {
	withAgentHome(t)
	t.Setenv("AF_INTERNAL_GIT_HOST", "")
	if w := putInternalGitToken(t, `{"token":"`+agt("mem-1", 'a')+`","epoch":1}`); w.Code != http.StatusConflict {
		t.Fatalf("push without a host = %d, want 409", w.Code)
	}
}
