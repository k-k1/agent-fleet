package awsx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
)

// consoleOpts is a run with nobody at a terminal, inside a workspace.
func consoleOpts(stderr *bytes.Buffer, wait time.Duration) ExecOptions {
	return ExecOptions{Profile: "prod", Settings: prodSettings, Login: "auto", Argv: []string{"true"}, Quiet: true,
		Stderr: stderr, ConsoleLogin: true, ConsoleWait: wait, Waiter: LoginWaiter{Session: "s1", Command: "terraform"}}
}

func fastPoll(t *testing.T) {
	t.Helper()
	old := loginPollInterval
	loginPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { loginPollInterval = old })
}

// writeSSOCache writes the token cache of sso-session af-prod the way botocore does.
func writeSSOCache(t *testing.T, token string, expires time.Time) {
	t.Helper()
	path := ssoCachePath("af-prod")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"accessToken": token, "expiresAt": expires.UTC().Format(time.RFC3339)})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("%s never appeared", path)
}

func onlyRequest(t *testing.T) LoginRequest {
	t.Helper()
	r, ok := readRequest("af-prod")
	if !ok {
		t.Fatal("no login request was filed")
	}
	return r
}

func TestConsoleLoginWaitsForTheMembersApproval(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	fastPoll(t)
	go func() {
		waitForFile(t, requestPath("af-prod"))
		// The member approves in the Console: the token lands and the CLI accepts it.
		writeSSOCache(t, "fresh", time.Now().Add(time.Hour))
		os.WriteFile(filepath.Join(state, "loggedIn"), nil, 0o600)
	}()
	var stderr bytes.Buffer
	_, _, env, err := PlanExec(bin, workloadEnv, consoleOpts(&stderr, 5*time.Second))
	if err != nil {
		t.Fatalf("err = %v\n%s", err, stderr.String())
	}
	if envMap(env)["AWS_ACCESS_KEY_ID"] != "ASIAFAKE" {
		t.Fatal("no credentials after the Console login")
	}
	if !strings.Contains(stderr.String(), "requested in the Agent Fleet Console") {
		t.Fatalf("the run never said where the login is: %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(state, "loginArgs")); err == nil {
		t.Fatal("the CLI started a device login itself; only the member's press may")
	}
}

func TestConsoleLoginTimesOutWithTheRequestStillPending(t *testing.T) {
	bin, _ := fakeAWS(t, ssoProfile)
	fastPoll(t)
	var stderr bytes.Buffer
	_, _, _, err := PlanExec(bin, workloadEnv, consoleOpts(&stderr, 150*time.Millisecond))
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "requested in the Agent Fleet Console") {
		t.Fatalf("err = %v", err)
	}
	r := onlyRequest(t)
	if r.Profile != "prod" || len(r.Waiters) != 1 || r.Waiters[0].Session != "s1" || r.Waiters[0].Command != "terraform" {
		t.Fatalf("request = %+v", r)
	}
	// Exactly one notification, and it carries nothing but the request id.
	evs := notice.List()
	if len(evs) != 1 || evs[0].Kind != NoticeKindAWSLogin || evs[0].Payload["requestId"] != r.ID || len(evs[0].Payload) != 1 {
		t.Fatalf("notifications = %+v", evs)
	}
	// A second run joins the same request: no new id, no second notification.
	_, _, _, _ = PlanExec(bin, workloadEnv, consoleOpts(&stderr, 50*time.Millisecond))
	if again := onlyRequest(t); again.ID != r.ID || len(again.Waiters) != 2 {
		t.Fatalf("second run: %+v", again)
	}
	if n := len(notice.List()); n != 1 {
		t.Fatalf("%d notifications after a join, want 1", n)
	}
}

