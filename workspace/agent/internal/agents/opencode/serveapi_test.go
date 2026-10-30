package opencode

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

const html = "<!doctype html><html><body>OpenCode</body></html>"

// fakeServe answers like the two majors do (measured 1.19.0 / 2.0.28): its own health path
// as JSON, the other major's as 200 text/html, everything behind Basic auth.
type fakeServe struct {
	major    int
	password string
	srv      *httptest.Server
	mu       sync.Mutex
	reqs     []string // "METHOD path?query"
	bodies   map[string]string
	active   func() string // ses id the daemon reports running ("" = idle)
}

func newFakeServe(t *testing.T, major int, password string) *fakeServe {
	t.Helper()
	f := &fakeServe{major: major, password: password, bodies: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	t.Setenv(serveAddrEnv, f.srv.URL)
	t.Setenv("HOME", t.TempDir())
	resetServeAuth(password)
	detectedMajor.Store(int32(apiV1))
	t.Cleanup(func() { detectedMajor.Store(int32(apiV1)); resetServeAuth("") })
	return f
}

// resetServeAuth sets the in-memory password without touching the file.
func resetServeAuth(p string) {
	pwMu.Lock()
	pw, pwLoaded = p, true
	pwMu.Unlock()
}

func (f *fakeServe) handle(w http.ResponseWriter, r *http.Request) {
	if u, p, ok := r.BasicAuth(); f.password != "" && (!ok || u != "opencode" || p != f.password) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	b, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.RequestURI()
	f.mu.Lock()
	f.reqs = append(f.reqs, key)
	f.bodies[key] = string(b)
	f.mu.Unlock()
	js := func(s string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(s))
	}
	switch {
	case r.URL.Path == "/api/info" && f.major == 2:
		js(`{"version":"2.0.28","pid":1}`)
	case r.URL.Path == "/global/health" && f.major == 1:
		js(`{"healthy":true,"version":"1.19.0"}`)
	case r.URL.Path == "/api/info" || r.URL.Path == "/global/health":
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(html))
	case r.URL.Path == "/api/session/active":
		if id := f.activeID(); id != "" {
			js(`{"data":{"` + id + `":{"type":"running"}}}`)
		} else {
			js(`{"data":{}}`)
		}
	case r.URL.Path == "/session/status":
		if id := f.activeID(); id != "" {
			js(`{"` + id + `":{"type":"busy"}}`)
		} else {
			js(`{}`)
		}
	case r.Method == "POST" && (r.URL.Path == "/api/session" || strings.HasSuffix(r.URL.Path, "/fork") && f.major == 2):
		js(`{"data":{"id":"ses_new"}}`)
	case r.Method == "POST" && (r.URL.Path == "/session" || strings.HasSuffix(r.URL.Path, "/fork")):
		js(`{"id":"ses_new"}`)
	default:
		js(`{}`)
	}
}

func (f *fakeServe) activeID() string {
	if f.active == nil {
		return ""
	}
	return f.active()
}

func (f *fakeServe) saw(method, prefix string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.reqs {
		if strings.HasPrefix(k, method+" "+prefix) {
			return k, true
		}
	}
	return "", false
}

func TestProbeRecognisesEachMajorByItsJSON(t *testing.T) {
	for _, major := range []int{1, 2} {
		f := newFakeServe(t, major, "pw")
		if !healthy(f.srv.URL) {
			t.Fatalf("v%d daemon reported unhealthy", major)
		}
		if got := int(serveMajor()); got != major {
			t.Fatalf("detected major = %d, want %d", got, major)
		}
	}
}

// The other major answers 200 text/html on this major's health path, so a status-code-only
// probe calls any server healthy. A page that is 200 everywhere must not pass.
func TestProbeRejectsHTMLWith200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()
	t.Setenv(serveAddrEnv, srv.URL)
	if healthy(srv.URL) {
		t.Fatal("a server answering 200 HTML on both health paths was accepted as healthy")
	}
}

func TestProbeRejectsMissingVersionAndWrongAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pid":1}`)) // JSON, but not an /api/info
	}))
	defer srv.Close()
	t.Setenv(serveAddrEnv, srv.URL)
	if healthy(srv.URL) {
		t.Fatal("JSON without version/healthy was accepted")
	}
	f := newFakeServe(t, 2, "right")
	resetServeAuth("wrong")
	if healthy(f.srv.URL) {
		t.Fatal("a daemon answering 401 was accepted as healthy")
	}
}

func TestEveryClientSendsBasicAuth(t *testing.T) {
	f := newFakeServe(t, 1, "s3cret")
	for name, c := range map[string]*http.Client{"serve": serveClient, "turn": turnClient, "sse": sseClient, "oauth": oauthClient} {
		res, err := c.Get(f.srv.URL + "/global/health")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Errorf("%s client got %d, want 200 (no Basic auth sent?)", name, res.StatusCode)
		}
	}
}

func TestPasswordIsNotSentToOtherHosts(t *testing.T) {
	var got atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("Authorization"))
	}))
	defer other.Close()
	newFakeServe(t, 1, "s3cret") // the serve address is a different server
	res, err := serveClient.Get(other.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if v, _ := got.Load().(string); v != "" {
		t.Fatalf("Authorization leaked to a foreign host: %q", v)
	}
}

func TestNewServePasswordIsRandomAndPrivate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a, err := newServePassword()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newServePassword()
	if a == b || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(a) {
		t.Fatalf("passwords %q / %q are not 256-bit random hex", a, b)
	}
	st, err := os.Stat(servePasswordFile())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("password file: %v %v, want 0600", st, err)
	}
	// A restarted Agent (empty memory) reads the file back to adopt the daemon.
	resetServeAuth("")
	pwMu.Lock()
	pwLoaded = false
	pwMu.Unlock()
	if got := servePassword(); got != b {
		t.Fatalf("reloaded password = %q, want %q", got, b)
	}
	resetServeAuth("")
}

