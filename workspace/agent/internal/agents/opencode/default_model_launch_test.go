package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// fakeDirServe answers the endpoints the way opencode 2.4.0 does (measured): /api/plugin is []
// until the directory has settled, /api/provider is partial until then.
type fakeDirServe struct {
	providers   string // data of the settled /api/provider
	partial     string // data of /api/provider before the plugins load
	def         string // data of /api/model/default
	plugins     string // data of the settled /api/plugin
	settleAfter int32  // /api/plugin answers [] this many times first
	echoDir     string // overrides location.directory when set
	swapAfter   int32  // /api/info reports a new pid once /api/plugin has answered this many times (0: never)
	infoBody    string // overrides /api/info when set
	calls       atomic.Int32
	providerRan atomic.Int32
	mu          sync.Mutex
	asked       []string // directories asked about
}

// askedDirs is a copy of the directories asked about, safe to read while the handler runs.
func (f *fakeDirServe) askedDirs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

func (f *fakeDirServe) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dir := r.URL.Query().Get("location[directory]")
		if r.URL.Path != "/api/info" {
			f.mu.Lock()
			f.asked = append(f.asked, dir)
			f.mu.Unlock()
		}
		if f.echoDir != "" {
			dir = f.echoDir
		}
		loc := `{"directory":` + mustJSON(dir) + `}`
		settled := f.calls.Load() > f.settleAfter
		switch r.URL.Path {
		case "/api/info":
			if f.infoBody != "" {
				_, _ = w.Write([]byte(f.infoBody))
				return
			}
			pid := 1000
			if f.swapAfter > 0 && f.calls.Load() >= f.swapAfter {
				pid = 2000
			}
			_, _ = w.Write([]byte(`{"version":"2.4.0","pid":` + mustJSON(pid) + `}`))
		case "/api/plugin":
			f.calls.Add(1)
			data := "[]"
			if f.calls.Load() > f.settleAfter {
				data = f.plugins
			}
			_, _ = w.Write([]byte(`{"location":` + loc + `,"data":` + data + `}`))
		case "/api/provider":
			f.providerRan.Add(1)
			data := f.partial
			if settled {
				data = f.providers
			}
			_, _ = w.Write([]byte(`{"location":` + loc + `,"data":` + data + `}`))
		case "/api/model/default":
			_, _ = w.Write([]byte(`{"location":` + loc + `,"data":` + f.def + `}`))
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>web ui</html>")) // what a path the daemon does not serve answers
		}
	})
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

const (
	officialURL   = `https://opencode.ai/inference/openai/v1`
	onlyOpencode  = `[{"id":"opencode","settings":{"apiKey":"public","baseURL":"` + officialURL + `"}}]`
	opencodeAcme  = `[{"id":"opencode","settings":{"apiKey":"public","baseURL":"` + officialURL + `"}},{"id":"acme","settings":{"apiKey":"k"}}]`
	opencodeKeyed = `[{"id":"opencode","settings":{"apiKey":"sk-real","baseURL":"` + officialURL + `"}}]`
	opencodeMoved = `[{"id":"opencode","settings":{"apiKey":"public","baseURL":"https://proxy.example/v1"}}]`
	freeDefault   = `{"id":"big-pickle","providerID":"opencode","status":"active","enabled":true,"settings":{"apiKey":"public","baseURL":"` + officialURL + `"},"cost":[{"input":0,"output":0,"cache":{"read":0,"write":0}}]}`
	activePlugins = `[{"id":"opencode.wellknown","state":{"status":"active"}}]`
)

func defaultWith(cost, extra string) string {
	return `{"id":"m","providerID":"opencode","settings":{"apiKey":"public","baseURL":"` + officialURL + `"},` + extra + `"cost":` + cost + `}`
}

// serveWith points the Agent at a fake serve; the launch reads its address like any caller.
func serveWith(t *testing.T, f *fakeDirServe) {
	t.Helper()
	if f.plugins == "" {
		f.plugins = activePlugins
	}
	if f.partial == "" {
		f.partial = "[]"
	}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	t.Setenv(serveAddrEnv, srv.URL)
}

