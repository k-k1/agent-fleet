package gcpx

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudlogin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// fakeLoginDirEnv makes the test binary act as gcloud's `auth login` (TestMain): it is
// started by the Console login exactly as the real gcloud would be, in its own process
// group, with the clean environment, and reads the code from stdin.
const fakeLoginDirEnv = "GCPX_FAKE_LOGIN_DIR"

// fakeGcloudLogin behaves like `gcloud auth login [<account>] --no-launch-browser
// --configuration <name> [--force]` of SDK 587.0.0 as far as the Console login sees it.
// Files in dir steer it; it never writes the code anywhere but its hash.
func fakeGcloudLogin(dir string, args []string) int {
	rd := func(n string) string {
		b, _ := os.ReadFile(filepath.Join(dir, n))
		return strings.TrimSpace(string(b))
	}
	has := func(n string) bool { _, err := os.Stat(filepath.Join(dir, n)); return err == nil }
	appendTo := func(path, line string) {
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString(line + "\n")
			f.Close()
		}
	}
	cfg := os.Getenv("CLOUDSDK_CONFIG")
	// Real gcloud writes a DEBUG log under the config root unless file logging is off,
	// including the URL and the code exchange (ADR 0107 decision 1, measured).
	gcloudLog := func(s string) {
		if os.Getenv("CLOUDSDK_CORE_DISABLE_FILE_LOGGING") != "true" {
			appendTo(filepath.Join(cfg, "logs", "gcloud.log"), s)
		}
	}
	// The verification mint after a login (finishLogin): config-helper with the token.
	if len(args) >= 2 && args[0] == "config" && args[1] == "config-helper" {
		// Like gcloud, the configuration's auth/impersonate_service_account applies unless
		// the flag or the environment variable (even empty) overrides it.
		imp, envSet := os.LookupEnv("CLOUDSDK_AUTH_IMPERSONATE_SERVICE_ACCOUNT")
		if !envSet {
			name := ""
			for i, a := range args {
				if a == "--configuration" && i+1 < len(args) {
					name = args[i+1]
				}
			}
			props, _ := readProps(filepath.Join(cfg, "configurations", "config_"+name))
			imp = props["auth/impersonate_service_account"]
		}
		for i, a := range args {
			if a == "--impersonate-service-account" && i+1 < len(args) {
				imp = args[i+1]
			}
		}
		appendTo(filepath.Join(dir, "mints"), strings.Join(args, " ")+" impersonating="+imp)
		if imp != "" && has("impersonate-denied") {
			fmt.Fprintln(os.Stderr, "ERROR: (gcloud.config.config-helper) PERMISSION_DENIED: Permission 'iam.serviceAccounts.getAccessToken' denied on "+imp)
			return 1
		}
		if f := rd("mint-fail"); f != "" {
			fmt.Fprintln(os.Stderr, f)
			return 1
		}
		fmt.Printf(`{"credential":{"access_token":%q,"token_expiry":%q}}`+"\n", rd("access"),
			time.Now().Add(55*time.Minute).UTC().Format(time.RFC3339))
		return 0
	}
	appendTo(filepath.Join(dir, "login-args"), strings.Join(args, " "))
	appendTo(filepath.Join(dir, "login-env"), "CLOUDSDK_CONFIG="+cfg+
		" CHECK_GCE="+os.Getenv("CLOUDSDK_CORE_CHECK_GCE_METADATA")+" NOLOG="+os.Getenv("CLOUDSDK_CORE_DISABLE_FILE_LOGGING")+
		" PGID_SELF="+strconv.FormatBool(pgidIsSelf()))
	if len(args) < 2 || args[0] != "auth" || args[1] != "login" {
		fmt.Fprintln(os.Stderr, "fake gcloud: unexpected", args)
		return 9
	}
	acct, cfgName, force := "", "", false
	if len(args) > 2 && !strings.HasPrefix(args[2], "--") {
		acct = args[2]
	}
	for i, a := range args {
		if a == "--configuration" && i+1 < len(args) {
			cfgName = args[i+1]
		}
		force = force || a == "--force"
	}
	in := bufio.NewReader(os.Stdin)
	if has("prompt") {
		fmt.Fprint(os.Stderr, "You are running on a Google Compute Engine virtual machine.\n\nDo you want to continue (Y/n)?  ")
		line, _ := in.ReadString('\n')
		appendTo(filepath.Join(dir, "prompt-answer"), strconv.Quote(line))
		return 0
	}
	if has("reuse") && !force && acct != "" {
		fmt.Fprintf(os.Stderr, "Re-using locally stored credentials for [%s]. To fetch new credentials, re-run the command with the `--force` flag.\n", acct)
		setFakeAccount(cfg, cfgName, acct)
		return 0
	}
	u := rd("url")
	fmt.Fprintf(os.Stderr, "Go to the following link in your browser, and complete the sign-in prompts:\n\n    %s\n\n"+
		"Once finished, enter the verification code provided in your browser: ", u)
	gcloudLog(u)
	line, err := in.ReadString('\n')
	if err != nil {
		fmt.Fprintln(os.Stderr, "\nERROR: (gcloud.auth.login) no verification code was entered")
		return 1
	}
	code := strings.TrimSpace(line)
	gcloudLog(code)
	sum := sha256.Sum256([]byte(code))
	appendTo(filepath.Join(dir, "codes"), hex.EncodeToString(sum[:]))
	if want := rd("code-sha"); want != "" && want != hex.EncodeToString(sum[:]) {
		fmt.Fprintln(os.Stderr, "ERROR: (gcloud.auth.login) invalid_grant: Malformed auth code.")
		return 1
	}
	if acct == "" {
		acct = rd("login-account")
	}
	// "slow" stretches the exchange so a test can look at the lock while gcloud still runs.
	if has("slow") {
		time.Sleep(300 * time.Millisecond)
	}
	// gcloud asks before it overwrites a stored credential; stdin is closed by then, so the
	// default (yes) is taken.
	fmt.Fprint(os.Stderr, "Do you wish to proceed and overwrite existing credentials?\n\nDo you want to continue (Y/n)?  ")
	rest, _ := in.ReadString('\n')
	appendTo(filepath.Join(dir, "after-code"), strconv.Quote(rest))
	if has("slow") {
		time.Sleep(1500 * time.Millisecond)
	}
	refresh := rd("refresh")
	db, err := sql.Open("sqlite", "file:"+filepath.Join(cfg, "credentials.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return 7
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS credentials (account_id TEXT PRIMARY KEY, value BLOB)`); err != nil {
		return 7
	}
	v, _ := json.Marshal(map[string]string{"type": "authorized_user", "refresh_token": refresh})
	if _, err := db.Exec(`INSERT OR REPLACE INTO credentials VALUES (?, ?)`, acct, string(v)); err != nil {
		return 7
	}
	gcloudLog("stored " + refresh)
	setFakeAccount(cfg, cfgName, acct)
	fmt.Fprintf(os.Stderr, "\nYou are now logged in as [%s].\n", acct)
	return 0
}

func pgidIsSelf() bool {
	pg, err := syscall.Getpgid(0)
	return err == nil && pg == os.Getpid()
}

// setFakeAccount writes core/account the way gcloud does: the file in its own layout.
func setFakeAccount(cfg, name, acct string) {
	path := filepath.Join(cfg, "configurations", "config_"+name)
	props, _ := readProps(path)
	props["core/account"] = acct
	var b strings.Builder
	sections := map[string][]string{}
	for k, v := range props {
		sec, key, _ := strings.Cut(k, "/")
		sections[sec] = append(sections[sec], key+" = "+v)
	}
	for sec, lines := range sections {
		b.WriteString("[" + sec + "]\n" + strings.Join(lines, "\n") + "\n\n")
	}
	_ = os.WriteFile(path, []byte(b.String()), 0o600)
}

// syncBuf collects the Agent's log while a test runs.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// loginEnv is one Console-login test: the gcpx env, a fake CP serving the profiles, the
// fake login's directory, the Agent's routes and its log.
type loginEnv struct {
	*env
	dir     string
	mux     *http.ServeMux
	log     *syncBuf
	mu      sync.Mutex
	ps      []Profile
	url     string // the synthetic sign-in URL
	code    string // the synthetic verification code
	refresh string // the synthetic refresh token the login stores
}

func (l *loginEnv) setProfiles(ps ...Profile) {
	l.mu.Lock()
	l.ps = ps
	l.mu.Unlock()
}

func syntheticURL(t *testing.T) string {
	return "https://" + signInHost + "/o/oauth2/auth?response_type=code&client_id=" + randHex(t, 8) +
		".apps.googleusercontent.com&redirect_uri=" + url.QueryEscape(signInRedirect) +
		"&scope=openid&code_challenge=" + randHex(t, 16) + "&code_challenge_method=S256&state=" + randHex(t, 8)
}

func setupLogin(t *testing.T, ps ...Profile) *loginEnv {
	t.Helper()
	l := &loginEnv{env: setup(t), dir: t.TempDir(), log: &syncBuf{}}
	l.setProfiles(ps...)
	tok := "afg_" + randHex(t, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/gcp-profiles" || r.Header.Get("Authorization") != "Bearer "+tok {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"profiles": l.ps, "conflicts": []Conflict{}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_GCP_PROFILES_TOKEN", tok)
	t.Setenv(fakeLoginDirEnv, l.dir)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	old := LoginGCloudBin
	LoginGCloudBin = func() (string, error) { return self, nil }
	oldLog := log.Writer()
	log.SetOutput(l.log)
	oldPoll := loginPollInterval
	loginPollInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		LoginGCloudBin = old
		log.SetOutput(oldLog)
		loginPollInterval = oldPoll
	})
	// Every attempt this test started must be gone with its process before the next test.
	t.Cleanup(func() { endAllAttempts(t) })
	l.url, l.code, l.refresh = syntheticURL(t), "4/0"+randHex(t, 30), "1//"+randHex(t, 24)
	l.put(t, "url", l.url)
	l.put(t, "refresh", l.refresh)
	l.put(t, "access", l.token)
	l.put(t, "login-account", "picked@example.com")
	sum := sha256.Sum256([]byte(l.code))
	l.put(t, "code-sha", hex.EncodeToString(sum[:]))
	if _, err := Sync(); err != nil {
		t.Fatal(err)
	}
	l.mux = http.NewServeMux()
	l.mux.HandleFunc("GET /gcp-login", HandleLoginList)
	l.mux.HandleFunc("POST /gcp-login/{id}/start", HandleLoginStart)
	l.mux.HandleFunc("POST /gcp-login/{id}/cancel", HandleLoginCancel)
	l.mux.HandleFunc("GET /gcp-login/profiles", HandleProfileLoginStates)
	l.mux.HandleFunc("POST /gcp-login/profiles/{name}/start", HandleProfileLoginStart)
	l.mux.HandleFunc("GET /gcp-login/profiles/{name}/attempts/{attempt}", HandleProfileLoginAttempt)
	l.mux.HandleFunc("POST /gcp-login/profiles/{name}/attempts/{attempt}/code", HandleProfileLoginCode)
	return l
}

// endAllAttempts ends every attempt still registered and waits for its process.
func endAllAttempts(t *testing.T) {
	attemptsMu.Lock()
	ids := make([]string, 0, len(attempts))
	for id := range attempts {
		ids = append(ids, id)
	}
	attemptsMu.Unlock()
	for _, id := range ids {
		if a := logins.Attempt(id); a != nil {
			a.End(cloudlogin.PhaseCancelled, "")
			waitExited(t, a)
		}
	}
}

func waitExited(t *testing.T, a *cloudlogin.Attempt) {
	t.Helper()
	ch := a.Exited()
	if ch == nil {
		return
	}
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("the login process outlived its attempt")
	}
}

func (l *loginEnv) put(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(l.dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (l *loginEnv) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-AF-Relay", "cp")
	rec := httptest.NewRecorder()
	// Through the Agent's own access log, as main serves the routes.
	httpx.LogRequests(l.mux).ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (l *loginEnv) start(t *testing.T, path string) string {
	t.Helper()
	code, out := l.do(t, "POST", path, "")
	if code != http.StatusOK || out["attempt"] == "" {
		t.Fatalf("start %s: %d %v", path, code, out)
	}
	return out["attempt"].(string)
}

// waitPhase polls the attempt until it reaches phase.
func (l *loginEnv) waitPhase(t *testing.T, name, id, phase string) map[string]any {
	t.Helper()
	var out map[string]any
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		_, out = l.do(t, "GET", "/gcp-login/profiles/"+name+"/attempts/"+id, "")
		if out["phase"] == phase {
			return out
		}
	}
	t.Fatalf("attempt never reached %s: %v", phase, out)
	return nil
}

func (l *loginEnv) submit(t *testing.T, name, id, code string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(codeWire{Code: code})
	return l.do(t, "POST", "/gcp-login/profiles/"+name+"/attempts/"+id+"/code", string(b))
}

func noAccount() Profile {
	p := prod()
	p.Account = ""
	return p
}

// noSecretAnywhere greps every captured output for the synthetic code, refresh token,
// access token and URL: the Agent's log, the wrapper's stderr, the notification outbox,
// the request files, and every file under the Agent's state directory except the
// credential store itself (which is where a login is supposed to put the refresh token).
func (l *loginEnv) noSecretAnywhere(t *testing.T, extra ...string) {
	t.Helper()
	secrets := map[string]string{"code": l.code, "refresh token": l.refresh, "access token": l.token, "URL": l.url}
	outputs := map[string]string{"agent log": l.log.String()}
	for i, x := range extra {
		outputs[fmt.Sprintf("output %d", i)] = x
	}
	for _, ev := range notice.List() {
		b, _ := json.Marshal(ev)
		outputs["notice "+ev.Kind] = string(b)
	}
	scanned := 0
	_ = filepath.WalkDir(paths.AgentStateDir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(p) == "credentials.db" {
			return nil
		}
		// The run's private token file is where the wrapper hands the token to its command.
		if rel, _ := filepath.Rel(ExecDir(), p); filepath.Base(p) == "token" && !strings.HasPrefix(rel, "..") {
			return nil
		}
		b, _ := os.ReadFile(p)
		outputs[p] = string(b)
		scanned++
		return nil
	})
	if scanned == 0 {
		t.Fatal("scanned no file under the Agent's state directory")
	}
	for what, s := range secrets {
		for where, out := range outputs {
			if strings.Contains(out, s) {
				t.Errorf("the %s appears in %s", what, where)
			}
		}
	}
}

func TestSignInURLCheck(t *testing.T) {
	good := syntheticURL(t)
	if u, err := signInURL("Go to:\n\n    " + good + "\n\nOnce finished"); err != nil || u != good {
		t.Fatalf("a good URL: %q %v", u, err)
	}
	// Not yet complete: the URL may still be arriving.
	if u, err := signInURL("Go to:\n\n    " + good); u != "" || err != nil {
		t.Fatalf("a half-read URL was taken: %q %v", u, err)
	}
	redir := "&redirect_uri=" + url.QueryEscape(signInRedirect)
	bad := map[string]string{
		"http":             "http://accounts.google.com/o/oauth2/auth?x=1" + redir,
		"other host":       "https://accounts.google.com.evil.example/o/oauth2/auth?x=1" + redir,
		"subdomain":        "https://evil.accounts.google.com/o/oauth2/auth?x=1" + redir,
		"port":             "https://accounts.google.com:8443/o/oauth2/auth?x=1" + redir,
		"userinfo":         "https://user@accounts.google.com/o/oauth2/auth?x=1" + redir,
		"fragment":         "https://accounts.google.com/o/oauth2/auth?x=1" + redir + "#frag",
		"no redirect":      "https://accounts.google.com/o/oauth2/auth?x=1",
		"two redirects":    "https://accounts.google.com/o/oauth2/auth?x=1" + redir + redir,
		"other redirect":   "https://accounts.google.com/o/oauth2/auth?x=1&redirect_uri=" + url.QueryEscape("https://evil.example/authcode.html"),
		"redirect variant": "https://accounts.google.com/o/oauth2/auth?x=1&redirect_uri=" + url.QueryEscape(signInRedirect+"?x"),
		"upper host":       "https://ACCOUNTS.google.com/o/oauth2/auth?x=1" + redir,
		"bad query":        "https://accounts.google.com/o/oauth2/auth?x=1;y=2" + redir,
	}
	for name, u := range bad {
		if got, err := signInURL("Go to:\n\n    " + u + "\n"); err == nil || got != "" || err.Error() != msgUnexpectedURL {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
	// A question before the URL ends the attempt; one after it (overwrite a stored
	// credential, answered by the closed stdin) does not.
	if _, err := signInURL("Do you want to continue (Y/n)?  "); err == nil || err.Error() != msgPrompt {
		t.Errorf("prompt first: %v", err)
	}
	if u, err := signInURL("Go to:\n\n    " + good + "\n\nDo you want to continue (Y/n)?  "); err != nil || u != good {
		t.Errorf("prompt after the URL: %q %v", u, err)
	}
}

// TestConsoleLoginEndToEnd: an unattended run files a request (notice with the id only),
// the member starts the login from the toast, the tab that pressed reads the URL, posts the
// code once, and the waiting run gets its token. No secret reaches any output.
func TestConsoleLoginEndToEnd(t *testing.T) {
	l := setupLogin(t, prod())
	o := execOpts(l.env, prod())
	o.Login, o.ConsoleLogin, o.ConsoleWait = "auto", true, 20*time.Second
	o.Waiter = cloudlogin.Waiter{Session: "s1", Command: "terraform"}
	stderr := &syncBuf{}
	o.Stderr = stderr
	type result struct {
		env []string
		err error
	}
	done := make(chan result, 1)
	go func() {
		_, _, env, err := PlanExec(l.gcloud, hostile(t), o)
		done <- result{env, err}
	}()
	waitForFile(t, logins.RequestPath(ConfigName("prod")))

	evs := notice.List()
	if len(evs) != 1 || evs[0].Kind != NoticeKindGCPLogin || len(evs[0].Payload) != 1 || evs[0].Payload["requestId"] == "" {
		t.Fatalf("notifications = %+v", evs)
	}
	reqID := evs[0].Payload["requestId"].(string)
	code, list := l.do(t, "GET", "/gcp-login", "")
	reqs, _ := list["requests"].([]any)
	if code != 200 || len(reqs) != 1 || reqs[0].(map[string]any)["id"] != reqID || reqs[0].(map[string]any)["relogin"] == true {
		t.Fatalf("list = %d %v", code, list)
	}
	if strings.Contains(fmt.Sprint(list), "attempt") {
		t.Fatalf("the list carries an attempt: %v", list)
	}

	id := l.start(t, "/gcp-login/"+reqID+"/start")
	view := l.waitPhase(t, "prod", id, cloudlogin.PhaseAuthorize)
	if view["url"] != l.url || view["code"] != nil {
		t.Fatalf("view = %v", view)
	}
	// The same attempt read through another profile's route is gone; a code through it is refused.
	if _, out := l.do(t, "GET", "/gcp-login/profiles/other/attempts/"+id, ""); out["phase"] != cloudlogin.PhaseGone {
		t.Fatalf("another profile read the attempt: %v", out)
	}
	if c, _ := l.submit(t, "other", id, l.code); c != http.StatusNotFound {
		t.Fatalf("code through another profile: %d", c)
	}
	if c, _ := l.submit(t, "prod", id, "two\nlines"); c != http.StatusBadRequest {
		t.Fatalf("a code with a line break: %d", c)
	}
	if c, _ := l.do(t, "POST", "/gcp-login/profiles/prod/attempts/"+id+"/code", `{"code":"`+strings.Repeat("a", 5000)+`"}`); c != http.StatusBadRequest {
		t.Fatalf("an oversized body: %d", c)
	}
	if c, out := l.submit(t, "prod", id, l.code); c != http.StatusOK {
		t.Fatalf("submit: %d %v", c, out)
	}
	if c, _ := l.submit(t, "prod", id, l.code); c != http.StatusConflict {
		t.Fatalf("a second code: %d", c)
	}
	l.waitPhase(t, "prod", id, cloudlogin.PhaseDone)

	var res result
	select {
	case res = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the waiting run never finished")
	}
	if res.err != nil {
		t.Fatalf("run: %v\n%s", res.err, stderr.String())
	}
	if envMap(res.env)["GOOGLE_OAUTH_ACCESS_TOKEN"] != l.token {
		t.Fatal("the run got no token after the Console login")
	}
	if !strings.Contains(stderr.String(), "requested in the Agent Fleet Console") {
		t.Fatalf("the run never said where the login is: %q", stderr.String())
	}
	if codes, _ := os.ReadFile(filepath.Join(l.dir, "codes")); strings.Count(string(codes), "\n") != 1 {
		t.Fatalf("gcloud received %d codes", strings.Count(string(codes), "\n"))
	}
	if after, _ := os.ReadFile(filepath.Join(l.dir, "after-code")); strings.TrimSpace(string(after)) != `""` {
		t.Fatalf("stdin carried more than the code: %s", after)
	}
	env, _ := os.ReadFile(filepath.Join(l.dir, "login-env"))
	if !strings.Contains(string(env), "CLOUDSDK_CONFIG="+ConfigRoot()+" CHECK_GCE=false NOLOG=true PGID_SELF=true") {
		t.Fatalf("login environment: %s", env)
	}
	args, _ := os.ReadFile(filepath.Join(l.dir, "login-args"))
	if strings.TrimSpace(string(args)) != "auth login dev@example.com --no-launch-browser --configuration af-prod" {
		t.Fatalf("login args: %s", args)
	}
	// The request is resolved, and the log names the profile and the attempt's reference.
	if pending := logins.Sweep(time.Now()); len(pending) != 0 {
		t.Fatal("the request outlived the login")
	}
	if lg := l.log.String(); !strings.Contains(lg, "gcp-login: code profile=prod attempt="+AttemptRef(id)+" relayed=true") ||
		strings.Contains(lg, id) {
		t.Fatalf("agent log: %s", lg)
	}
	l.noSecretAnywhere(t, stderr.String())
}

// TestCodeRefusedForAnyOtherAttempt: replaced, cancelled, already used, unknown, other
// profile, and an attempt that never showed a URL all refuse a code, and gcloud gets none.
func TestCodeRefusedForAnyOtherAttempt(t *testing.T) {
	other := noAccount()
	other.ID, other.Name, other.Label = "id-2", "other", "Other"
	l := setupLogin(t, prod(), other)

	first := l.start(t, "/gcp-login/profiles/prod/start")
	l.waitPhase(t, "prod", first, cloudlogin.PhaseAuthorize)
	second := l.start(t, "/gcp-login/profiles/prod/start")
	l.waitPhase(t, "prod", second, cloudlogin.PhaseAuthorize)
	if _, out := l.do(t, "GET", "/gcp-login/profiles/prod/attempts/"+first, ""); out["phase"] != cloudlogin.PhaseReplaced || out["url"] != nil {
		t.Fatalf("first after a second press: %v", out)
	}
	if c, _ := l.submit(t, "prod", first, l.code); c != http.StatusNotFound && c != http.StatusConflict {
		t.Fatalf("code to a replaced attempt: %d", c)
	}
	// Another profile's attempt cannot take this one's code.
	otherID := l.start(t, "/gcp-login/profiles/other/start")
	l.waitPhase(t, "other", otherID, cloudlogin.PhaseAuthorize)
	if c, _ := l.submit(t, "prod", otherID, l.code); c != http.StatusNotFound {
		t.Fatalf("code to another profile's attempt: %d", c)
	}
	if c, _ := l.submit(t, "prod", "0123456789abcdef01234567", l.code); c != http.StatusNotFound {
		t.Fatalf("code to an unknown attempt: %d", c)
	}
	// Cancelled.
	a := logins.Attempt(second)
	a.End(cloudlogin.PhaseCancelled, "")
	waitExited(t, a)
	if c, _ := l.submit(t, "prod", second, l.code); c != http.StatusNotFound && c != http.StatusConflict {
		t.Fatalf("code to a cancelled attempt: %d", c)
	}
	if codes, _ := os.ReadFile(filepath.Join(l.dir, "codes")); len(codes) != 0 {
		t.Fatalf("a refused code reached gcloud: %s", codes)
	}
	// Already used: the first code is taken, the second refused.
	third := l.start(t, "/gcp-login/profiles/prod/start")
	l.waitPhase(t, "prod", third, cloudlogin.PhaseAuthorize)
	if c, _ := l.submit(t, "prod", third, l.code); c != http.StatusOK {
		t.Fatalf("code: %d", c)
	}
	if c, _ := l.submit(t, "prod", third, l.code); c != http.StatusConflict && c != http.StatusNotFound {
		t.Fatalf("second code: %d", c)
	}
	l.waitPhase(t, "prod", third, cloudlogin.PhaseDone)
	if codes, _ := os.ReadFile(filepath.Join(l.dir, "codes")); strings.Count(string(codes), "\n") != 1 {
		t.Fatalf("gcloud received %d codes", strings.Count(string(codes), "\n"))
	}
	l.noSecretAnywhere(t)
}

// TestCodeRefusedBeforeTheURL: an attempt still starting is not waiting for a code.
func TestCodeRefusedBeforeTheURL(t *testing.T) {
	l := setupLogin(t, prod())
	l.put(t, "url", "") // prints no URL at all; waits on stdin
	id := l.start(t, "/gcp-login/profiles/prod/start")
	time.Sleep(200 * time.Millisecond)
	if _, out := l.do(t, "GET", "/gcp-login/profiles/prod/attempts/"+id, ""); out["phase"] != cloudlogin.PhaseStarting {
		t.Fatalf("phase: %v", out)
	}
	if c, _ := l.submit(t, "prod", id, l.code); c != http.StatusConflict {
		t.Fatalf("code before the URL: %d", c)
	}
}

// TestNoURLEndsTheAttempt: an attempt holds the root's lock until its URL is out, so one that
// prints none ends after urlWait and lets go, and af-gcloud-exec runs are not stalled.
func TestNoURLEndsTheAttempt(t *testing.T) {
	l := setupLogin(t, prod())
	old := urlWait
	urlWait = 300 * time.Millisecond
	t.Cleanup(func() { urlWait = old })
	l.put(t, "url", "")
	id := l.start(t, "/gcp-login/profiles/prod/start")
	if _, _, err := lockRootNonBlocking(); !errors.Is(err, errRootBusy) {
		t.Fatalf("the attempt does not hold the lock before its URL: %v", err)
	}
	if out := l.waitPhase(t, "prod", id, cloudlogin.PhaseFailed); out["message"] != msgNoURL {
		t.Fatalf("view: %v", out)
	}
	waitExited(t, logins.Attempt(id))
	_, unlock, err := lockRootNonBlocking()
	if err != nil {
		t.Fatalf("the lock outlived the attempt: %v", err)
	}
	unlock()
}

func TestUnexpectedURLOrPromptEndsTheAttempt(t *testing.T) {
	l := setupLogin(t, prod())
	l.put(t, "url", strings.Replace(l.url, signInHost, "accounts.google.com.evil.example", 1))
	id := l.start(t, "/gcp-login/profiles/prod/start")
	out := l.waitPhase(t, "prod", id, cloudlogin.PhaseFailed)
	if out["message"] != msgUnexpectedURL || out["url"] != nil {
		t.Fatalf("view: %v", out)
	}
	waitExited(t, logins.Attempt(id))

	// On a Compute Engine VM gcloud first asks whether to use a personal account: nothing
	// answers it, and the attempt ends pointing at the terminal login (ADR 0107 note of
	// 2026-10-02).
	l.put(t, "prompt", "")
	id = l.start(t, "/gcp-login/profiles/prod/start")
	out = l.waitPhase(t, "prod", id, cloudlogin.PhaseFailed)
	if out["message"] != msgPrompt {
		t.Fatalf("view: %v", out)
	}
	waitExited(t, logins.Attempt(id))
	if b, _ := os.ReadFile(filepath.Join(l.dir, "prompt-answer")); strings.Contains(string(b), "y") {
		t.Fatalf("the prompt was answered: %s", b)
	}
	l.noSecretAnywhere(t)
}

// TestStoredCredentialAndForce: a stored credential ends the attempt done without a URL;
// "Log in again" and a request filed for a rejected credential pass --force.
func TestStoredCredentialAndForce(t *testing.T) {
	l := setupLogin(t, prod())
	addCredential(t, "dev@example.com", "authorized_user")
	l.put(t, "reuse", "")
	id := l.start(t, "/gcp-login/profiles/prod/start")
	l.waitPhase(t, "prod", id, cloudlogin.PhaseDone)
	args, _ := os.ReadFile(filepath.Join(l.dir, "login-args"))
	if strings.Contains(string(args), "--force") {
		t.Fatalf("a plain Log in forced: %s", args)
	}
	// Log in again: forced, so a URL and a code after all.
	id = l.start(t, "/gcp-login/profiles/prod/start?force=1")
	l.waitPhase(t, "prod", id, cloudlogin.PhaseAuthorize)
	args, _ = os.ReadFile(filepath.Join(l.dir, "login-args"))
	if !strings.HasSuffix(strings.TrimSpace(string(args)), "--force") {
		t.Fatalf("Log in again did not force: %s", args)
	}
	a := logins.Attempt(id)
	a.End(cloudlogin.PhaseCancelled, "")
	waitExited(t, a)

	// A request filed because Google rejected the credential starts forced.
	l.write(t, "fail", "ERROR: (gcloud.config.config-helper) There was a problem refreshing your current auth tokens: "+
		"('invalid_grant: Bad Request', {'error': 'invalid_grant'})")
	o := execOpts(l.env, prod())
	o.Login, o.ConsoleLogin, o.ConsoleWait = "auto", true, 100*time.Millisecond
	if _, _, _, err := PlanExec(l.gcloud, hostile(t), o); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v", err)
	}
	req, ok := logins.Read(ConfigName("prod"))
	if !ok || !req.Snapshot.rejected() {
		t.Fatalf("request = %+v %v", req, ok)
	}
	_, list := l.do(t, "GET", "/gcp-login", "")
	if reqs, _ := list["requests"].([]any); len(reqs) != 1 || reqs[0].(map[string]any)["relogin"] != true {
		t.Fatalf("list = %v", list)
	}
	id = l.start(t, "/gcp-login/"+req.ID+"/start")
	l.waitPhase(t, "prod", id, cloudlogin.PhaseAuthorize)
	args, _ = os.ReadFile(filepath.Join(l.dir, "login-args"))
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	if !strings.HasSuffix(lines[len(lines)-1], "--force") {
		t.Fatalf("a rejected credential's request did not force: %s", args)
	}
	// The same credential succeeding again does not resolve it; the completed login does.
	_ = os.Remove(filepath.Join(l.fakeDir, "fail"))
	if pending := logins.Sweep(time.Now()); len(pending) != 1 {
		t.Fatal("the request was resolved without a login")
	}
	if c, _ := l.submit(t, "prod", id, l.code); c != http.StatusOK {
		t.Fatalf("submit: %d", c)
	}
	l.waitPhase(t, "prod", id, cloudlogin.PhaseDone)
	if pending := logins.Sweep(time.Now()); len(pending) != 0 {
		t.Fatalf("the request outlived the login: %+v", pending)
	}
	l.noSecretAnywhere(t)
}

// TestStartRefusesWhatSettingsDoesNotExport: the press re-syncs first, so a profile removed
// or turned invalid since the last poll is refused, and a request whose profile changed
// since it was filed is dropped.
func TestStartRefusesWhatSettingsDoesNotExport(t *testing.T) {
	l := setupLogin(t, prod())
	o := execOpts(l.env, prod())
	o.Login, o.ConsoleLogin, o.ConsoleWait = "auto", true, 100*time.Millisecond
	if _, _, _, err := PlanExec(l.gcloud, hostile(t), o); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v", err)
	}
	req, ok := logins.Read(ConfigName("prod"))
	if !ok {
		t.Fatal("no request")
	}
	// Settings now names another account: the press syncs, and the request is not started.
	changed := prod()
	changed.Account = "someone@example.com"
	l.setProfiles(changed)
	if c, out := l.do(t, "POST", "/gcp-login/"+req.ID+"/start", ""); c != http.StatusConflict && c != http.StatusNotFound {
		t.Fatalf("start after a change: %d %v", c, out)
	}
	if pending := logins.Sweep(time.Now()); len(pending) != 0 {
		t.Fatalf("a request for a changed profile stayed: %+v", pending)
	}
	// Gone from Settings.
	l.setProfiles()
	if c, out := l.do(t, "POST", "/gcp-login/profiles/prod/start", ""); c != http.StatusNotFound || out["error"] == nil {
		t.Fatalf("start of a removed profile: %d %v", c, out)
	}
	// Not exportable.
	bad := prod()
	bad.Project = "Not A Project"
	l.setProfiles(bad)
	if c, _ := l.do(t, "POST", "/gcp-login/profiles/prod/start", ""); c != http.StatusConflict {
		t.Fatalf("start of an invalid profile: %d", c)
	}
	if _, err := os.Stat(filepath.Join(l.dir, "login-args")); err == nil {
		t.Fatal("gcloud was started for a refused press")
	}
}

// TestWaitEndsWithTheReasonWhenALoginCannotHelp: after a login lands, a check that fails
// with permission denied ends the wait with that reason (exit 1), not with exit 3.
func TestWaitEndsWithTheReasonWhenALoginCannotHelp(t *testing.T) {
	l := setupLogin(t, prod())
	o := execOpts(l.env, prod())
	o.Login, o.ConsoleLogin, o.ConsoleWait = "auto", true, 10*time.Second
	done := make(chan error, 1)
	go func() {
		_, _, _, err := PlanExec(l.gcloud, hostile(t), o)
		done <- err
	}()
	waitForFile(t, logins.RequestPath(ConfigName("prod")))
	l.write(t, "fail", "ERROR: (gcloud.config.config-helper) PERMISSION_DENIED: Permission 'iam.serviceAccounts.getAccessToken' denied")
	addCredential(t, "dev@example.com", "authorized_user")
	completedLogin(t, "dev@example.com")
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the run kept waiting")
	}
	if err == nil || errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "PERMISSION_DENIED") {
		t.Fatalf("err = %v", err)
	}
}

func TestLandedRules(t *testing.T) {
	b := loginBackend{}
	none := LoginState{Profile: "v1", Account: "a@example.com"}
	in := LoginState{Profile: "v1", Account: "a@example.com", Credential: true, Login: "m1"}
	cases := []struct {
		name          string
		cur, recorded LoginState
		want          bool
	}{
		{"no credential, still none", none, none, false},
		{"no credential, now one after a login", in, none, true},
		{"no credential, one written by hand", LoginState{Profile: "v1", Account: "a@example.com", Credential: true}, none, false},
		{"rejected, same credential", in, in, false},
		{"rejected, a login completed", LoginState{Profile: "v1", Account: "a@example.com", Credential: true, Login: "m2"}, in, true},
		{"profile changed", LoginState{Profile: "v2"}, in, true},
		{"rejected, credential gone", none, in, false},
	}
	for _, c := range cases {
		if got := b.Landed(c.cur, c.recorded, time.Now()); got != c.want {
			t.Errorf("%s: Landed = %v", c.name, got)
		}
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("%s never appeared", path)
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// completedLogin records a login of account the way a finished login does, under the lock.
func completedLogin(t *testing.T, account string) {
	t.Helper()
	root, unlock, err := lockRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := recordLogin(root, account); err != nil {
		t.Fatal(err)
	}
}

// TestOnlyAVerifiedLoginSettlesARequest: a credential that appears in the store without a
// login settles nothing, and neither does a login whose mint Google refuses (decision 3
// step 5: resolved when a token can be minted).
func TestOnlyAVerifiedLoginSettlesARequest(t *testing.T) {
	l := setupLogin(t, prod())
	o := execOpts(l.env, prod())
	o.Login, o.ConsoleLogin, o.ConsoleWait = "auto", true, 100*time.Millisecond
	if _, _, _, err := PlanExec(l.gcloud, hostile(t), o); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v", err)
	}
	// A broken credential written by hand: the mint of a waiting run would fail with
	// invalid_grant, and the request must stay.
	addCredential(t, "dev@example.com", "authorized_user")
	if pending := logins.Sweep(time.Now()); len(pending) != 1 {
		t.Fatal("a credential written by hand settled the request")
	}
	// A login whose verification mint Google refuses is not done and settles nothing.
	l.put(t, "mint-fail", "ERROR: (gcloud.config.config-helper) There was a problem refreshing your current auth tokens: invalid_grant: Token has been expired or revoked.")
	id := l.start(t, "/gcp-login/profiles/prod/start")
	l.waitPhase(t, "prod", id, cloudlogin.PhaseAuthorize)
	if c, _ := l.submit(t, "prod", id, l.code); c != http.StatusOK {
		t.Fatalf("submit: %d", c)
	}
	out := l.waitPhase(t, "prod", id, cloudlogin.PhaseFailed)
	if msg, _ := out["message"].(string); !strings.Contains(msg, "Google refused the credential") {
		t.Fatalf("view: %v", out)
	}
	if pending := logins.Sweep(time.Now()); len(pending) != 1 {
		t.Fatal("a login whose mint failed settled the request")
	}
	// The same login with a mint that works settles it.
	_ = os.Remove(filepath.Join(l.dir, "mint-fail"))
	id = l.start(t, "/gcp-login/profiles/prod/start")
	l.waitPhase(t, "prod", id, cloudlogin.PhaseAuthorize)
	if c, _ := l.submit(t, "prod", id, l.code); c != http.StatusOK {
		t.Fatalf("submit: %d", c)
	}
	l.waitPhase(t, "prod", id, cloudlogin.PhaseDone)
	if pending := logins.Sweep(time.Now()); len(pending) != 0 {
		t.Fatalf("the verified login left the request: %+v", pending)
	}
	if mints, _ := os.ReadFile(filepath.Join(l.dir, "mints")); strings.Count(string(mints), "\n") != 2 ||
		strings.Contains(string(mints), "--impersonate") || !strings.Contains(string(mints), "--configuration af-prod") {
		t.Fatalf("verification mints: %s", mints)
	}
	l.noSecretAnywhere(t)
}

// TestLockHeldFromTheCodeUntilExit: output gcloud prints after the code (the overwrite
// question) must not let go of the lock the submit took while gcloud still writes.
func TestLockHeldFromTheCodeUntilExit(t *testing.T) {
	l := setupLogin(t, prod())
	l.put(t, "slow", "")
	id := l.start(t, "/gcp-login/profiles/prod/start")
	l.waitPhase(t, "prod", id, cloudlogin.PhaseAuthorize)
	if _, unlock, err := lockRootNonBlocking(); err != nil {
		t.Fatalf("the lock is held while the member signs in: %v", err)
	} else {
		unlock()
	}
	if c, _ := l.submit(t, "prod", id, l.code); c != http.StatusOK {
		t.Fatalf("submit: %d", c)
	}
	// After the post-code question is printed, before gcloud stores the credential.
	time.Sleep(800 * time.Millisecond)
	if a := logins.Attempt(id); !a.Live() {
		t.Fatal("the fake finished too early for this check")
	}
	if _, unlock, err := lockRootNonBlocking(); !errors.Is(err, errRootBusy) {
		if err == nil {
			unlock()
		}
		t.Fatalf("the lock was let go while gcloud still runs: %v", err)
	}
	l.waitPhase(t, "prod", id, cloudlogin.PhaseDone)
	waitExited(t, logins.Attempt(id))
	_, unlock, err := lockRootNonBlocking()
	if err != nil {
		t.Fatalf("the lock outlived the attempt: %v", err)
	}
	unlock()
}

// TestReuseDoesNotSettleARejectedRequest: while a request filed because Google rejected the
// credential is pending, a plain Log in that gcloud ends at once on the stored credential
// (no URL, no code) is done for the member but settles nothing.
func TestReuseDoesNotSettleARejectedRequest(t *testing.T) {
	l := setupLogin(t, prod())
	addCredential(t, "dev@example.com", "authorized_user")
	completedLogin(t, "dev@example.com")
	l.write(t, "fail", "ERROR: (gcloud.config.config-helper) There was a problem refreshing your current auth tokens: "+
		"('invalid_grant: Bad Request', {'error': 'invalid_grant'})")
	o := execOpts(l.env, prod())
	o.Login, o.ConsoleLogin, o.ConsoleWait = "auto", true, 100*time.Millisecond
	if _, _, _, err := PlanExec(l.gcloud, hostile(t), o); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v", err)
	}
	if req, ok := logins.Read(ConfigName("prod")); !ok || !req.Snapshot.rejected() {
		t.Fatalf("request = %+v %v", req, ok)
	}
	l.put(t, "reuse", "")
	id := l.start(t, "/gcp-login/profiles/prod/start")
	l.waitPhase(t, "prod", id, cloudlogin.PhaseDone)
	if pending := logins.Sweep(time.Now()); len(pending) != 1 {
		t.Fatal("reusing the rejected credential settled the request")
	}
}

// TestVerificationMintUsesTheUsersCredentialOnly: the profile impersonates a service account
// the user may not (yet) be allowed to; the login is still verified and done, because the
// verification mint overrides the configuration's impersonation property too.
func TestVerificationMintUsesTheUsersCredentialOnly(t *testing.T) {
	p := prod()
	p.ImpersonateServiceAccount = "deployer@prod-project.iam.gserviceaccount.com"
	l := setupLogin(t, p)
	l.put(t, "impersonate-denied", "")
	if !strings.Contains(configText(t, "prod"), "impersonate_service_account = "+p.ImpersonateServiceAccount) {
		t.Fatalf("the configuration holds no impersonation:\n%s", configText(t, "prod"))
	}
	id := l.start(t, "/gcp-login/profiles/prod/start")
	l.waitPhase(t, "prod", id, cloudlogin.PhaseAuthorize)
	if c, _ := l.submit(t, "prod", id, l.code); c != http.StatusOK {
		t.Fatalf("submit: %d", c)
	}
	l.waitPhase(t, "prod", id, cloudlogin.PhaseDone)
	mints, _ := os.ReadFile(filepath.Join(l.dir, "mints"))
	if strings.TrimSpace(string(mints)) == "" || !strings.HasSuffix(strings.TrimSpace(string(mints)), "impersonating=") {
		t.Fatalf("verification mints: %s", mints)
	}
	if readLogins(ConfigRoot())["dev@example.com"] == "" {
		t.Fatal("the verified login was not recorded")
	}
}

// TestCodeThenFastExit: gcloud may redeem the code and exit before the submit route has
// returned. The attempt owns the lock and the exchange before the code is written, so the
// exit finds both: a good code ends done and recorded, a bad one fails with gcloud's reason
// (not "busy"), and the lock is free afterwards.
func TestCodeThenFastExit(t *testing.T) {
	old := afterSubmit
	afterSubmit = func() { time.Sleep(300 * time.Millisecond) }
	t.Cleanup(func() { afterSubmit = old })
	oldWait := rootBusyWait
	rootBusyWait = time.Second
	t.Cleanup(func() { rootBusyWait = oldWait })
	l := setupLogin(t, prod())

	id := l.start(t, "/gcp-login/profiles/prod/start")
	l.waitPhase(t, "prod", id, cloudlogin.PhaseAuthorize)
	if c, _ := l.submit(t, "prod", id, l.code); c != http.StatusOK {
		t.Fatalf("submit: %d", c)
	}
	l.waitPhase(t, "prod", id, cloudlogin.PhaseDone)
	if readLogins(ConfigRoot())["dev@example.com"] == "" {
		t.Fatal("the login was not recorded")
	}
	waitExited(t, logins.Attempt(id))

	id = l.start(t, "/gcp-login/profiles/prod/start")
	l.waitPhase(t, "prod", id, cloudlogin.PhaseAuthorize)
	if c, _ := l.submit(t, "prod", id, "4/0wrong"+randHex(t, 8)); c != http.StatusOK {
		t.Fatalf("submit: %d", c)
	}
	out := l.waitPhase(t, "prod", id, cloudlogin.PhaseFailed)
	if msg, _ := out["message"].(string); !strings.Contains(msg, "invalid_grant") || strings.Contains(msg, "busy") {
		t.Fatalf("view: %v", out)
	}
	waitExited(t, logins.Attempt(id))
	_, unlock, err := lockRootNonBlocking()
	if err != nil {
		t.Fatalf("the lock outlived the attempt: %v", err)
	}
	unlock()
}
