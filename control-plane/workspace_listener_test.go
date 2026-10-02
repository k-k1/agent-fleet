package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// routeSample turns a route line of muxRoutes ("GET /internal/memos/{id}", "ANY /git/…")
// back into the pattern buildMux registered and a request that pattern matches.
func routeSample(line string) (pattern string, req *http.Request) {
	method, path, _ := strings.Cut(line, " ")
	pattern = line
	if method == "ANY" {
		pattern, method = path, http.MethodGet
	}
	var segs []string
	for _, s := range strings.Split(path, "/") {
		switch {
		case strings.HasSuffix(s, "...}"):
			s = "x/y"
		case strings.HasPrefix(s, "{"):
			s = "x"
		}
		segs = append(segs, s)
	}
	p := strings.Join(segs, "/")
	p = strings.ReplaceAll(p, "{$}", "")
	return pattern, httptest.NewRequest(method, p, nil)
}

// Every pattern the workspace listener lists must exist in the main route table, or a
// rename in routes.go silently drops a workspace feature on the deployments that use it.
func TestWorkspaceRoutesAreRegistered(t *testing.T) {
	_, mux := smokeEnvWith(t, allRouteSwitches(t)...)
	registered := map[string]bool{}
	for _, line := range muxRoutes(t, mux) {
		p, _ := routeSample(line)
		registered[p] = true
	}
	for p := range workspaceRoutes {
		if !registered[p] {
			t.Errorf("workspaceRoutes lists %q, which buildMux does not register", p)
		}
	}
}

// The listener serves exactly the listed patterns: every other route of the full table —
// the Console, the admin API, login, the egress proxy's /internal/egress* — answers 404.
func TestWorkspaceListenerServesOnlyWorkspaceRoutes(t *testing.T) {
	cfg, mux := smokeEnvWith(t, allRouteSwitches(t)...)
	h := workspaceListenerHandler(mux, cfg.mgr.emailHeader)
	var served, refused int
	for _, line := range muxRoutes(t, mux) {
		pattern, req := routeSample(line)
		if _, got := mux.Handler(req); got != pattern {
			// The sample hit a more specific route; that route is checked on its own line.
			continue
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if workspaceRoutes[pattern] {
			served++
			if w.Code == http.StatusNotFound && strings.HasPrefix(w.Body.String(), "404 page not found") {
				t.Errorf("%s: workspace route answered the listener's 404", line)
			}
			continue
		}
		refused++
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: reachable on the workspace listener (%d %s)", line, w.Code, w.Body.String())
		}
	}
	if served != len(workspaceRoutes) || refused < 100 {
		t.Fatalf("checked %d workspace routes (want %d) and %d others: the sampling is broken",
			served, len(workspaceRoutes), refused)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/"},
		{"GET", "/login"},
		{"GET", "/healthz"},
		{"GET", "/api/whoami"},
		{"POST", "/internal/egress"},
		{"GET", "/internal/egress/policy"},
		{"GET", "/internal/memo-categories"},
		{"GET", "/mcp"},
		{"DELETE", "/internal/docs"}, // a listed path under a method it is not listed for
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s on the workspace listener: %d, want 404", c.method, c.path, w.Code)
		}
	}
}

// A forged identity header gets a workspace nothing on its listener: the Console route that
// would honour it is not there, and a bridge route still demands its own token.
func TestWorkspaceListenerIgnoresForgedIdentity(t *testing.T) {
	cfg, mux := smokeProxyEnv(t)
	forged := func(method, path string) *http.Request {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("X-Forwarded-Email", "admin@example.com")
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		return r
	}
	// Positive control: the main mux in AUTH=proxy does take the header.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, forged("GET", "/api/whoami"))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "admin@example.com") {
		t.Fatalf("control: main mux whoami = %d %s", w.Code, w.Body.String())
	}

	h := workspaceListenerHandler(mux, cfg.mgr.emailHeader)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, forged("GET", "/api/whoami"))
	if w.Code != http.StatusNotFound {
		t.Errorf("whoami on the workspace listener: %d, want 404", w.Code)
	}
	for _, path := range []string{"/internal/memos", "/internal/docs", "/internal/mcp-servers", "/internal/branch-rules", "/internal/aws-profiles", "/internal/gcp-profiles", "/internal/schedules"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, forged("GET", path))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with a forged identity and no token: %d %s, want 401", path, w.Code, w.Body.String())
		}
	}
}