func freeView(t *testing.T) {
	t.Helper()
	old := credentialView
	t.Cleanup(func() { credentialView = old })
	credentialView = func() (map[string]string, string, oauthState, *secrets.CustomAPIConn) {
		return nil, UsageZen, oauthState{known: true}, nil
	}
}

var freeLaunch = routes.Launch{Route: routes.RouteOpencodeZen, Binding: FreeBinding}

// realTemp is a temp dir whose path is already resolved (the launch resolves symlinks).
func realTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func resolveDefault(m session.Meta) routes.Launch {
	m.Name, m.Kind = "s", session.KindOpencode
	if m.Driver == "" {
		m.Driver = session.DriverManaged
	}
	return (agentImpl{}).ResolveDefaultLaunch(m)
}

// The acceptance of #4227: a directory with only opencode.ai's free models records the keyless
// route, and one with several providers stays unresolved.
func TestDefaultModelLaunchFromServe(t *testing.T) {
	dir := realTemp(t)
	for _, tc := range []struct {
		name string
		f    *fakeDirServe
		want routes.Launch
	}{
		{"only the keyless opencode provider and a free default", &fakeDirServe{providers: onlyOpencode, def: freeDefault}, freeLaunch},
		{"settles after a few reads", &fakeDirServe{providers: onlyOpencode, def: freeDefault, settleAfter: 3}, freeLaunch},
		{"several providers", &fakeDirServe{providers: opencodeAcme, def: freeDefault}, routes.Launch{}},
		{"a key reached the opencode provider", &fakeDirServe{providers: opencodeKeyed, def: freeDefault}, routes.Launch{}},
		{"the opencode provider's baseURL is overridden", &fakeDirServe{providers: opencodeMoved, def: freeDefault}, routes.Launch{}},
		{"the default model's baseURL is overridden", &fakeDirServe{providers: onlyOpencode, def: `{"id":"m","providerID":"opencode","settings":{"apiKey":"public","baseURL":"https://proxy.example/v1"},"cost":[{"input":0,"output":0}]}`}, routes.Launch{}},
		{"the default model carries its own key", &fakeDirServe{providers: onlyOpencode, def: `{"id":"m","providerID":"opencode","settings":{"apiKey":"sk-model","baseURL":"` + officialURL + `"},"cost":[{"input":0,"output":0}]}`}, routes.Launch{}},
		{"the default model states no key", &fakeDirServe{providers: onlyOpencode, def: `{"id":"m","providerID":"opencode","settings":{"baseURL":"` + officialURL + `"},"cost":[{"input":0,"output":0}]}`}, routes.Launch{}},
		{"a cache entry with no write price", &fakeDirServe{providers: onlyOpencode, def: defaultWith(`[{"input":0,"output":0,"cache":{"read":0}}]`, "")}, routes.Launch{}},
		{"a cache entry with no read price", &fakeDirServe{providers: onlyOpencode, def: defaultWith(`[{"input":0,"output":0,"cache":{"write":0}}]`, "")}, routes.Launch{}},
		{"an empty cache entry", &fakeDirServe{providers: onlyOpencode, def: defaultWith(`[{"input":0,"output":0,"cache":{}}]`, "")}, routes.Launch{}},
		{"a paid cache write", &fakeDirServe{providers: onlyOpencode, def: defaultWith(`[{"input":0,"output":0,"cache":{"read":0,"write":2}}]`, "")}, routes.Launch{}},
		{"a version that is not a number", &fakeDirServe{providers: onlyOpencode, def: freeDefault, infoBody: `{"version":"unknown","pid":7}`}, routes.Launch{}},
		{"a version with a padded major", &fakeDirServe{providers: onlyOpencode, def: freeDefault, infoBody: `{"version":"02.4.0","pid":7}`}, routes.Launch{}},
		{"no pid", &fakeDirServe{providers: onlyOpencode, def: freeDefault, infoBody: `{"version":"2.4.0"}`}, routes.Launch{}},
		{"no default model", &fakeDirServe{providers: onlyOpencode, def: `null`}, routes.Launch{}},
		{"a paid default", &fakeDirServe{providers: onlyOpencode, def: defaultWith(`[{"input":5,"output":25}]`, "")}, routes.Launch{}},
		{"a default paid for output only", &fakeDirServe{providers: onlyOpencode, def: defaultWith(`[{"input":0,"output":25}]`, "")}, routes.Launch{}},
		{"a default paid for cache only", &fakeDirServe{providers: onlyOpencode, def: defaultWith(`[{"input":0,"output":0,"cache":{"read":1,"write":0}}]`, "")}, routes.Launch{}},
		{"one paid tier among free ones", &fakeDirServe{providers: onlyOpencode, def: defaultWith(`[{"input":0,"output":0},{"input":0,"output":3}]`, "")}, routes.Launch{}},
		{"a tier with no prices", &fakeDirServe{providers: onlyOpencode, def: defaultWith(`[{}]`, "")}, routes.Launch{}},
		{"a tier with no output price", &fakeDirServe{providers: onlyOpencode, def: defaultWith(`[{"input":0}]`, "")}, routes.Launch{}},
		{"no price tiers", &fakeDirServe{providers: onlyOpencode, def: defaultWith(`[]`, "")}, routes.Launch{}},
		{"no cost field", &fakeDirServe{providers: onlyOpencode, def: `{"id":"m","providerID":"opencode","settings":{"baseURL":"` + officialURL + `"}}`}, routes.Launch{}},
		{"a default of another provider", &fakeDirServe{providers: onlyOpencode, def: `{"id":"m","providerID":"acme","settings":{"baseURL":"` + officialURL + `"},"cost":[{"input":0,"output":0}]}`}, routes.Launch{}},
		{"a deprecated default", &fakeDirServe{providers: onlyOpencode, def: defaultWith(`[{"input":0,"output":0}]`, `"status":"deprecated",`)}, routes.Launch{}},
		{"a failed plugin", &fakeDirServe{providers: onlyOpencode, def: freeDefault, plugins: `[{"id":"p","state":{"status":"failed","error":"x"}}]`}, routes.Launch{}},
		{"an answer for another directory", &fakeDirServe{providers: onlyOpencode, def: freeDefault, echoDir: "/elsewhere"}, routes.Launch{}},
		{"a serve replaced while it was read", &fakeDirServe{providers: onlyOpencode, def: freeDefault, swapAfter: 1}, routes.Launch{}},
		{"a v1 daemon", &fakeDirServe{providers: onlyOpencode, def: freeDefault, infoBody: `{"version":"1.19.0","pid":7}`}, routes.Launch{}},
		{"an info answer that is not a serve's", &fakeDirServe{providers: onlyOpencode, def: freeDefault, infoBody: `<html>web ui</html>`}, routes.Launch{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			freeView(t)
			serveWith(t, tc.f)
			if got := resolveDefault(session.Meta{Dir: dir}); got != tc.want {
				t.Errorf("ResolveDefaultLaunch = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The provider list is read only after the plugins have loaded: before that a directory whose
// project adds a provider still lists the keyless opencode provider alone, which would resolve a
// multi-provider launch as free.
func TestDefaultModelLaunchWaitsForSettle(t *testing.T) {
	freeView(t)
	f := &fakeDirServe{providers: opencodeAcme, partial: onlyOpencode, def: freeDefault, settleAfter: 4}
	serveWith(t, f)
	if got := resolveDefault(session.Meta{Dir: realTemp(t)}); got != (routes.Launch{}) {
		t.Fatalf("a partial provider list resolved the launch: %+v", got)
	}
	if f.providerRan.Load() != 1 {
		t.Errorf("/api/provider was read %d times, want once and after the plugins loaded", f.providerRan.Load())
	}
}

// A directory that does not settle within the budget leaves the launch unresolved, and the probe
// ends on time instead of polling on.
func TestDefaultModelLaunchTimesOutUnsettled(t *testing.T) {
	freeView(t)
	serveWith(t, &fakeDirServe{providers: onlyOpencode, def: freeDefault, settleAfter: 1 << 30})
	start := time.Now()
	if got := resolveDefault(session.Meta{Dir: realTemp(t)}); got != (routes.Launch{}) {
		t.Fatalf("an unsettled directory resolved: %+v", got)
	}
	if d := time.Since(start); d > routes.ObserveBudget {
		t.Errorf("the probe ran %s, past the %s launch budget", d, routes.ObserveBudget)
	}
}

// A daemon that accepts the connection and never answers (the health read included) is cut at the
// budget: nothing waits for the HTTP client's own timeout, and no directory is asked about.
func TestDefaultModelLaunchHungDaemonIsCutAtTheBudget(t *testing.T) {
	freeView(t)
	release := make(chan struct{})
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	t.Setenv(serveAddrEnv, srv.URL)
	start := time.Now()
	if got := resolveDefault(session.Meta{Dir: realTemp(t)}); got != (routes.Launch{}) {
		t.Fatalf("a hung daemon resolved: %+v", got)
	}
	if d := time.Since(start); d > routes.ObserveBudget {
		t.Errorf("the probe ran %s, past the %s launch budget", d, routes.ObserveBudget)
	}
	if asked.Load() != 1 {
		t.Errorf("the daemon was asked %d times, want only the health read", asked.Load())
	}
}

// The serve is asked about the directory the session runs in: a chosen subdirectory, not the
// working copy root, and the path the OS opens (symlinks resolved), the same one it must echo.
func TestDefaultModelLaunchAsksAboutTheRunningDirectory(t *testing.T) {
	root := realTemp(t)
	sub := filepath.Join(root, "pkg", "app")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(realTemp(t), "link")
	if err := os.Symlink(sub, link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		m    session.Meta
		want string // "" = never asked
	}{
		{"the working copy root", session.Meta{Dir: root}, root},
		{"a chosen subdirectory", session.Meta{Dir: root, Subdir: "pkg/app"}, sub},
		{"a subdirectory that is gone falls back to the root, as the runtime does", session.Meta{Dir: root, Subdir: "gone"}, root},
		{"a trailing slash", session.Meta{Dir: root + "/"}, root},
		{"a symlinked directory", session.Meta{Dir: link}, sub},
		{"a symlink then ..", session.Meta{Dir: link + "/.."}, filepath.Join(root, "pkg")},
		{"a directory that does not exist", session.Meta{Dir: filepath.Join(root, "nope")}, ""},
		{"a relative directory", session.Meta{Dir: "relative/dir"}, ""},
		{"no directory", session.Meta{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			freeView(t)
			f := &fakeDirServe{providers: onlyOpencode, def: freeDefault}
			serveWith(t, f)
			got := resolveDefault(tc.m)
			if tc.want == "" {
				if got != (routes.Launch{}) || len(f.askedDirs()) != 0 {
					t.Errorf("resolved %+v after asking %v, want nothing asked", got, f.askedDirs())
				}
				return
			}
			if got != freeLaunch {
				t.Errorf("resolved %+v, want %+v", got, freeLaunch)
			}
			for _, a := range f.askedDirs() {
				if a != tc.want {
					t.Errorf("asked about %q, want %q", a, tc.want)
				}
			}
		})
	}
}

// No serve is running: unresolved, and never a daemon start.
func TestDefaultModelLaunchNeedsAServe(t *testing.T) {
	freeView(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close() // nothing listens there any more
	t.Setenv(serveAddrEnv, addr)
	if got := resolveDefault(session.Meta{Dir: realTemp(t)}); got != (routes.Launch{}) {
		t.Errorf("no daemon: %+v", got)
	}
}

// The serve proves the provider is keyless; af's own view still has to agree that the free route
// is the payer, and hold no stored key at all (the launch's environment carries every one of them,
// and the serve may have started before it was stored). A signed-in account is another payer.
func TestDefaultModelLaunchKeepsAfsOwnView(t *testing.T) {
	serveWith(t, &fakeDirServe{providers: onlyOpencode, def: freeDefault})
	old := credentialView
	t.Cleanup(func() { credentialView = old })
	type view = func() (map[string]string, string, oauthState, *secrets.CustomAPIConn)
	for name, v := range map[string]view{
		"a stored opencode key": func() (map[string]string, string, oauthState, *secrets.CustomAPIConn) {
			return map[string]string{opencodeKeyEnv: "k"}, UsageZen, oauthState{known: true}, nil
		},
		"a stored provider key": func() (map[string]string, string, oauthState, *secrets.CustomAPIConn) {
			return map[string]string{"ANTHROPIC_API_KEY": "k"}, UsageZen, oauthState{known: true}, nil
		},
		"a signed-in login": func() (map[string]string, string, oauthState, *secrets.CustomAPIConn) {
			return nil, UsageZen, oauthState{known: true, connected: true, id: "a"}, nil
		},
		"an unknown login": func() (map[string]string, string, oauthState, *secrets.CustomAPIConn) {
			return nil, UsageZen, oauthState{}, nil
		},
		"billing off": func() (map[string]string, string, oauthState, *secrets.CustomAPIConn) {
			return nil, UsageOff, oauthState{known: true}, nil
		},
		"an empty stored key slot": nil, // control: an empty value is no key
	} {
		want := routes.Launch{}
		if v == nil {
			v = func() (map[string]string, string, oauthState, *secrets.CustomAPIConn) {
				return map[string]string{opencodeKeyEnv: ""}, UsageZen, oauthState{known: true}, nil
			}
			want = freeLaunch
		}
		credentialView = v
		if got := resolveDefault(session.Meta{Dir: realTemp(t)}); got != want {
			t.Errorf("%s: resolved %+v, want %+v", name, got, want)
		}
	}
}

// What a policy judges a launch on, ResolveLaunch, never asks the serve: a create is judged before
// its working directory is known, and a resume must not wait on a probe. The chat has no directory.
func TestDefaultModelLaunchIsNotWhatAPolicyJudges(t *testing.T) {
	freeView(t)
	f := &fakeDirServe{providers: onlyOpencode, def: freeDefault}
	serveWith(t, f)
	m := session.Meta{Name: "s", Kind: session.KindOpencode, Dir: realTemp(t)}
	if got := (agentImpl{}).ResolveLaunch(m); got != (routes.Launch{}) {
		t.Errorf("ResolveLaunch resolved a default-model launch: %+v", got)
	}
	if got := ModelLaunch(""); got != (routes.Launch{}) {
		t.Errorf("the chat resolved: %+v", got)
	}
	if len(f.askedDirs()) != 0 {
		t.Errorf("the serve was asked: %v", f.askedDirs())
	}
}

func TestKeylessDefaultModelDirect(t *testing.T) {
	f := &fakeDirServe{providers: onlyOpencode, def: freeDefault, plugins: activePlugins, partial: "[]"}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := keylessDefaultModel(ctx, srv.URL, realTemp(t))
	if err != nil || got != "opencode/big-pickle" {
		t.Fatalf("keylessDefaultModel = %q, %v", got, err)
	}
}

// Only a session the shared serve runs is recorded: a Terminal runs its own opencode, in a pane
// whose environment the serve's provider list says nothing about, and the serve is never asked.
func TestDefaultModelLaunchTerminalIsNotRecorded(t *testing.T) {
	freeView(t)
	f := &fakeDirServe{providers: onlyOpencode, def: freeDefault}
	serveWith(t, f)
	dir := realTemp(t)
	if got := resolveDefault(session.Meta{Dir: dir, Driver: session.DriverTUI}); got != (routes.Launch{}) || len(f.askedDirs()) != 0 {
		t.Errorf("a Terminal session resolved %+v after asking %v", got, f.askedDirs())
	}
	if got := resolveDefault(session.Meta{Dir: dir, Driver: session.DriverManaged}); got != freeLaunch {
		t.Errorf("the same directory on the managed control: %+v, want %+v", got, freeLaunch)
	}
}

// The budget runs from the moment the caller began: when the file system before the first request
// has already spent it, no request is sent.
func TestDefaultModelLaunchSpentBudgetSendsNothing(t *testing.T) {
	f := &fakeDirServe{providers: onlyOpencode, def: freeDefault}
	serveWith(t, f)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer srv.Close()
	t.Setenv(serveAddrEnv, srv.URL)
	if got := launchDefaultModelOf(realTemp(t), time.Now().Add(-time.Second)); got != "" {
		t.Errorf("a spent budget resolved %q", got)
	}
	if requests.Load() != 0 {
		t.Errorf("%d requests after the budget was spent", requests.Load())
	}
}
