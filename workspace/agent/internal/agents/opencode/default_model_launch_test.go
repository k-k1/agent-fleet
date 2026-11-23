package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// fakeDirServe answers the three directory-scoped endpoints the way opencode 2.4.0 does (measured):
// /api/plugin is [] until the directory has settled, /api/provider is partial until then.
type fakeDirServe struct {
	providers   string // data of the settled /api/provider
	partial     string // data of /api/provider before the plugins load
	def         string // data of /api/model/default
	plugins     string // data of the settled /api/plugin
	settleAfter int32  // /api/plugin answers [] this many times first
	echoDir     string // overrides location.directory when set
	calls       atomic.Int32
	providerRan atomic.Int32
}

func (f *fakeDirServe) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dir := r.URL.Query().Get("location[directory]")
		if f.echoDir != "" {
			dir = f.echoDir
		}
		loc := `{"directory":` + mustJSON(dir) + `}`
		settled := f.calls.Load() > f.settleAfter
		switch r.URL.Path {
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

func mustJSON(s string) string { b, _ := json.Marshal(s); return string(b) }

const (
	onlyOpencode  = `[{"id":"opencode","settings":{"apiKey":"public","baseURL":"https://opencode.ai/inference/openai/v1"}}]`
	opencodeAcme  = `[{"id":"opencode","settings":{"apiKey":"public"}},{"id":"acme","settings":{"apiKey":"k"}}]`
	opencodeKeyed = `[{"id":"opencode","settings":{"apiKey":"sk-real"}}]`
	freeDefault   = `{"id":"big-pickle","providerID":"opencode","status":"active","enabled":true,"cost":[{"input":0,"output":0}]}`
	paidDefault   = `{"id":"big-opus","providerID":"opencode","status":"active","cost":[{"input":5,"output":25}]}`
	activePlugins = `[{"id":"opencode.wellknown","state":{"status":"active"}}]`
)

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
	orig := oauthProbe
	oauthProbe = func() (string, bool) { return srv.URL, true }
	t.Cleanup(func() { oauthProbe = orig })
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

func resolveIn(dir string) routes.Launch {
	return (agentImpl{}).ResolveLaunch(session.Meta{Name: "s", Kind: session.KindOpencode, Dir: dir})
}

// The acceptance of #4227: a directory with only opencode.ai's free models records the keyless
// route, and one with several providers stays unresolved.
func TestDefaultModelLaunchFromServe(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		f    *fakeDirServe
		want routes.Launch
	}{
		{"only the keyless opencode provider and a free default", &fakeDirServe{providers: onlyOpencode, def: freeDefault}, freeLaunch},
		{"settles after a few reads", &fakeDirServe{providers: onlyOpencode, def: freeDefault, settleAfter: 3}, freeLaunch},
		{"several providers", &fakeDirServe{providers: opencodeAcme, def: freeDefault}, routes.Launch{}},
		{"a key reached the opencode provider", &fakeDirServe{providers: opencodeKeyed, def: freeDefault}, routes.Launch{}},
		{"no default model", &fakeDirServe{providers: onlyOpencode, def: `null`}, routes.Launch{}},
		{"a paid default", &fakeDirServe{providers: onlyOpencode, def: paidDefault}, routes.Launch{}},
		{"a default of another provider", &fakeDirServe{providers: onlyOpencode, def: `{"id":"m","providerID":"acme","cost":[{"input":0}]}`}, routes.Launch{}},
		{"a deprecated default", &fakeDirServe{providers: onlyOpencode, def: `{"id":"m","providerID":"opencode","status":"deprecated","cost":[{"input":0}]}`}, routes.Launch{}},
		{"a failed plugin", &fakeDirServe{providers: onlyOpencode, def: freeDefault, plugins: `[{"id":"p","state":{"status":"failed","error":"x"}}]`}, routes.Launch{}},
		{"an answer for another directory", &fakeDirServe{providers: onlyOpencode, def: freeDefault, echoDir: "/elsewhere"}, routes.Launch{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			freeView(t)
			serveWith(t, tc.f)
			if got := resolveIn(dir); got != tc.want {
				t.Errorf("ResolveLaunch = %+v, want %+v", got, tc.want)
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
	if got := resolveIn(t.TempDir()); got != (routes.Launch{}) {
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
	if got := resolveIn(t.TempDir()); got != (routes.Launch{}) {
		t.Fatalf("an unsettled directory resolved: %+v", got)
	}
	if d := time.Since(start); d > routes.ObserveBudget {
		t.Errorf("the probe ran %s, past the %s launch budget", d, routes.ObserveBudget)
	}
}

// No serve is running, the daemon is a v1 one that answers its web UI for every path, or no
// directory is known: unresolved, and never a daemon start.
func TestDefaultModelLaunchNeedsAServe(t *testing.T) {
	freeView(t)
	orig := oauthProbe
	t.Cleanup(func() { oauthProbe = orig })

	oauthProbe = func() (string, bool) { t.Error("probed a daemon for a launch with no directory"); return "", false }
	if got := resolveIn(""); got != (routes.Launch{}) {
		t.Errorf("no directory: %+v", got)
	}
	if got := resolveIn("relative/dir"); got != (routes.Launch{}) {
		t.Errorf("a relative directory: %+v", got)
	}

	oauthProbe = func() (string, bool) { return "", false }
	if got := resolveIn(t.TempDir()); got != (routes.Launch{}) {
		t.Errorf("no daemon: %+v", got)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>web ui</html>"))
	}))
	defer srv.Close()
	oauthProbe = func() (string, bool) { return srv.URL, true }
	if got := resolveIn(t.TempDir()); got != (routes.Launch{}) {
		t.Errorf("a daemon that answers HTML: %+v", got)
	}
}

// The serve proves the provider is keyless; af's own view still has to agree that the free route
// is the payer. A stored key or a signed-in account on the launch is a different payer, so the
// launch stays unresolved rather than recording the free route.
func TestDefaultModelLaunchKeepsAfsOwnView(t *testing.T) {
	serveWith(t, &fakeDirServe{providers: onlyOpencode, def: freeDefault})
	old := credentialView
	t.Cleanup(func() { credentialView = old })
	for name, view := range map[string]func() (map[string]string, string, oauthState, *secrets.CustomAPIConn){
		"a stored key": func() (map[string]string, string, oauthState, *secrets.CustomAPIConn) {
			return map[string]string{opencodeKeyEnv: "k"}, UsageZen, oauthState{known: true}, nil
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
	} {
		credentialView = view
		if got := resolveIn(t.TempDir()); got != (routes.Launch{}) {
			t.Errorf("%s: resolved %+v", name, got)
		}
	}
}

// The assistant chat has no directory: it stays unresolved, as does a launch that names a model
// on a provider the serve was not asked about (that is resolved by its name, unchanged).
func TestDefaultModelLaunchChatStaysUnresolved(t *testing.T) {
	freeView(t)
	serveWith(t, &fakeDirServe{providers: onlyOpencode, def: freeDefault})
	if got := ModelLaunch(""); got != (routes.Launch{}) {
		t.Errorf("the chat resolved: %+v", got)
	}
}

func TestKeylessDefaultModelDirectoryEchoIsCleaned(t *testing.T) {
	f := &fakeDirServe{providers: onlyOpencode, def: freeDefault, plugins: activePlugins, partial: "[]"}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	dir := filepath.Clean(t.TempDir())
	got, err := keylessDefaultModel(ctx, srv.URL, dir)
	if err != nil || got != "opencode/big-pickle" {
		t.Fatalf("keylessDefaultModel = %q, %v", got, err)
	}
}
