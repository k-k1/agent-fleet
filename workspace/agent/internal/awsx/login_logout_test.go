package awsx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudlogin"
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

// portal stands in for the SSO portal's Logout API and records every call.
type portal struct {
	mu     sync.Mutex
	calls  []string // "<method> <path> <region> <token>"
	status int
}

func fakePortal(t *testing.T) *portal {
	t.Helper()
	p := &portal{status: http.StatusOK}
	var region string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.calls = append(p.calls, r.Method+" "+r.URL.Path+" "+region+" "+r.Header.Get("x-amz-sso_bearer_token"))
		w.WriteHeader(p.status)
		if p.status != http.StatusOK {
			w.Write([]byte(`{"message":"Too many requests"}`))
		}
	}))
	t.Cleanup(srv.Close)
	old := ssoPortalURL
	ssoPortalURL = func(r string) string { region = r; return srv.URL }
	t.Cleanup(func() { ssoPortalURL = old })
	return p
}

func (p *portal) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
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
	p := fakePortal(t)
	mine, others := logoutFixture(t)

	rec, out := profileLogout(t, "prod")
	if rec.Code != http.StatusOK || !out.Revoked || out.NoToken || out.Message != "" {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret-") {
		t.Fatalf("a token left the Agent: %s", rec.Body.String())
	}
	// The token carries no region, so the profile's SSO region is used.
	if got := p.seen(); len(got) != 1 || got[0] != "POST /logout ap-northeast-1 secret-access" {
		t.Fatalf("portal calls = %q", got)
	}
	assertGone(t, mine, others)
}

func TestProfileLogoutSignsOutHereWhenAWSRefuses(t *testing.T) {
	p := fakePortal(t)
	p.status = http.StatusTooManyRequests
	mine, others := logoutFixture(t)

	rec, out := profileLogout(t, "prod")
	if rec.Code != http.StatusOK || out.Revoked || !strings.Contains(out.Message, "429") {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body.String())
	}
	assertGone(t, mine, others)
}

func TestProfileLogoutSendsTheTokenOnlyToAPortalHost(t *testing.T) {
	p := fakePortal(t)
	mine, others := logoutFixture(t)
	writeSSOCacheDoc(t, map[string]string{"accessToken": "secret-access", "expiresAt": "2099-01-01T00:00:00Z",
		"region": "evil.example/x"})

	rec, out := profileLogout(t, "prod")
	if rec.Code != http.StatusOK || out.Revoked || !strings.Contains(out.Message, "region") {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body.String())
	}
	if got := p.seen(); len(got) != 0 {
		t.Fatalf("the token was sent: %q", got)
	}
	assertGone(t, mine, others)
}

func TestProfileLogoutWithoutATokenRevokesNothing(t *testing.T) {
	p := fakePortal(t)
	mine, others := logoutFixture(t)
	os.Remove(mine[0])

	rec, out := profileLogout(t, "prod")
	if rec.Code != http.StatusOK || out.Revoked || !out.NoToken {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body.String())
	}
	if got := p.seen(); len(got) != 0 {
		t.Fatalf("revoked with no token: %q", got)
	}
	// Role credentials outlive the token, so they still go.
	assertGone(t, mine, others)
}

// Settings is cached before the managed block is rewritten, so for a while (or after a
// failed write) the CLI keys the role cache by the block's account and role, not Settings'.
func TestProfileLogoutRemovesTheRoleCacheOfTheManagedBlockToo(t *testing.T) {
	fakePortal(t)
	mine, others := logoutFixture(t)
	moved := prodSettings["prod"]
	moved.RoleName = "Admin"
	if err := saveSettingsCache([]Profile{moved}, nil); err != nil {
		t.Fatal(err)
	}
	newer := ssoRoleCachePath(moved.AccountID, moved.RoleName, "af-prod")
	os.WriteFile(newer, []byte("{}"), 0o600)

	if rec, _ := profileLogout(t, "prod"); rec.Code != http.StatusOK {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body.String())
	}
	assertGone(t, append(mine, newer), others)
}