// The client is the connection, whatever AF_TRUSTED_PROXY_HOPS says, and no identity or
// forwarding header reaches a handler — including a renamed AUTH_EMAIL_HEADER.
func TestWorkspaceEdgeStripsHeadersAndUsesPeerAddress(t *testing.T) {
	old := trustedProxyHops
	trustedProxyHops = 1
	t.Cleanup(func() { trustedProxyHops = old })

	var seen *http.Request
	var info clientIPInfo
	h := workspaceEdge(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, info = r, clientIPFrom(r.Context())
	}), "X-Custom-Email")
	r := httptest.NewRequest("GET", "/internal/docs", nil)
	r.RemoteAddr = "10.1.2.3:5555"
	for _, hdr := range append([]string{"X-Custom-Email"}, workspaceUntrustedHeaders...) {
		r.Header.Set(hdr, "203.0.113.9")
	}
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !info.OK || info.IP.String() != "10.1.2.3" || info.Forwarded {
		t.Errorf("client = %+v, want the peer 10.1.2.3 with no forwarding", info)
	}
	for _, hdr := range append([]string{"X-Custom-Email"}, workspaceUntrustedHeaders...) {
		if v := seen.Header.Get(hdr); v != "" {
			t.Errorf("%s reached the handler: %q", hdr, v)
		}
	}
}

func extraEnvValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v, true
		}
	}
	return "", false
}

// AF_CP_INTERNAL_URL unset: the workspace env carries neither the new variable nor any
// change to the public base, the git host or the clone URL.
func TestWorkspaceEnvWithoutInternalURLIsUnchanged(t *testing.T) {
	_, mgr, mv := bridgeEnv(t)
	mgr.dataRoot = t.TempDir()
	mgr.publicBaseURL = "https://af.example"
	mgr.internalGitHost = internalGitCredentialHost(mgr.publicBaseURL)
	env := mgr.workspaceExtraEnv(t.Context(), store.Workspace{ID: "ws1", TenantID: mv.TenantID, MembershipID: mv.MembershipID})
	if v, ok := extraEnvValue(env, "AF_CP_INTERNAL_URL"); ok {
		t.Errorf("AF_CP_INTERNAL_URL = %q injected with no internal URL configured", v)
	}
	if v, _ := extraEnvValue(env, "AF_CP_BASE_URL"); v != "https://af.example" {
		t.Errorf("AF_CP_BASE_URL = %q", v)
	}
	if v, _ := extraEnvValue(env, "AF_INTERNAL_GIT_HOST"); v != "af.example" {
		t.Errorf("AF_INTERNAL_GIT_HOST = %q", v)
	}
	g := newGitServerAPI(mgr, mgr.publicBaseURL)
	if got := g.cloneURL("t", "r"); got != "https://af.example/git/t/r.git" {
		t.Errorf("clone URL = %q", got)
	}
}

// AF_CP_INTERNAL_URL set: injected next to an unchanged public base. The git host and
// clone URL stay public — the Agent maps them onto the internal URL for its own git.
func TestWorkspaceEnvCarriesInternalURL(t *testing.T) {
	_, mgr, mv := bridgeEnv(t)
	mgr.dataRoot = t.TempDir()
	mgr.publicBaseURL = "https://af.example"
	mgr.internalBaseURL = "http://af-cp-internal.af.svc:8098"
	mgr.internalGitHost = internalGitCredentialHost(mgr.publicBaseURL)
	env := mgr.workspaceExtraEnv(t.Context(), store.Workspace{ID: "ws1", TenantID: mv.TenantID, MembershipID: mv.MembershipID})
	if v, _ := extraEnvValue(env, "AF_CP_INTERNAL_URL"); v != "http://af-cp-internal.af.svc:8098" {
		t.Errorf("AF_CP_INTERNAL_URL = %q", v)
	}
	if v, _ := extraEnvValue(env, "AF_CP_BASE_URL"); v != "https://af.example" {
		t.Errorf("AF_CP_BASE_URL = %q, want the public base unchanged", v)
	}
	host, _ := extraEnvValue(env, "AF_INTERNAL_GIT_HOST")
	clone := newGitServerAPI(mgr, mgr.publicBaseURL).cloneURL("t", "r")
	if clone != "https://af.example/git/t/r.git" {
		t.Errorf("clone URL = %q, want the public one", clone)
	}
	if u, err := url.Parse(clone); err != nil || u.Host != host {
		t.Errorf("AF_INTERNAL_GIT_HOST = %q, want the clone URL's authority (%q)", host, clone)
	}
}

