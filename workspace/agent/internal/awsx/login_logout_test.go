package awsx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSSORoleCachePathMatchesBotocore pins the key to what botocore computes: the first is
// the file aws-cli 2.36.46 wrote, the second Python's json.dumps over non-ASCII, a
// character outside the BMP, DEL, quotes and the characters encoding/json would escape.
func TestSSORoleCachePathMatchesBotocore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, c := range []struct{ account, role, session, want string }{
		{"111111111111", "R", "af-p1", "a06b6ab6dc4ed12c8f306b4e97a614066a604a50"},
		{"333333333333", "Rö\"\\", "af-チーム😀\x7f<&>", "d4e770b08b7c1e49d3915db39bc2adb8e7db040e"},
	} {
		got := ssoRoleCachePath(c.account, c.role, c.session)
		if want := filepath.Join(os.Getenv("HOME"), ".aws", "cli", "cache", c.want+".json"); got != want {
			t.Errorf("ssoRoleCachePath(%q, %q, %q) = %s, want %s", c.account, c.role, c.session, got, want)
		}
	}
}

// fakeLogoutAWS stands in for the CLI's `sso logout`: it records the HOME it ran with, the
// token files that HOME held and its AWS_* environment, then sources onLogout if present.
func fakeLogoutAWS(t *testing.T) (state string) {
	t.Helper()
	dir := t.TempDir()
	state = filepath.Join(dir, "state")
	os.MkdirAll(state, 0o700)
	script := `#!/bin/sh
S="` + state + `"
[ "$1 $2" = "sso logout" ] || exit 1
echo "$HOME" > "$S/home"
ls "$HOME/.aws/sso/cache" > "$S/tokens"
env | grep -E '^AWS_' | sort > "$S/env"
[ -f "$S/onLogout" ] && . "$S/onLogout"
exit 0
`
	bin := filepath.Join(dir, "aws")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := LoginAWSBin
	LoginAWSBin = func() (string, error) { return bin, nil }
	t.Cleanup(func() { LoginAWSBin = old })
	return state
}

func profileLogout(t *testing.T, name string) (*httptest.ResponseRecorder, profileLogoutWire) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/aws-login/profiles/"+name+"/logout", nil)
	req.SetPathValue("name", name)
	HandleProfileLogout(rec, req)
	var out profileLogoutWire
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("logout body = %s", rec.Body.String())
		}
	}
	return rec, out
}