// ensure starts serve with the generated password in its environment, and sends the same
// password on its own requests.
func TestEnsureStartsServeWithPassword(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env")
	script := "#!/bin/sh\necho \"$OPENCODE_SERVER_USERNAME:$OPENCODE_SERVER_PASSWORD\" > " + envFile + ".tmp && mv " + envFile + ".tmp " + envFile + "\nexec sleep 60\n"
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENCODE_SERVER_PASSWORD", "inherited-must-lose")

	var dead atomic.Bool
	var badAuth atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(envFile)
		if err != nil || dead.Load() { // the "daemon" is not up until the process has run
			http.NotFound(w, r)
			return
		}
		want := strings.TrimSpace(string(b))
		u, p, _ := r.BasicAuth()
		if u+":"+p != want {
			badAuth.Add(1)
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/global/health" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"healthy":true,"version":"1.19.0"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv(serveAddrEnv, srv.URL)
	t.Setenv("HOME", t.TempDir())
	resetServeAuth("")
	pwMu.Lock()
	pwLoaded = false
	pwMu.Unlock()

	s := &Supervisor{}
	t.Cleanup(func() {
		dead.Store(true)
		s.mu.Lock()
		if s.cmd != nil {
			s.askedStop = s.cmd
			_ = s.cmd.Process.Kill()
		}
		s.mu.Unlock()
		time.Sleep(1500 * time.Millisecond) // let the event monitor see it is gone
	})
	if _, _, err := s.ensure(true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(envFile)
	got := strings.TrimSpace(string(b))
	if !regexp.MustCompile(`^opencode:[0-9a-f]{64}$`).MatchString(got) {
		t.Fatalf("serve env = %q, want opencode:<random hex> (the inherited value must lose)", got)
	}
	if badAuth.Load() != 0 {
		t.Fatalf("%d probe(s) were sent with a password serve does not hold", badAuth.Load())
	}
}

func TestV2RoutesAndShapes(t *testing.T) {
	f := newFakeServe(t, 2, "pw")
	addr := f.srv.URL
	if !healthy(addr) {
		t.Fatal("v2 fake unhealthy")
	}
	dir := "/work/p q"

	f.active = func() string { return "ses_1" }
	if !serveSessionBusy(addr, "ses_1", dir) || serveSessionBusy(addr, "ses_2", dir) {
		t.Fatal("serveSessionBusy did not read {data:{id:{type:running}}}")
	}
	if _, ok := f.saw("GET", "/api/session/active"); !ok {
		t.Fatalf("no GET /api/session/active in %v", f.reqs)
	}

	id, err := serveCreateSession(addr, dir, "t")
	if err != nil || id != "ses_new" {
		t.Fatalf("create = %q, %v", id, err)
	}
	var cb struct {
		Location struct{ Directory string } `json:"location"`
	}
	if json.Unmarshal([]byte(f.bodies["POST /api/session"]), &cb) != nil || cb.Location.Directory != dir {
		t.Fatalf("create body = %q, want location.directory", f.bodies["POST /api/session"])
	}

	if id, err := serveForkSession(addr, "ses_1", dir, "msg_9"); err != nil || id != "ses_new" {
		t.Fatalf("fork = %q, %v", id, err)
	}
	if b := f.bodies["POST /api/session/ses_1/fork"]; b != `{"before":"msg_9"}` {
		t.Fatalf("fork body = %q", b)
	}

	if err := serveAbort(addr, "ses_1", dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.saw("POST", "/api/session/ses_1/interrupt"); !ok {
		t.Fatalf("abort did not use /interrupt: %v", f.reqs)
	}
	if got := eventURL(addr); got != addr+"/api/event" {
		t.Fatalf("event url = %s", got)
	}
	if serveListQuestions(addr, dir) != nil {
		t.Fatal("v2 has no /question")
	}
	for _, k := range f.reqs {
		if strings.Contains(k, "/question") || strings.HasPrefix(k, "GET /session") || strings.Contains(k, "directory=") {
			t.Errorf("a v1 route reached the v2 daemon: %s", k)
		}
	}
}

func TestV1RoutesUnchanged(t *testing.T) {
	f := newFakeServe(t, 1, "pw")
	addr := f.srv.URL
	if !healthy(addr) {
		t.Fatal("v1 fake unhealthy")
	}
	dir := "/work/p"
	f.active = func() string { return "ses_1" }
	if !serveSessionBusy(addr, "ses_1", dir) {
		t.Fatal("v1 /session/status not read")
	}
	if _, ok := f.saw("GET", "/session/status?directory=%2Fwork%2Fp"); !ok {
		t.Fatalf("v1 status route changed: %v", f.reqs)
	}
	if id, err := serveCreateSession(addr, dir, "t"); err != nil || id != "ses_new" {
		t.Fatalf("create = %q, %v", id, err)
	}
	if _, ok := f.saw("POST", "/session?directory=%2Fwork%2Fp"); !ok {
		t.Fatalf("v1 create route changed: %v", f.reqs)
	}
	if _, err := serveForkSession(addr, "ses_1", dir, "msg_9"); err != nil {
		t.Fatal(err)
	}
	if b := f.bodies["POST /session/ses_1/fork?directory=%2Fwork%2Fp"]; b != `{"messageID":"msg_9"}` {
		t.Fatalf("v1 fork body = %q", b)
	}
	_ = serveAbort(addr, "ses_1", dir)
	if _, ok := f.saw("POST", "/session/ses_1/abort?directory=%2Fwork%2Fp"); !ok {
		t.Fatalf("v1 abort route changed: %v", f.reqs)
	}
	if got := eventURL(addr); got != addr+"/global/event" {
		t.Fatalf("event url = %s", got)
	}
}

func TestPushEngineProvidersNeedsRestartOnV2(t *testing.T) {
	f := newFakeServe(t, 2, "pw")
	healthy(f.srv.URL)
	prev := supervisor
	supervisor = &Supervisor{up: true}
	t.Cleanup(func() { supervisor = prev })
	err := PushEngineProviders([]EngineProvider{{Provider: "p", BaseURL: "http://x", Models: []string{"m"}}})
	if !errors.Is(err, ErrLiveConfigUnsupported) {
		t.Fatalf("err = %v, want ErrLiveConfigUnsupported", err)
	}
	if _, ok := f.saw("PATCH", ""); ok {
		t.Fatal("a PATCH reached the v2 daemon")
	}
}

// v2's /prompt only queues the input: the turn ends when the session leaves the active list.
func TestV2TurnWaitsForActiveToClear(t *testing.T) {
	f := newFakeServe(t, 2, "pw")
	healthy(f.srv.URL)
	var polls atomic.Int32
	f.active = func() string {
		if polls.Add(1) <= 3 {
			return "ses_1"
		}
		return ""
	}
	prev := v2PollEvery
	v2PollEvery = 5 * time.Millisecond
	t.Cleanup(func() { v2PollEvery = prev })
	inner := f.srv.Config.Handler
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/message"):
			if polls.Load() > 3 { // the execution has ended: its idle message exists
				_, _ = w.Write([]byte(`{"data":[{"id":"msg_9","type":"idle","outcome":"succeeded"}]}`))
			} else {
				_, _ = w.Write([]byte(`{"data":[]}`))
			}
		case strings.HasSuffix(r.URL.Path, "/prompt"):
			b, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.bodies["POST "+r.URL.RequestURI()] = string(b)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"data":{"id":"msg_5"}}`))
		default:
			inner.ServeHTTP(w, r)
		}
	})

	in := agents.TurnInput{Prompt: " hello ", Attachments: []string{"/tmp/a b.png"}}
	st := agents.ThreadSettings{Model: "prov/mod", Mode: "plan", Effort: "high"}
	req, err := newTurnRequest(f.srv.URL, "ses_1", "/d", in, st)
	if err != nil {
		t.Fatal(err)
	}
	res, err := sendTurn(f.srv.URL, "ses_1", "/d", req, st)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || polls.Load() < 4 {
		t.Fatalf("status %d after %d polls: returned before the turn ended", res.StatusCode, polls.Load())
	}
	if b := f.bodies["POST /api/session/ses_1/prompt"]; !strings.Contains(b, `"text":"hello"`) || !strings.Contains(b, `file:///tmp/a b.png`) {
		t.Fatalf("prompt body = %s", b)
	}
	if b := f.bodies["POST /api/session/ses_1/model"]; b != `{"model":{"id":"mod","providerID":"prov","variant":"high"}}` {
		t.Fatalf("model body = %s", b)
	}
	if b := f.bodies["POST /api/session/ses_1/agent"]; b != `{"agent":"plan"}` {
		t.Fatalf("agent body = %s", b)
	}
}

// A daemon that misses one health probe must stay adoptable: the password a failed start
// tried is forgotten, and the file keeps the one the live daemon holds.
func TestFailedStartKeepsThePasswordOfTheLiveDaemon(t *testing.T) {
	f := newFakeServe(t, 1, "P1")
	// The probe that fails must be the last chance of the new password: on a PATH opencode 2 the
	// shared-TUI policy keeps the live password across a start, the fake would accept the
	// candidate and this start would succeed, so the host's major must not decide it.
	useMajor(t, 1)
	if err := persistServePassword("P1"); err != nil {
		t.Fatal(err)
	}
	var drop atomic.Bool
	drop.Store(true)
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if drop.CompareAndSwap(true, false) {
			http.Error(w, "busy", http.StatusServiceUnavailable) // one slow/failed probe
			return
		}
		f.handle(w, r)
	})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	prev := serveStartTimeout
	serveStartTimeout = time.Second
	t.Cleanup(func() { serveStartTimeout = prev })

	s := &Supervisor{}
	if _, _, err := s.ensure(true); err == nil {
		t.Fatal("the first Ensure should have failed on the dropped probe")
	}
	if got := servePassword(); got != "P1" {
		t.Fatalf("password after the failed start = %q, want P1", got)
	}
	if b, _ := os.ReadFile(servePasswordFile()); strings.TrimSpace(string(b)) != "P1" {
		t.Fatalf("password file = %q, want P1", b)
	}
	t.Cleanup(func() { s.mu.Lock(); s.up = false; s.mu.Unlock() })
	if _, _, err := s.ensure(true); err != nil { // the daemon recovered: adopt it
		t.Fatalf("second Ensure: %v", err)
	}
}