func TestConsoleLoginOnlyForASettingsProfileRunUnattended(t *testing.T) {
	cases := map[string]func(*ExecOptions){
		"no-login":         func(o *ExecOptions) { o.Login = "never" },
		"outside a WS":     func(o *ExecOptions) { o.ConsoleLogin = false },
		"not in Settings":  func(o *ExecOptions) { o.Settings = nil; o.Account = "123456789012" },
		"Settings differs": func(o *ExecOptions) { o.Settings = map[string]Profile{"prod": {Name: "prod", AccountID: "999"}} },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			bin, _ := fakeAWS(t, ssoProfile)
			var stderr bytes.Buffer
			o := consoleOpts(&stderr, time.Second)
			mut(&o)
			_, _, _, err := PlanExec(bin, workloadEnv, o)
			if err == nil {
				t.Fatal("ran without a login")
			}
			if _, ok := readRequest("af-prod"); ok {
				t.Fatal("filed a Console login request")
			}
		})
	}
}

func TestConsoleLoginCancelEndsTheWaitAndHoldsNewRuns(t *testing.T) {
	bin, _ := fakeAWS(t, ssoProfile)
	fastPoll(t)
	go func() {
		waitForFile(t, requestPath("af-prod"))
		r, _ := readRequest("af-prod")
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/aws-login/"+r.ID+"/cancel", nil)
		req.SetPathValue("id", r.ID)
		HandleLoginCancel(rec, req)
	}()
	var stderr bytes.Buffer
	withSettingsCache(t)
	start := time.Now()
	_, _, _, err := PlanExec(bin, workloadEnv, consoleOpts(&stderr, 5*time.Second))
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "cancelled in the Agent Fleet Console") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("the cancel did not end the wait at once")
	}
	if _, ok := readRequest("af-prod"); ok {
		t.Fatal("the cancelled request is still there")
	}
	// Within the hold a new run files nothing and says how to log in instead.
	before := len(notice.List())
	_, _, _, err = PlanExec(bin, workloadEnv, consoleOpts(&stderr, 5*time.Second))
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "aws sso login --profile 'prod'") {
		t.Fatalf("held run: %v", err)
	}
	if _, ok := readRequest("af-prod"); ok || len(notice.List()) != before {
		t.Fatal("a run during the hold filed a request")
	}
	// A login elsewhere makes the marker moot.
	writeSSOCache(t, "fresh", time.Now().Add(time.Hour))
	if _, held := liveMarker("af-prod", time.Now()); held {
		t.Fatal("the marker still holds after the cache changed")
	}
}

// A cache holding an unexpired token that AWS rejects is not a login: the request must
// stay pending, not resolve itself away (ADR 0102 decision 1).
func TestConsoleLoginDoesNotTakeARejectedTokenForALogin(t *testing.T) {
	bin, _ := fakeAWS(t, ssoProfile)
	fastPoll(t)
	writeSSOCache(t, "revoked", time.Now().Add(time.Hour))
	withSettingsCache(t)
	var stderr bytes.Buffer
	_, _, _, err := PlanExec(bin, workloadEnv, consoleOpts(&stderr, 100*time.Millisecond))
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v", err)
	}
	if got := sweepLoginRequests(time.Now()); len(got) != 1 {
		t.Fatalf("pending = %+v, want the request still there", got)
	}
}

// The snapshot is read before the check: a login that lands during the check is tried
// again, not recorded as the cache that failed.
func TestConsoleLoginRetriesWhenALoginLandsDuringTheCheck(t *testing.T) {
	bin, state := fakeAWS(t, ssoProfile)
	fastPoll(t)
	cache := ssoCachePath("af-prod")
	os.MkdirAll(filepath.Dir(cache), 0o700)
	hook := `if [ ! -f "$S/landed" ]; then touch "$S/landed"; ` +
		`printf '{"accessToken":"fresh","expiresAt":"2099-01-01T00:00:00Z"}' > '` + cache + `'; ` +
		`echo "Error loading SSO Token: expired" >&2; exit 255; fi; touch "$S/loggedIn"
`
	os.WriteFile(filepath.Join(state, "onExport"), []byte(hook), 0o600)
	var stderr bytes.Buffer
	if _, _, _, err := PlanExec(bin, workloadEnv, consoleOpts(&stderr, 2*time.Second)); err != nil {
		t.Fatalf("err = %v", err)
	}
	if _, ok := readRequest("af-prod"); ok {
		t.Fatal("filed a request although the login had landed")
	}
}