func TestProfileLogoutEndsARunningLoginAndWaitsForIt(t *testing.T) {
	fakePortal(t)
	mine, _ := logoutFixture(t)
	os.Remove(mine[0])
	// The CLI is killed, but what it was writing may still land while it goes; the cleanup
	// stands in for that late write.
	wrote := make(chan struct{})
	a, err := logins.Start("af-prod", "", "prod", cloudlogin.Process{
		Name: "fake login", Path: "sleep", Args: []string{"5"}, Env: os.Environ(), Timeout: time.Minute,
		Parse:  func(string) (string, string, error) { return "", "", nil },
		Exited: func(error) (bool, string) { return false, "" },
		Cleanup: func() {
			time.Sleep(100 * time.Millisecond)
			os.WriteFile(mine[0], []byte(`{"accessToken":"late"}`), 0o600)
			close(wrote)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if rec, _ := profileLogout(t, "prod"); rec.Code != http.StatusOK {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body.String())
	}
	if v := a.View(); v.Phase != cloudlogin.PhaseCancelled {
		t.Fatalf("the running login was not ended: phase=%s", v.Phase)
	}
	// Whatever the CLI wrote before it exited must be gone.
	select {
	case <-wrote:
	case <-time.After(5 * time.Second):
		t.Fatal("the login never exited")
	}
	assertGone(t, mine, nil)
}

// An af-aws-exec run that read the token before the logout writes role credentials back
// after it; the logout must wait for it, not delete under it.
func TestProfileLogoutWaitsForARunReadingTheCache(t *testing.T) {
	fakePortal(t)
	mine, others := logoutFixture(t)
	unlock, err := logins.LockKey("af-prod", true)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		profileLogout(t, "prod")
	}()
	time.Sleep(100 * time.Millisecond)
	os.WriteFile(mine[1], []byte(`{"ProviderType": "sso"}`), 0o600)
	unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the logout never finished")
	}
	assertGone(t, mine, others)
}

func TestProfileLogoutRefusesWhatItDoesNotOwn(t *testing.T) {
	p := fakePortal(t)
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
	if got := p.seen(); len(got) != 0 {
		t.Fatalf("a refused logout called AWS: %q", got)
	}
	assertGone(t, nil, append(mine, others...))
}

// The other half of the lock: an af-aws-exec run does not read the cache while a logout
// holds it.
func TestExportSSOCredsWaitsForALogout(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600)
	unlock, err := logins.LockKey("af-prod", false)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := exportSSOCreds(awsRunner{bin: bin, env: os.Environ()}, "af-prod")
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("read the cache while a logout held it")
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("never read the cache after the logout")
	}
}

// A role profile whose source_profile chain ends in a Settings SSO profile reads the same
// cached login, so it waits for a logout of that session too.
func TestPlanExecThroughAnSSOSourceWaitsForALogout(t *testing.T) {
	src := "[profile src]\nsso_session = af-prod\nsso_account_id = 123456789012\nsso_role_name = Dev\n"
	bin, _ := fakeDeploy(t, deployProfile, src, "", deployARN)
	unlock, err := logins.LockKey("af-prod", false)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, _, err := PlanExec(bin, workloadEnv, ExecOptions{Profile: "prod", Account: deployAccount, Login: "never",
			Argv: []string{"true"}, Quiet: true})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("ran while a logout held the source's login (err = %v)", err)
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("never ran after the logout")
	}
}

// blockCacheLock makes af-prod's cache lock impossible to take.
func blockCacheLock(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(logins.LockKeyPath("af-prod"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestNoLockMeansNoLogoutAndNoRun(t *testing.T) {
	p := fakePortal(t)
	mine, others := logoutFixture(t)
	blockCacheLock(t)
	rec, _ := profileLogout(t, "prod")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), `"lock_failed"`) {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body.String())
	}
	if got := p.seen(); len(got) != 0 {
		t.Fatalf("called AWS without the lock: %q", got)
	}
	assertGone(t, nil, append(mine, others...))

	bin, state := fakeAWS(t, ssoProfile)
	os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600)
	blockCacheLock(t)
	if _, err := exportSSOCreds(awsRunner{bin: bin, env: os.Environ()}, "af-prod"); err == nil {
		t.Fatal("read the cache without the lock")
	}
	if n := cliCalls(state); n != 0 {
		t.Fatalf("aws was started %d times without the lock", n)
	}
}