// logoutFixture signs prod in and puts other profiles' caches beside it. It returns prod's
// two files and the files that must survive.
func logoutFixture(t *testing.T) (mine, others []string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	withSettingsCache(t)
	exportProd(t, "")
	writeSSOCache(t, "secret-access", time.Now().Add(time.Hour))
	sp := prodSettings["prod"]
	role := ssoRoleCachePath(sp.AccountID, sp.RoleName, "af-prod")
	tokens := filepath.Dir(ssoCachePath("af-prod"))
	roles := filepath.Dir(role)
	os.MkdirAll(roles, 0o700)
	others = []string{
		ssoCachePath("af-other"),                            // another Settings profile's login
		filepath.Join(tokens, "member-own.json"),            // the member's own sso-session
		ssoRoleCachePath("999999999999", "Dev", "af-other"), // another profile's role credentials
		filepath.Join(roles, "assume-role.json"),
	}
	for _, f := range append([]string{role}, others...) {
		if err := os.WriteFile(f, []byte(`{"ProviderType": "sso"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return []string{ssoCachePath("af-prod"), role}, others
}

func assertGone(t *testing.T, gone, kept []string) {
	t.Helper()
	for _, f := range gone {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s survived the logout", f)
		}
	}
	for _, f := range kept {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s was removed: %v", f, err)
		}
	}
}

func TestProfileLogoutRevokesAndRemovesOnlyThisProfile(t *testing.T) {
	state := fakeLogoutAWS(t)
	mine, others := logoutFixture(t)
	t.Setenv("AWS_ENDPOINT_URL", "http://127.0.0.1:1")
	t.Setenv("AWS_PROFILE", "something-else")

	rec, out := profileLogout(t, "prod")
	if rec.Code != http.StatusOK || !out.Revoked || out.NoToken || out.Message != "" {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret-") {
		t.Fatalf("a token left the Agent: %s", rec.Body.String())
	}
	// The CLI revokes every token its HOME holds, so that HOME must hold prod's alone.
	seen, _ := os.ReadFile(filepath.Join(state, "tokens"))
	if got := strings.TrimSpace(string(seen)); got != filepath.Base(ssoCachePath("af-prod")) {
		t.Fatalf("the CLI's HOME held %q", got)
	}
	home, _ := os.ReadFile(filepath.Join(state, "home"))
	tmpHome := strings.TrimSpace(string(home))
	if tmpHome == os.Getenv("HOME") || tmpHome == "" {
		t.Fatalf("the CLI ran with the member's HOME %q", tmpHome)
	}
	if _, err := os.Stat(tmpHome); !os.IsNotExist(err) {
		t.Errorf("the throwaway HOME %s was left behind", tmpHome)
	}
	env, _ := os.ReadFile(filepath.Join(state, "env"))
	for _, bad := range []string{"AWS_ENDPOINT_URL=", "AWS_PROFILE="} {
		if strings.Contains(string(env), bad) {
			t.Errorf("%s reached the logout: %s", bad, env)
		}
	}
	assertGone(t, mine, others)
}

func TestProfileLogoutSignsOutHereWhenTheRevokeFails(t *testing.T) {
	state := fakeLogoutAWS(t)
	mine, others := logoutFixture(t)
	os.WriteFile(filepath.Join(state, "onLogout"), []byte(`echo 'Could not connect to the endpoint URL' >&2; exit 255
`), 0o600)

	rec, out := profileLogout(t, "prod")
	if rec.Code != http.StatusOK || out.Revoked || !strings.Contains(out.Message, "Could not connect") {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body.String())
	}
	assertGone(t, mine, others)
}

func TestProfileLogoutWithoutATokenRevokesNothing(t *testing.T) {
	state := fakeLogoutAWS(t)
	mine, others := logoutFixture(t)
	os.Remove(mine[0])

	rec, out := profileLogout(t, "prod")
	if rec.Code != http.StatusOK || out.Revoked || !out.NoToken {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(state, "home")); !os.IsNotExist(err) {
		t.Fatal("the CLI was run with no token to revoke")
	}
	// Role credentials outlive the token, so they still go.
	assertGone(t, mine, others)
}

func TestProfileLogoutEndsARunningLogin(t *testing.T) {
	fakeLogoutAWS(t)
	logoutFixture(t)
	stopped := false
	a := &loginAttempt{id: "running", ssoSession: "af-prod", profile: "prod", phase: attemptAuthorize,
		url: "https://device.sso.ap-northeast-1.amazonaws.com/?user_code=ABCD-EFGH", code: "ABCD-EFGH",
		stop: func() { stopped = true }}
	loginAttempts.Lock()
	loginAttempts.byID[a.id], loginAttempts.current["af-prod"] = a, a
	loginAttempts.Unlock()
	t.Cleanup(func() {
		loginAttempts.Lock()
		delete(loginAttempts.byID, a.id)
		delete(loginAttempts.current, "af-prod")
		loginAttempts.Unlock()
	})

	if rec, _ := profileLogout(t, "prod"); rec.Code != http.StatusOK {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body.String())
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.phase != attemptCancelled || a.code != "" || !stopped {
		t.Fatalf("the running login was not ended: phase=%s code=%q stopped=%t", a.phase, a.code, stopped)
	}
}

func TestProfileLogoutRefusesWhatItDoesNotOwn(t *testing.T) {
	state := fakeLogoutAWS(t)
	mine, others := logoutFixture(t)
	// The member's own config defines the profile: af-prod's token is theirs, not ours.
	if err := os.WriteFile(ConfigPath(), []byte("[profile prod]\nregion = us-east-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"nope": "not_a_settings_profile", "prod": "not_exported"} {
		rec, _ := profileLogout(t, name)
		if rec.Code < 400 || !strings.Contains(rec.Body.String(), `"`+want+`"`) {
			t.Errorf("logout %s = %d %s, want %s", name, rec.Code, rec.Body.String(), want)
		}
	}
	if _, err := os.Stat(filepath.Join(state, "home")); !os.IsNotExist(err) {
		t.Fatal("a refused logout ran the CLI")
	}
	assertGone(t, nil, append(mine, others...))
}