// Every KNOWN request the Agent sends to the CP (workspace/agent: docs_sync, branchrule,
// mcpreg, awsx, mcpx memo/schedule, gitx git-oauth, engines, git and git-lfs) must reach its
// handler on the workspace listener. Dropping one from workspaceRoutes breaks that feature
// only where AF_CP_INTERNAL_LISTEN is set, which no other test would notice. The list is
// written by hand — the Agent is another Go module — so a NEW Agent → CP call has to be
// added here and to workspaceRoutes together; nothing detects one that was not.
func TestWorkspaceListenerServesKnownAgentCalls(t *testing.T) {
	cfg, mux := smokeEnvWith(t, allRouteSwitches(t)...)
	h := workspaceListenerHandler(mux, cfg.mgr.emailHeader)
	calls := []struct{ method, path string }{
		{"GET", "/internal/docs"},
		{"GET", "/internal/branch-rules"},
		{"GET", "/internal/mcp-servers"},
		{"GET", "/internal/aws-profiles"},
		{"GET", "/internal/gcp-profiles"},
		{"GET", "/internal/memos"},
		{"POST", "/internal/memos"},
		{"POST", "/internal/memos/flush"},
		{"PATCH", "/internal/memos/m1"},
		{"DELETE", "/internal/memos/m1"},
		{"GET", "/internal/schedules"},
		{"POST", "/internal/schedules"},
		{"PATCH", "/internal/schedules/s1"},
		{"DELETE", "/internal/schedules/s1"},
		{"POST", "/internal/schedules/s1/pause"},
		{"POST", "/internal/schedules/s1/resume"},
		{"POST", "/internal/schedules/s1/run-now"},
		{"GET", "/internal/schedules/s1/runs"},
		{"POST", "/internal/git-oauth/bitbucket/refresh"},
		{"POST", "/internal/git-oauth/jira/refresh"},
		{"POST", "/internal/engine/token"},
		{"GET", "/internal/engine/catalog"},
		{"GET", "/engine/llm/props"},
		{"POST", "/engine/llm/v1/chat/completions"},
		{"GET", "/git/t/r.git/info/refs?service=git-upload-pack"},
		{"POST", "/git/t/r.git/git-upload-pack"},
		{"POST", "/git/t/r.git/git-receive-pack"},
		{"POST", "/git/t/r.git/info/lfs/objects/batch"},
		{"PUT", "/git/t/r.git/info/lfs/objects/" + strings.Repeat("a", 64)},
		{"GET", "/git/t/r.git/info/lfs/objects/" + strings.Repeat("a", 64)},
		{"POST", "/git/t/r.git/info/lfs/locks"},
		{"GET", "/git/t/r.git/info/lfs/locks"},
		{"POST", "/git/t/r.git/info/lfs/locks/verify"},
		{"POST", "/git/t/r.git/info/lfs/locks/l1/unlock"},
	}
	for _, c := range calls {
		req := httptest.NewRequest(c.method, c.path, nil)
		_, pattern := mux.Handler(req)
		if !workspaceRoutes[pattern] {
			t.Errorf("%s %s (pattern %q) is not served on the workspace listener", c.method, c.path, pattern)
			continue
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound && strings.HasPrefix(w.Body.String(), "404 page not found") {
			t.Errorf("%s %s: the listener's 404", c.method, c.path)
		}
	}
}

// The same LFS batch answers each listener with hrefs on that listener: a person's clone
// outside the cluster must get the public base, a workspace on the workspace listener the
// internal one — and each href must actually transfer the object.
func TestLFSHrefsFollowTheListener(t *testing.T) {
	ctx := t.Context()
	tmp := t.TempDir()
	dataRoot := filepath.Join(tmp, "data")
	st, err := store.OpenSQLite(filepath.Join(tmp, "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dflt, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "u@x", "u-x", "")
	mem, _ := st.EnsureMembership(ctx, ident.ID, dflt.ID, "member")
	if err := os.MkdirAll(filepath.Join(dataRoot, "git", "default", "shared.git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateGitRepo(ctx, store.GitRepo{ID: store.NewID(), TenantID: dflt.ID, Name: "shared", DefaultBranch: "main", CreatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	master := []byte("master-key-lfs-listener-000000000000")
	token := mintGitToken(gitSignKey(master), mem.ID)

	pub, internal := httptest.NewUnstartedServer(nil), httptest.NewUnstartedServer(nil)
	defer pub.Close()
	defer internal.Close()
	pubURL, internalURL := "http://"+pub.Listener.Addr().String(), "http://"+internal.Listener.Addr().String()
	mgr := &manager{store: st, master32: master, dataRoot: dataRoot, internalBaseURL: internalURL}
	g := newGitServerAPI(mgr, pubURL)
	pub.Config.Handler = g.gitMux()
	internal.Config.Handler = workspaceEdge(g.gitMux(), "")
	pub.Start()
	internal.Start()

	do := func(method, u string, body []byte) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, u, bytes.NewReader(body))
		req.SetBasicAuth("x-access-token", token)
		req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
		req.Header.Set("Accept", "application/vnd.git-lfs+json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, u, err)
		}
		return resp
	}
	batch := func(base, op, oid string, size int) string {
		t.Helper()
		body := fmt.Sprintf(`{"operation":%q,"transfers":["basic"],"objects":[{"oid":%q,"size":%d}]}`, op, oid, size)
		resp := do("POST", base+"/git/default/shared.git/info/lfs/objects/batch", []byte(body))
		defer resp.Body.Close()
		var out struct {
			Objects []struct {
				Actions map[string]struct {
					Href string `json:"href"`
				} `json:"actions"`
			} `json:"objects"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK || len(out.Objects) != 1 {
			t.Fatalf("%s batch on %s: %d %v %+v", op, base, resp.StatusCode, err, out)
		}
		return out.Objects[0].Actions[op].Href
	}

	for _, c := range []struct {
		name, base, other string
		payload           []byte
	}{
		{"public listener", pubURL, internalURL, []byte("public-listener-object")},
		{"workspace listener", internalURL, pubURL, []byte("workspace-listener-object")},
	} {
		t.Run(c.name, func(t *testing.T) {
			sum := sha256.Sum256(c.payload)
			oid := hex.EncodeToString(sum[:])
			up := batch(c.base, "upload", oid, len(c.payload))
			if !strings.HasPrefix(up, c.base+"/git/") {
				t.Fatalf("upload href = %q, want one on %s (not %s)", up, c.base, c.other)
			}
			if resp := do("PUT", up, c.payload); resp.StatusCode/100 != 2 {
				t.Fatalf("upload to %s: %d", up, resp.StatusCode)
			}
			down := batch(c.base, "download", oid, len(c.payload))
			if !strings.HasPrefix(down, c.base+"/git/") {
				t.Fatalf("download href = %q, want one on %s", down, c.base)
			}
			resp := do("GET", down, nil)
			got, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || !bytes.Equal(got, c.payload) {
				t.Fatalf("download from %s: %d %q", down, resp.StatusCode, got)
			}
		})
	}
}

// The clone URL the Console's repository API returns (list, create and rename all build it
// with repoDTO) stays public even where the CP has an internal URL: people clone from
// outside the cluster.
func TestRepoDTOCloneURLStaysPublicWithInternalURL(t *testing.T) {
	g := gitServerAPI{publicBaseURL: "https://af.example", internalBaseURL: "http://af-cp-internal.af.svc:8098"}
	dto := g.repoDTO(store.MembershipView{TenantSlug: "t"}, store.GitRepo{Name: "r"})
	if dto.CloneURL != "https://af.example/git/t/r.git" {
		t.Errorf("clone_url = %q, want the public one", dto.CloneURL)
	}
}