func TestEnsureRefusesToSpawnOverADaemonItCannotAuthenticateTo(t *testing.T) {
	newFakeServe(t, 1, "theirs")
	resetServeAuth("ours")
	dir := t.TempDir()
	marker := filepath.Join(dir, "spawned")
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, _, err := (&Supervisor{}).ensure(true); err == nil {
		t.Fatal("ensure succeeded against a daemon that rejects our password")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a second serve was spawned onto the occupied port")
	}
	if servePassword() != "ours" {
		t.Fatal("the password was rotated")
	}
}

func v2Fast(t *testing.T) {
	t.Helper()
	prev, prevStart := v2PollEvery, v2StartWithin
	v2PollEvery, v2StartWithin = 2*time.Millisecond, 30*time.Millisecond
	t.Cleanup(func() { v2PollEvery, v2StartWithin = prev, prevStart })
}

func v2Turn(t *testing.T, f *fakeServe) (*http.Response, error) {
	t.Helper()
	req, err := newTurnRequest(f.srv.URL, "ses_1", "/d", agents.TurnInput{Prompt: "x"}, agents.ThreadSettings{})
	if err != nil {
		t.Fatal(err)
	}
	return sendTurn(f.srv.URL, "ses_1", "/d", req, agents.ThreadSettings{})
}

