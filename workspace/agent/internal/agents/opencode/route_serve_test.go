package opencode

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// fakeRouteServeEnv makes the test binary a stand-in for `opencode serve` (TestMain): it answers
// /api/info as an opencode 2 server only with the password from its environment, holds
// /api/event open, and writes its environment to $AF_TEST_ROUTE_SERVE_LOG.
const fakeRouteServeEnv = "AF_TEST_FAKE_ROUTE_SERVE"

func runFakeRouteServe() {
	port := ""
	for i, a := range os.Args {
		if a == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	if f := os.Getenv("AF_TEST_ROUTE_SERVE_LOG"); f != "" {
		_ = os.WriteFile(f, []byte(strings.Join(os.Environ(), "\n")), 0o600)
	}
	pw := os.Getenv("OPENCODE_SERVER_PASSWORD")
	mux := http.NewServeMux()
	auth := func(r *http.Request) bool {
		u, p, ok := r.BasicAuth()
		return ok && u == serveUser && p == pw
	}
	mux.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"version":"2.2.6","pid":%d}`, os.Getpid())
	})
	mux.HandleFunc("/api/event", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	_ = http.ListenAndServe("127.0.0.1:"+port, mux)
}

// useFakeRouteServe puts an `opencode` on PATH that runs the stand-in, and returns the file its
// environment is written to.
func useFakeRouteServe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "env")
	script := "#!/bin/sh\nexec env " + fakeRouteServeEnv + "=1 AF_TEST_ROUTE_SERVE_LOG=" + log + " " + exe + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func managedBedrockMeta(t *testing.T, name string) session.Meta {
	t.Helper()
	m := bedrockMeta(t, name)
	m.Driver = session.DriverManaged
	return m
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// A Managed session on the route gets a server of its own: the route's bearer and inline config,
// the session's identity, none of the workspace's provider keys or the shared-server marker; it is
// the gateway's caller root while it runs, its password reaches it and no other host, and a stop
// leaves no process and no root behind.
func TestRouteServeIsTheSessionsOwn(t *testing.T) {
	isolatedHome(t)
	useMajor(t, 2)
	log := useFakeRouteServe(t)
	withGateway(t, &fakeGateway{})
	// A provider key the workspace stores, which reaches the shared server and must not reach
	// this one even when the Agent's own environment carries it.
	prevPref := UsagePref
	UsagePref = func() string { return UsageZen }
	t.Cleanup(func() { UsagePref = prevPref })
	if err := secrets.Update(func(d *secrets.Data) error {
		d.Opencode = map[string]string{"ANTHROPIC_API_KEY": "workspace-key"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "workspace-key")
	t.Setenv(sharedServeEnv, "1")
	t.Setenv("OPENCODE_CONFIG_DIR", "/elsewhere")
	m := managedBedrockMeta(t, "oc_route1")

	addr, gen, err := ensureRouteServe(m)
	if err != nil {
		t.Fatalf("ensureRouteServe: %v", err)
	}
	t.Cleanup(func() { stopRouteServe(m.Name, "test") })
	if addr == serveAddr() || gen <= 0 {
		t.Fatalf("addr %q gen %d: not a private server", addr, gen)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Split(string(b), "\n")
	for _, want := range []string{"AWS_BEARER_TOKEN_BEDROCK=" + testBearer, "AF_SESSION_NAME=" + m.Name, "OPENCODE_SERVER_USERNAME=" + serveUser} {
		if !slices.Contains(env, want) {
			t.Errorf("server environment lacks %s", want)
		}
	}
	for _, prefix := range []string{routeConfigKey + "=", "AWS_REGION=", "AF_WORK_DIR="} {
		if !slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, prefix) }) {
			t.Errorf("server environment lacks %s…", prefix)
		}
	}
	for _, banned := range []string{"ANTHROPIC_API_KEY=", sharedServeEnv + "=", "OPENCODE_CONFIG_DIR=", "AF_SESSION_SID="} {
		if slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, banned) }) {
			t.Errorf("server environment carries %s", banned)
		}
	}

	roots := agents.RuntimeRoots(m.Name)
	if len(roots) != 1 || !alive(roots[0]) {
		t.Fatalf("runtime roots = %v, want the live server process", roots)
	}
	if got := agents.RuntimeRoots("someone_else"); len(got) != 0 {
		t.Fatalf("another session has roots %v", got)
	}
	if !healthy(addr) {
		t.Fatal("the server's own password did not reach it")
	}
	if p := passwordFor(mustURL(t, "http://127.0.0.1:1/")); p != "" {
		t.Fatal("a route server's password went to another host")
	}
	if got := routeServeGen(addr); got != gen {
		t.Fatalf("routeServeGen = %d, want %d", got, gen)
	}

	// A second ensure keeps the running server.
	if addr2, gen2, err := ensureRouteServe(m); err != nil || addr2 != addr || gen2 != gen {
		t.Fatalf("second ensure = %q %d %v, want the same server", addr2, gen2, err)
	}

	stopRouteServe(m.Name, "test")
	if alive(roots[0]) {
		t.Fatal("the server outlived its stop")
	}
	if got := agents.RuntimeRoots(m.Name); len(got) != 0 {
		t.Fatalf("roots after stop = %v", got)
	}
	if routeServeGen(addr) != -1 {
		t.Fatal("a stopped server still has a generation")
	}
}

// A server that dies on its own is forgotten, and the next ensure starts a new one.
func TestRouteServeIsReplacedAfterItDies(t *testing.T) {
	isolatedHome(t)
	useMajor(t, 2)
	useFakeRouteServe(t)
	withGateway(t, &fakeGateway{})
	m := managedBedrockMeta(t, "oc_route2")
	addr, gen, err := ensureRouteServe(m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopRouteServe(m.Name, "test") })
	rs := routeServeOf(m.Name)
	_ = rs.cmd.Process.Kill()
	select {
	case <-rs.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the dead server was not reaped")
	}
	if routeServeOf(m.Name) != nil || len(agents.RuntimeRoots(m.Name)) != 0 {
		t.Fatal("a dead server is still registered")
	}
	addr2, gen2, err := ensureRouteServe(m)
	if err != nil {
		t.Fatal(err)
	}
	if addr2 == addr || gen2 <= gen {
		t.Fatalf("replacement = %q gen %d, after %q gen %d", addr2, gen2, addr, gen)
	}
}

// A server that never answers is killed and leaves nothing registered.
func TestRouteServeThatNeverAnswersLeavesNothing(t *testing.T) {
	isolatedHome(t)
	useMajor(t, 2)
	bin := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "pid")
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte("#!/bin/sh\necho $$ > "+pidFile+"\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	withGateway(t, &fakeGateway{})
	old := routeServeStartTimeout
	routeServeStartTimeout = time.Second
	t.Cleanup(func() { routeServeStartTimeout = old })
	m := managedBedrockMeta(t, "oc_route3")
	if _, _, err := ensureRouteServe(m); err == nil {
		t.Fatal("a server that never answered was accepted")
	}
	if routeServeOf(m.Name) != nil {
		t.Fatal("a failed server is still registered")
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	fmt.Sscan(strings.TrimSpace(string(b)), &pid)
	if pid == 0 || alive(pid) {
		t.Fatalf("the failed server (pid %d) is still running", pid)
	}
}

// The route's refusals stop the start before any process: a session without a route, and one the
// gateway refuses.
func TestRouteServeRefusesBeforeStarting(t *testing.T) {
	isolatedHome(t)
	useMajor(t, 2)
	log := useFakeRouteServe(t)
	m := managedBedrockMeta(t, "oc_route4")
	m.Route = nil
	if _, _, err := ensureRouteServe(m); err == nil {
		t.Fatal("a session without a route got a route server")
	}
	withGateway(t, &fakeGateway{refuse: &agents.RouteRefusal{Status: 403, Code: "route_connection_forbidden", Message: "no"}})
	if _, _, err := ensureRouteServe(managedBedrockMeta(t, "oc_route5")); err == nil {
		t.Fatal("a refused route got a server")
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("a server was started for a refused route")
	}
}

// The shared daemon's demand and its handle lists leave the routed sessions out: they do not keep
// it up, and its death or drain is not theirs.
func TestSharedServeLeavesRoutedHandlesOut(t *testing.T) {
	handlesMu.Lock()
	saved := handles
	handles = map[string]*threadHandle{
		"plain":  {name: "plain", alive: true},
		"routed": {name: "routed", routed: true, alive: true},
	}
	handlesMu.Unlock()
	t.Cleanup(func() {
		handlesMu.Lock()
		handles = saved
		handlesMu.Unlock()
	})
	stubTerminalSlots(t, func() []session.Meta { return nil })
	// Relative to the plain handle alone: an OAuth flow another test left counts as demand too.
	withRouted := dependents()
	handlesMu.Lock()
	delete(handles, "routed")
	handlesMu.Unlock()
	if plain := dependents(); withRouted != plain {
		t.Fatalf("dependents = %d with the routed session, %d without: it runs on its own server", withRouted, plain)
	}
	handlesMu.Lock()
	handles["routed"] = &threadHandle{name: "routed", routed: true, alive: true}
	handlesMu.Unlock()
	hs := sharedHandles()
	if len(hs) != 1 || hs[0].name != "plain" {
		t.Fatalf("sharedHandles = %v", hs)
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