// The revoke can take seconds; a login the member finishes meanwhile is a new one, and its
// token stays.
func TestProfileLogoutKeepsALoginThatLandsDuringTheRevoke(t *testing.T) {
	mine, others := logoutFixture(t)
	called, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(called)
		<-release
	}))
	t.Cleanup(srv.Close)
	old := ssoPortalURL
	ssoPortalURL = func(string) string { return srv.URL }
	t.Cleanup(func() { ssoPortalURL = old })

	done := make(chan profileLogoutWire, 1)
	go func() {
		_, out := profileLogout(t, "prod")
		done <- out
	}()
	<-called
	fresh := []byte(`{"accessToken":"new-login"}`)
	os.WriteFile(mine[0], fresh, 0o600)
	close(release)
	if out := <-done; !out.Revoked {
		t.Fatalf("logout = %+v", out)
	}
	if b, _ := os.ReadFile(mine[0]); string(b) != string(fresh) {
		t.Fatalf("the new login's token is %q", b)
	}
	assertGone(t, mine[1:], others)
}

func TestTakeOffTokenPutsBackANewerLogin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	os.WriteFile(path, []byte("old"), 0o600)
	if err := takeOffToken(path, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the logged-out token stayed")
	}
	os.WriteFile(path, []byte("newer"), 0o600)
	if err := takeOffToken(path, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "newer" {
		t.Fatalf("a newer login was taken off: %q", b)
	}
	if _, err := os.Stat(path + ".af-logout"); !os.IsNotExist(err) {
		t.Fatal("the side file stayed")
	}
}

// A still newer login written while the put-back is decided must not be replaced by it.
func TestTakeOffTokenNeverReplacesAStillNewerLogin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	os.WriteFile(path, []byte("newer"), 0o600)
	beforePutBack = func() { os.WriteFile(path, []byte("newest"), 0o600) }
	t.Cleanup(func() { beforePutBack = func() {} })
	if err := takeOffToken(path, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "newest" {
		t.Fatalf("the newest login was replaced: %q", b)
	}
	if _, err := os.Stat(path + ".af-logout"); !os.IsNotExist(err) {
		t.Fatal("the side file stayed")
	}
}

// botocore rewrites the token file in place (open, truncate, write), so a login CLI that
// opened it before the logout took it off would write into the deleted file. No login CLI
// starts while a logout holds the gate.
func TestALoginDoesNotStartWhileALogoutTakesTheTokenOff(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	exportedProd(t)
	old := LoginAWSBin
	LoginAWSBin = func() (string, error) { return bin, nil }
	t.Cleanup(func() { LoginAWSBin = old })
	os.WriteFile(filepath.Join(state, "onLogin"), []byte("sleep 5\n"), 0o600)

	g := logins.Gate("af-prod")
	g.Lock()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- profileStart("prod") }()
	select {
	case <-done:
		g.Unlock()
		t.Fatal("a login started while a logout held the gate")
	case <-time.After(150 * time.Millisecond):
	}
	if n := cliCalls(state); n != 0 {
		g.Unlock()
		t.Fatalf("aws was started %d times", n)
	}
	g.Unlock()
	rec := <-done
	if rec.Code != http.StatusOK {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
	cur := logins.Current("af-prod")
	cur.End(cloudlogin.PhaseFailed, "")
	<-cur.Exited()
}

// The logout holds the gate until the old token is off disk, and not while AWS answers.
func TestProfileLogoutHoldsTheGateOnlyUntilTheTokenIsOff(t *testing.T) {
	mine, _ := logoutFixture(t)
	gateFree := make(chan bool, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g := logins.Gate("af-prod")
		free := g.TryLock()
		if free {
			g.Unlock()
		}
		gateFree <- free
	}))
	t.Cleanup(srv.Close)
	old := ssoPortalURL
	ssoPortalURL = func(string) string { return srv.URL }
	t.Cleanup(func() { ssoPortalURL = old })

	g := logins.Gate("af-prod")
	g.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		profileLogout(t, "prod")
	}()
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(mine[0]); err != nil {
		g.Unlock()
		t.Fatal("the token was taken off without the gate")
	}
	g.Unlock()
	<-done
	if !<-gateFree {
		t.Fatal("the gate was held during the revoke")
	}
	assertGone(t, mine, nil)
}