func TestV2TurnFailsWhenTheGenerationChanges(t *testing.T) {
	f := newFakeServe(t, 2, "pw")
	healthy(f.srv.URL)
	v2Fast(t)
	prev := supervisor
	supervisor = &Supervisor{gen: 1}
	t.Cleanup(func() { supervisor = prev })
	var n atomic.Int32
	f.active = func() string {
		if n.Add(1) == 3 {
			supervisor.mu.Lock()
			supervisor.gen++
			supervisor.mu.Unlock()
		}
		if n.Load() >= 3 {
			return "" // the replacement daemon reports an empty list
		}
		return "ses_1"
	}
	if _, err := v2Turn(t, f); err == nil {
		t.Fatal("a turn that outlived its daemon was reported as finished")
	}
}

func TestV2TurnFailsAfterConsecutiveMisses(t *testing.T) {
	f := newFakeServe(t, 2, "pw")
	healthy(f.srv.URL)
	v2Fast(t)
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/active") {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		f.handle(w, r)
	})
	if _, err := v2Turn(t, f); err == nil {
		t.Fatal("unreachable /active was reported as a finished turn")
	}
}

// v2 events carry their body in "data"; the permission auto-allow must still fire.
func TestV2PermissionEventIsAutoAllowed(t *testing.T) {
	f := newFakeServe(t, 2, "pw")
	healthy(f.srv.URL)
	h := &threadHandle{name: "perm-v2", addr: f.srv.URL, dir: "/d", ses: "ses_p", alive: true}
	handlesMu.Lock()
	prev := handles
	handles = map[string]*threadHandle{"perm-v2": h}
	handlesMu.Unlock()
	t.Cleanup(func() { handlesMu.Lock(); handles = prev; handlesMu.Unlock() })
	handleServeEvent([]byte(`{"id":"evt_1","type":"permission.asked","data":{"id":"per_1","sessionID":"ses_p"}}`))
	k, ok := f.saw("POST", "/api/session/ses_p/permission/per_1/reply")
	if !ok || f.bodies[k] != `{"decision":"always"}` {
		t.Fatalf("no v2 permission reply: %v", f.reqs)
	}
}
