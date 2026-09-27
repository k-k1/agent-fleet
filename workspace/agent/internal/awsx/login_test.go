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