func withSettingsCache(t *testing.T) {
	t.Helper()
	t.Setenv("AF_AWS_PROFILES_TOKEN", "test-token")
	if err := saveSettingsCache([]Profile{prodSettings["prod"]}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLoginListShowsSettingsNotTheCallersText(t *testing.T) {
	bin, _ := fakeAWS(t, ssoProfile)
	withSettingsCache(t)
	var stderr bytes.Buffer
	o := consoleOpts(&stderr, 20*time.Millisecond)
	o.Waiter = LoginWaiter{Session: "s1 account 999999999999\n<b>", Command: strings.Repeat("x", 100)}
	_, _, _, _ = PlanExec(bin, workloadEnv, o)
	rec := httptest.NewRecorder()
	HandleLoginList(rec, httptest.NewRequest(http.MethodGet, "/aws-login", nil))
	var out struct {
		Requests []loginRequestWire `json:"requests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Requests) != 1 {
		t.Fatalf("list = %s", rec.Body.String())
	}
	got := out.Requests[0]
	if got.AccountID != "123456789012" || got.RoleName != "Dev" || got.Profile != "prod" {
		t.Fatalf("request = %+v", got)
	}
	w := got.Waiters[0]
	if strings.ContainsAny(w.Session, " \n<>") || len(w.Command) > 40 {
		t.Fatalf("waiter text was not cut down: %+v", w)
	}
	if strings.Contains(rec.Body.String(), "code") || strings.Contains(rec.Body.String(), "attempt") {
		t.Fatalf("the list leaks attempt details: %s", rec.Body.String())
	}
}

// The request file is writable by every agent, so the list cleans what it shows even when
// the file was written around FileLoginRequest.
func TestLoginListCleansAWaiterWrittenStraightIntoTheFile(t *testing.T) {
	fakeAWS(t, ssoProfile)
	withSettingsCache(t)
	os.MkdirAll(loginDir(), 0o700)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	raw := LoginRequest{ID: "0123456789abcdef01234567", Profile: "prod", SSOSession: "af-prod", FirstAt: now, LastAt: now,
		Waiters: []LoginWaiter{{Session: "account 999999999999 — enter code XXXX-XXXX", Command: strings.Repeat("y", 200), At: now}}}
	if err := writeJSONFile(requestPath("af-prod"), raw); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	HandleLoginList(rec, httptest.NewRequest(http.MethodGet, "/aws-login", nil))
	var out struct {
		Requests []loginRequestWire `json:"requests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Requests) != 1 || len(out.Requests[0].Waiters) != 1 {
		t.Fatalf("list = %s", rec.Body.String())
	}
	w := out.Requests[0].Waiters[0]
	if strings.ContainsAny(w.Session, " —") || len(w.Session) > 40 || len(w.Command) > 40 {
		t.Fatalf("the list showed the file's text as written: %+v", w)
	}
}

func TestDeviceURLHostIsComparedWhole(t *testing.T) {
	allowed := allowedDeviceHosts(prodSettings["prod"])
	for url, want := range map[string]bool{
		"https://device.sso.ap-northeast-1.amazonaws.com/?user_code=ABCD-EFGH": true,
		"https://example.awsapps.com/start/#/device?user_code=ABCD-EFGH":       true,
		"https://device.sso.ap-northeast-1.amazonaws.com.evil.example/":        false,
		"https://device.sso.evil.example/":                                     false,
		"https://evil.example/device.sso.ap-northeast-1.amazonaws.com":         false,
		"http://device.sso.ap-northeast-1.amazonaws.com/":                      false,
		"https://x@device.sso.ap-northeast-1.amazonaws.com/":                   false,
	} {
		if got := deviceURLAllowed(url, allowed); got != want {
			t.Errorf("%s: allowed = %v, want %v", url, got, want)
		}
	}
	cn := allowedDeviceHosts(Profile{SSORegion: "cn-north-1", StartURL: "https://x.awsapps.cn/start"})
	if cn[0] != "device.sso.cn-north-1.amazonaws.com.cn" || cn[1] != "x.awsapps.cn" {
		t.Fatalf("cn hosts = %v", cn)
	}
}

func TestDeviceURLForIssuerStartURLAdmitsOnlyThePortal(t *testing.T) {
	const ins = "ssoins-0123456789abcdef"
	check := func(sp Profile, cases map[string]bool) {
		t.Helper()
		allowed := allowedDeviceHosts(sp)
		for url, want := range cases {
			if got := deviceURLAllowed(url, allowed); got != want {
				t.Errorf("%s: allowed = %v, want %v (hosts %v)", url, got, want, allowed)
			}
		}
	}
	check(Profile{SSORegion: "ap-northeast-1", StartURL: "https://identitycenter.amazonaws.com/" + ins}, map[string]bool{
		"https://d-0123456789.awsapps.com/start/#/device?user_code=ABCD-EFGH":             true,
		"https://my-alias.awsapps.com/start/#/device?user_code=ABCD-EFGH":                 true,
		"https://" + ins + ".ap-northeast-1.portal.amazonaws.com/#/device?user_code=ABCD": true,
		"https://" + ins + ".portal.ap-northeast-1.app.aws/#/device?user_code=ABCD-EFGH":  true,
		"https://device.sso.ap-northeast-1.amazonaws.com/?user_code=ABCD-EFGH":            true,
		"https://evil-awsapps.com/start/#/device":                                         false,
		"https://awsapps.com/start/#/device":                                              false,
		"https://evil.example.awsapps.com/start/#/device":                                 false,
		"https://d-0123456789.awsapps.com.evil.example/start/#/device":                    false,
		"https://evil.example/d-0123456789.awsapps.com/start":                             false,
		"https://ssoins-other.portal.ap-northeast-1.app.aws/#/device":                     false,
		"https://" + ins + ".portal.us-east-1.app.aws/#/device":                           false,
		"https://ssoins-other.ap-northeast-1.portal.amazonaws.com/#/device":               false,
		"https://" + ins + ".us-east-1.portal.amazonaws.com/#/device":                     false,
		"https://" + ins + ".ap-northeast-1.portal.amazonaws.com.evil.example/#/device":   false,
		"https://" + ins + ".ap-northeast-1.portal.amazonaws.com.cn/#/device":             false,
		"https://start.home.awsapps.cn/directory/x":                                       false,
		"http://d-0123456789.awsapps.com/start/#/device":                                  false,
	})
	check(Profile{SSORegion: "cn-north-1", StartURL: "https://identitycenter.amazonaws.com.cn/" + ins}, map[string]bool{
		"https://start.home.awsapps.cn/directory/x/#/device?user_code=ABCD-EFGH":          true,
		"https://start.cn-north-1.home.awsapps.cn/directory/x/#/device":                   true,
		"https://" + ins + ".cn-north-1.portal.amazonaws.com.cn/#/device":                 true,
		"https://" + ins + ".portal.cn-north-1.app.amazonwebservices.com.cn/#/device":     true,
		"https://device.sso.cn-north-1.amazonaws.com.cn/?user_code=ABCD-EFGH":             true,
		"https://d-0123456789.awsapps.cn/start/#/device":                                  false,
		"https://d-0123456789.awsapps.com/start/#/device":                                 false,
		"https://" + ins + ".portal.cn-north-1.app.aws/#/device":                          false,
		"https://" + ins + ".cn-north-1.portal.amazonaws.com/#/device":                    false,
		"https://start.cn-northwest-1.home.awsapps.cn/directory/x":                        false,
		"https://ssoins-other.cn-north-1.portal.amazonaws.com.cn/#/device":                false,
		"https://" + ins + ".portal.cn-northwest-1.app.amazonwebservices.com.cn/#/device": false,
		"https://evil.start.home.awsapps.cn/":                                             false,
	})
	// A portal-form start URL keeps the exact host: another instance's portal is refused.
	portal := allowedDeviceHosts(prodSettings["prod"])
	if deviceURLAllowed("https://d-0123456789.awsapps.com/start/#/device", portal) {
		t.Fatalf("a portal start URL admitted another portal: %v", portal)
	}
	// A path that is not one label never reaches a host name.
	odd := allowedDeviceHosts(Profile{SSORegion: "ap-northeast-1", StartURL: "https://identitycenter.amazonaws.com/ssoins-1.evil.example"})
	if deviceURLAllowed("https://ssoins-1.evil.example.portal.ap-northeast-1.app.aws/", odd) || len(odd) != 3 {
		t.Fatalf("hosts = %v", odd)
	}
}

// startAttempt presses "Log in" for the pending request.
func startAttempt(t *testing.T, id string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/aws-login/"+id+"/start", nil)
	req.SetPathValue("id", id)
	HandleLoginStart(rec, req)
	var out struct {
		Attempt string `json:"attempt"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Attempt == "" {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
	return out.Attempt
}

func attemptView(t *testing.T, id, attempt string) map[string]string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/aws-login/"+id+"/attempts/"+attempt, nil)
	req.SetPathValue("id", id)
	req.SetPathValue("attempt", attempt)
	HandleLoginAttempt(rec, req)
	out := map[string]string{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func waitPhase(t *testing.T, id, attempt, phase string) map[string]string {
	t.Helper()
	var v map[string]string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if v = attemptView(t, id, attempt); v["phase"] == phase {
			return v
		}
	}
	t.Fatalf("attempt phase = %v, want %s", v, phase)
	return nil
}

// fileRequest leaves one pending request for af-prod and points the Agent at the fake CLI.
func fileRequest(t *testing.T, onLogin string) (LoginRequest, string) {
	t.Helper()
	bin, state := fakeAWS(t, ssoProfile)
	withSettingsCache(t)
	if onLogin != "" {
		os.WriteFile(filepath.Join(state, "onLogin"), []byte(onLogin), 0o600)
	}
	old := LoginAWSBin
	LoginAWSBin = func() (string, error) { return bin, nil }
	t.Cleanup(func() { LoginAWSBin = old })
	var stderr bytes.Buffer
	_, _, _, _ = PlanExec(bin, workloadEnv, consoleOpts(&stderr, 20*time.Millisecond))
	return onlyRequest(t), state
}

func TestLoginAttemptShowsItsOwnCodeAndIsReplacedByTheNext(t *testing.T) {
	// Built from the child's $HOME: fileRequest gives the test a new home.
	cache := `$HOME/.aws/sso/cache/` + filepath.Base(ssoCachePath("af-prod"))
	r, state := fileRequest(t, `echo $$ >> "$S/pids"
echo "Open https://device.sso.ap-northeast-1.amazonaws.com/?user_code=ABCD-EFGH"
echo "Then enter the code:"; echo; echo "ABCD-EFGH"
while [ ! -f "$S/approve" ]; do sleep 0.02; done
mkdir -p "$(dirname "`+cache+`")"
printf '{"accessToken":"fresh","expiresAt":"2099-01-01T00:00:00Z"}' > "`+cache+`"
`)
	first := startAttempt(t, r.ID)
	v := waitPhase(t, r.ID, first, attemptAuthorize)
	if v["code"] != "ABCD-EFGH" || !strings.HasPrefix(v["url"], "https://device.sso.ap-northeast-1.amazonaws.com/") {
		t.Fatalf("attempt = %v", v)
	}
	// A second press (anyone's) replaces the first; the first never shows another code.
	second := startAttempt(t, r.ID)
	if v := waitPhase(t, r.ID, first, attemptReplaced); v["url"] != "" || v["code"] != "" {
		t.Fatalf("the replaced attempt still shows a code: %v", v)
	}
	waitPhase(t, r.ID, second, attemptAuthorize)
	// The replaced attempt's process is really gone, not only hidden.
	pids, _ := os.ReadFile(filepath.Join(state, "pids"))
	var firstPid int
	fmt.Sscan(string(pids), &firstPid)
	if firstPid == 0 {
		t.Fatalf("the fake recorded no pid: %q", pids)
	}
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(firstPid, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the replaced attempt's process %d is still running", firstPid)
		}
	}
	if v := attemptView(t, "000000000000000000000000", second); v["phase"] != attemptGone {
		t.Fatalf("an attempt answered under another request id: %v", v)
	}
	os.WriteFile(filepath.Join(state, "approve"), nil, 0o600)
	waitPhase(t, r.ID, second, attemptDone)
	if got := sweepLoginRequests(time.Now()); len(got) != 0 {
		t.Fatalf("the request did not resolve after the login: %+v", got)
	}
}

func TestLoginAttemptRefusesAnUnexpectedSignInURL(t *testing.T) {
	r, _ := fileRequest(t, `echo "Open https://device.sso.ap-northeast-1.amazonaws.com.evil.example/?user_code=ABCD-EFGH"; sleep 5
`)
	a := startAttempt(t, r.ID)
	v := waitPhase(t, r.ID, a, attemptFailed)
	if v["url"] != "" || v["code"] != "" || v["message"] != "unexpected sign-in URL" {
		t.Fatalf("attempt = %v", v)
	}
}

func TestLoginRequestExpiresButNotUnderALiveAttempt(t *testing.T) {
	r, _ := fileRequest(t, `echo "Open https://device.sso.ap-northeast-1.amazonaws.com/?user_code=ABCD-EFGH"; sleep 5
`)
	a := startAttempt(t, r.ID)
	waitPhase(t, r.ID, a, attemptAuthorize)
	later := time.Now().Add(loginRequestTTL + time.Minute)
	if got := sweepLoginRequests(later); len(got) != 1 {
		t.Fatal("the request expired under a live attempt")
	}
	loginAttempts.Lock()
	cur := loginAttempts.current["af-prod"]
	loginAttempts.Unlock()
	cur.end(attemptFailed, "")
	if got := sweepLoginRequests(later); len(got) != 0 {
		t.Fatal("the request did not expire")
	}
}

// exportedProd stands in for the sync the row's press runs: prod is in Settings and in
// the managed block.
func exportedProd(t *testing.T) {
	t.Helper()
	old := syncForLogin
	syncForLogin = func() (SyncResult, error) {
		return SyncResult{Settings: prodSettings, Exported: []string{"prod"}, Fetched: true}, nil
	}
	t.Cleanup(func() { syncForLogin = old })
}

func profileStart(name string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/aws-login/profiles/"+name+"/start", nil)
	req.SetPathValue("name", name)
	HandleProfileLoginStart(rec, req)
	return rec
}

func startProfileAttempt(t *testing.T, name string) string {
	t.Helper()
	rec := profileStart(name)
	var out struct {
		Attempt string `json:"attempt"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Attempt == "" {
		t.Fatalf("profile start = %d %s", rec.Code, rec.Body.String())
	}
	return out.Attempt
}

func profileAttemptView(name, attempt string) map[string]string {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/aws-login/profiles/"+name+"/attempts/"+attempt, nil)
	req.SetPathValue("name", name)
	req.SetPathValue("attempt", attempt)
	HandleProfileLoginAttempt(rec, req)
	out := map[string]string{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func waitProfilePhase(t *testing.T, name, attempt, phase string) map[string]string {
	t.Helper()
	var v map[string]string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if v = profileAttemptView(name, attempt); v["phase"] == phase {
			return v
		}
	}
	t.Fatalf("profile attempt phase = %v, want %s", v, phase)
	return nil
}

func TestProfileLoginRunsUnderACancelHoldAndSettlesIt(t *testing.T) {
	cache := `$HOME/.aws/sso/cache/` + filepath.Base(ssoCachePath("af-prod"))
	r, state := fileRequest(t, `echo "Open https://device.sso.ap-northeast-1.amazonaws.com/?user_code=WXYZ-1234"
echo "Then enter the code:"; echo; echo "WXYZ-1234"
while [ ! -f "$S/approve" ]; do sleep 0.02; done
mkdir -p "$(dirname "`+cache+`")"
printf '{"accessToken":"fresh","expiresAt":"2099-01-01T00:00:00Z"}' > "`+cache+`"
`)
	exportedProd(t)
	rec := httptest.NewRecorder()
	creq := httptest.NewRequest(http.MethodPost, "/aws-login/"+r.ID+"/cancel", nil)
	creq.SetPathValue("id", r.ID)
	HandleLoginCancel(rec, creq)
	if _, ok := liveMarker("af-prod", time.Now()); !ok {
		t.Fatal("the cancel left no hold to test against")
	}

	a := startProfileAttempt(t, "prod")
	v := waitProfilePhase(t, "prod", a, attemptAuthorize)
	if v["code"] != "WXYZ-1234" {
		t.Fatalf("profile attempt = %v", v)
	}
	// Neither route reads the other's code: not the request route, not another name.
	if v := attemptView(t, r.ID, a); v["phase"] != attemptGone {
		t.Fatalf("the request route answered for a row's attempt: %v", v)
	}
	if v := profileAttemptView("other", a); v["phase"] != attemptGone {
		t.Fatalf("another profile name answered for prod's attempt: %v", v)
	}
	os.WriteFile(filepath.Join(state, "approve"), nil, 0o600)
	waitProfilePhase(t, "prod", a, attemptDone)
	if _, ok := liveMarker("af-prod", time.Now()); ok {
		t.Fatal("the login did not void the cancel hold")
	}
}

func TestProfileLoginSharesTheAttemptSlotAndOutlivesACancel(t *testing.T) {
	r, _ := fileRequest(t, `echo "Open https://device.sso.ap-northeast-1.amazonaws.com/?user_code=ABCD-EFGH"; sleep 5
`)
	exportedProd(t)
	fromToast := startAttempt(t, r.ID)
	waitPhase(t, r.ID, fromToast, attemptAuthorize)
	if v := profileAttemptView("prod", fromToast); v["phase"] != attemptGone {
		t.Fatalf("the row route answered for a request's attempt: %v", v)
	}
	fromRow := startProfileAttempt(t, "prod")
	waitPhase(t, r.ID, fromToast, attemptReplaced)
	waitProfilePhase(t, "prod", fromRow, attemptAuthorize)

	rec := httptest.NewRecorder()
	creq := httptest.NewRequest(http.MethodPost, "/aws-login/"+r.ID+"/cancel", nil)
	creq.SetPathValue("id", r.ID)
	HandleLoginCancel(rec, creq)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	if v := profileAttemptView("prod", fromRow); v["phase"] != attemptAuthorize {
		t.Fatalf("cancelling the request ended the row's login: %v", v)
	}
	loginAttempts.Lock()
	cur := loginAttempts.byID[fromRow]
	loginAttempts.Unlock()
	cur.end(attemptFailed, "")
}

func TestProfileLoginRefusesWhatWasNotExported(t *testing.T) {
	bin, _ := fakeAWS(t, ssoProfile)
	old := LoginAWSBin
	LoginAWSBin = func() (string, error) { return bin, nil }
	t.Cleanup(func() { LoginAWSBin = old })
	shadowed := prodSettings["prod"]
	shadowed.Name, shadowed.Label = "mine", "mine"
	half := prodSettings["prod"]
	half.Name, half.Label, half.RoleName = "half", "half", ""
	oldSync := syncForLogin
	syncForLogin = func() (SyncResult, error) {
		return SyncResult{Settings: map[string]Profile{"prod": prodSettings["prod"], "mine": shadowed, "half": half},
			Exported: []string{"prod"}, Shadowed: []string{"mine"},
			Incomplete: map[string]string{"half": IncompleteReason(half)}, Fetched: true}, nil
	}
	t.Cleanup(func() { syncForLogin = oldSync })
	// The code is what the Console words the refusal by.
	for name, want := range map[string]string{"nope": "not_a_settings_profile", "mine": "not_exported", "half": "incomplete_profile"} {
		rec := profileStart(name)
		if rec.Code < 400 || !strings.Contains(rec.Body.String(), `"`+want+`"`) {
			t.Errorf("start %s = %d %s, want %s", name, rec.Code, rec.Body.String(), want)
		}
	}
	loginAttempts.Lock()
	defer loginAttempts.Unlock()
	for _, s := range []string{"af-nope", "af-mine", "af-half"} {
		if loginAttempts.current[s] != nil {
			t.Errorf("a refused press started an attempt for %s", s)
		}
	}
}

func TestProfileLoginStatesSayNoMoreThanTheCacheKnows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withSettingsCache(t)
	states := func() string {
		rec := httptest.NewRecorder()
		HandleProfileLoginStates(rec, httptest.NewRequest(http.MethodGet, "/aws-login/profiles", nil))
		var out profileLoginStatesWire
		if json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Profiles) != 1 || out.Profiles[0].Name != "prod" {
			t.Fatalf("states = %s", rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "secret-") {
			t.Fatalf("a token left the Agent: %s", rec.Body.String())
		}
		return out.Profiles[0].State
	}
	if s := states(); s != loginStateNone {
		t.Fatalf("no cache: %s", s)
	}
	writeSSOCache(t, "secret-access", time.Now().Add(time.Hour))
	if s := states(); s != loginStateSignedIn {
		t.Fatalf("unexpired token: %s", s)
	}
	writeSSOCache(t, "secret-access", time.Now().Add(-time.Minute))
	if s := states(); s != loginStateNone {
		t.Fatalf("expired, nothing to renew with: %s", s)
	}
	// The access token lasts about an hour and the CLI renews it: expired is not logged out.
	b, _ := json.Marshal(map[string]string{"accessToken": "secret-access", "refreshToken": "secret-refresh",
		"expiresAt": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)})
	os.WriteFile(ssoCachePath("af-prod"), b, 0o600)
	if s := states(); s != loginStateRenew {
		t.Fatalf("expired with a refresh token: %s", s)
	}
}

func TestProfileLoginNeedsAFreshWrittenSettingsList(t *testing.T) {
	bin, _ := fakeAWS(t, ssoProfile)
	old := LoginAWSBin
	LoginAWSBin = func() (string, error) { return bin, nil }
	t.Cleanup(func() { LoginAWSBin = old })
	oldSync := syncForLogin
	t.Cleanup(func() { syncForLogin = oldSync })
	for label, fn := range map[string]func() (SyncResult, error){
		// The CP cannot be asked: the cache may predate the row that was pressed.
		"cp down": func() (SyncResult, error) {
			return SyncResult{Settings: prodSettings, Exported: []string{"prod"}, FromCache: true}, errors.New("CP unreachable")
		},
		// Fetched, but ~/.aws/config was not written: Exported names a block that is not there.
		"write failed": func() (SyncResult, error) {
			return SyncResult{Settings: prodSettings, Exported: []string{"prod"}, Fetched: true}, errors.New("read-only file system")
		},
		"bridge off": func() (SyncResult, error) { return SyncResult{}, ErrBridgeOff },
	} {
		syncForLogin = fn
		rec := profileStart("prod")
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"settings_unavailable"`) {
			t.Errorf("%s: start = %d %s", label, rec.Code, rec.Body.String())
		}
	}
}

func TestARowLoginDoesNotKeepARequestPastItsTTL(t *testing.T) {
	r, _ := fileRequest(t, `echo "Open https://device.sso.ap-northeast-1.amazonaws.com/?user_code=ABCD-EFGH"; sleep 5
`)
	exportedProd(t)
	a := startProfileAttempt(t, "prod")
	waitProfilePhase(t, "prod", a, attemptAuthorize)
	later := time.Now().Add(loginRequestTTL + time.Minute)
	if got := sweepLoginRequests(later); len(got) != 0 {
		t.Fatalf("a row's login kept request %s past its TTL", r.ID)
	}
	loginAttempts.Lock()
	cur := loginAttempts.byID[a]
	loginAttempts.Unlock()
	cur.end(attemptFailed, "")
}
