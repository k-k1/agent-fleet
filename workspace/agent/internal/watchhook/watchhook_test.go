package watchhook

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testSecret = "s3cr3t-value-from-cp"

// fakeCP is the Control Plane's /internal/watch-hooks as far as the Agent uses it.
type fakeCP struct {
	mu    sync.Mutex
	hooks map[string]Hook
	n     int
	calls []string
	auth  string
}

func (f *fakeCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.auth = r.Header.Get("Authorization")
	switch {
	case r.Method == "GET":
		out := []Hook{}
		for _, h := range f.hooks {
			out = append(out, h)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"hooks": out})
	case r.Method == "POST":
		var in struct{ Source, Repo string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.n++
		id := "h" + string(rune('0'+f.n))
		h := Hook{ID: id, Source: in.Source, Repo: strings.ToLower(in.Repo), URL: "https://af.example/webhooks/hooks/" + id}
		f.hooks[id] = h
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(cpHook{Hook: h, Secret: testSecret})
	case r.Method == "DELETE":
		id := strings.TrimPrefix(r.URL.Path, "/internal/watch-hooks/")
		if _, ok := f.hooks[id]; !ok {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"no such hook"}}`)
			return
		}
		delete(f.hooks, id)
		_ = json.NewEncoder(w).Encode(map[string]string{"deleted": id})
	}
}

type env struct {
	cp      *fakeCP
	m       *Manager
	forge   *httptest.Server
	got     []string // forge requests, "METHOD path"
	bodies  []string
	status  int    // status the forge answers a hook creation with
	msg     string // message in a refusal body
	listing string // body of a hook listing
	delSt   int
	auths   []string          // Authorization header of every forge request
	pages   map[string]string // GitHub listing by ?page=; overrides listing when set
	drop    bool              // answer a hook creation by cutting the connection
}

func newEnv(t *testing.T) *env {
	e := &env{cp: &fakeCP{hooks: map[string]Hook{}}, status: 201, delSt: 204}
	cpSrv := httptest.NewServer(e.cp)
	e.forge = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		e.got = append(e.got, r.Method+" "+r.URL.Path)
		e.bodies = append(e.bodies, string(b))
		e.auths = append(e.auths, r.Header.Get("Authorization"))
		switch r.Method {
		case "POST":
			if e.drop {
				e.drop = false
				hj, _ := w.(http.Hijacker)
				c, _, _ := hj.Hijack()
				c.Close()
				return
			}
			w.WriteHeader(e.status)
			if e.status >= 400 {
				_ = json.NewEncoder(w).Encode(map[string]string{"message": e.msg})
			}
		case "GET":
			if e.pages != nil {
				_, _ = io.WriteString(w, e.pages[r.URL.Query().Get("page")])
				return
			}
			_, _ = io.WriteString(w, e.listing)
		case "DELETE":
			w.WriteHeader(e.delSt)
		}
	}))
	t.Cleanup(cpSrv.Close)
	t.Cleanup(e.forge.Close)
	e.m = &Manager{
		CPBase: cpSrv.URL, CPToken: "cp-token", GitHubAPI: e.forge.URL, BitbucketAPI: e.forge.URL,
		Creds: func(_ context.Context, host string) (Cred, error) {
			return Cred{User: "u", Token: "forge-token"}, nil
		},
	}
	return e
}

func TestAddGitHubInstallsWithMatchingSettingsAndNeverReturnsTheSecret(t *testing.T) {
	e := newEnv(t)
	res, err := e.m.Add(context.Background(), "github", "Acme/Priv")
	if err != nil {
		t.Fatal(err)
	}
	if res.ID == "" || res.URL == "" || res.Existing {
		t.Fatalf("result: %+v", res)
	}
	if b, _ := json.Marshal(res); strings.Contains(string(b), testSecret) {
		t.Fatalf("the secret reached the result: %s", b)
	}
	if e.cp.auth != "Bearer cp-token" {
		t.Fatalf("cp auth: %q", e.cp.auth)
	}
	if len(e.got) != 1 || e.got[0] != "POST /repos/Acme/Priv/hooks" {
		t.Fatalf("forge calls: %v", e.got)
	}
	var p struct {
		Events []string
		Config map[string]string
	}
	_ = json.Unmarshal([]byte(e.bodies[0]), &p)
	// What watchingress verifies: a JSON body signed with X-Hub-Signature-256, which GitHub does
	// for content_type json plus a secret; and only events ParseGitHub reads.
	if p.Config["content_type"] != "json" || p.Config["secret"] != testSecret || p.Config["url"] != res.URL || p.Config["insecure_ssl"] != "0" {
		t.Fatalf("config: %v", p.Config)
	}
	want := map[string]bool{"pull_request": true, "check_run": true, "check_suite": true, "issue_comment": true, "workflow_run": true}
	have := map[string]bool{}
	for _, ev := range p.Events {
		have[ev] = true
	}
	for ev := range want {
		if !have[ev] {
			t.Fatalf("event %s missing: %v", ev, p.Events)
		}
	}
}

func TestAddBitbucketUsesPullRequestEventsAndSecret(t *testing.T) {
	e := newEnv(t)
	if _, err := e.m.Add(context.Background(), "bitbucket", "ws/repo"); err != nil {
		t.Fatal(err)
	}
	if e.got[0] != "POST /repositories/ws/repo/hooks" {
		t.Fatalf("forge calls: %v", e.got)
	}
	var p struct {
		Secret string
		URL    string
		Events []string
		Active bool
	}
	_ = json.Unmarshal([]byte(e.bodies[0]), &p)
	if p.Secret != testSecret || !p.Active || len(p.Events) == 0 {
		t.Fatalf("payload: %+v", p)
	}
	for _, ev := range p.Events {
		if !strings.HasPrefix(ev, "pullrequest:") {
			t.Fatalf("ParseBitbucket reads only pullrequest:*, got %s", ev)
		}
	}
}

func TestAddRollsBackTheControlPlaneHookWhenTheForgeRefuses(t *testing.T) {
	for _, st := range []int{403, 404} {
		e := newEnv(t)
		e.status, e.msg = st, "Must have admin rights to Repository."
		_, err := e.m.Add(context.Background(), "github", "acme/priv")
		var ue *Error
		if err == nil || !asErr(err, &ue) || ue.Code != "forge_denied" {
			t.Fatalf("status %d: want forge_denied, got %v", st, err)
		}
		if !strings.Contains(ue.Message, "admin") || !strings.Contains(ue.Message, "No hook was left behind") {
			t.Fatalf("message not actionable: %s", ue.Message)
		}
		if len(e.cp.hooks) != 0 {
			t.Fatalf("orphan Control Plane hook after a refused install: %v", e.cp.hooks)
		}
		if strings.Contains(ue.Message, testSecret) || strings.Contains(ue.Message, "forge-token") {
			t.Fatalf("secret in message: %s", ue.Message)
		}
	}
}

func TestAddRollsBackOnForgeServerErrorAndScrubsTheSecret(t *testing.T) {
	e := newEnv(t)
	e.status, e.msg = 500, "boom "+testSecret
	_, err := e.m.Add(context.Background(), "github", "acme/priv")
	if err == nil || len(e.cp.hooks) != 0 {
		t.Fatalf("err=%v hooks=%v", err, e.cp.hooks)
	}
	if strings.Contains(err.Error(), testSecret) {
		t.Fatalf("secret leaked: %v", err)
	}
}

func TestAddWithoutCredentialCreatesNothing(t *testing.T) {
	e := newEnv(t)
	e.m.Creds = func(context.Context, string) (Cred, error) { return Cred{}, io.EOF }
	_, err := e.m.Add(context.Background(), "github", "acme/priv")
	var ue *Error
	if !asErr(err, &ue) || ue.Code != "no_credential" {
		t.Fatalf("err=%v", err)
	}
	if len(e.cp.calls) != 0 {
		t.Fatalf("the Control Plane was called before the credential was known: %v", e.cp.calls)
	}
}

func TestAddIsIdempotentPerRepository(t *testing.T) {
	e := newEnv(t)
	first, err := e.m.Add(context.Background(), "github", "Acme/Priv")
	if err != nil {
		t.Fatal(err)
	}
	e.got = nil
	e.listing = `[{"id":1,"config":{"url":"` + first.URL + `"}}]`
	second, err := e.m.Add(context.Background(), "github", "acme/priv")
	if err != nil {
		t.Fatal(err)
	}
	if !second.Existing || second.ID != first.ID || len(e.cp.hooks) != 1 || len(e.got) != 1 || e.got[0] != "GET /repos/acme/priv/hooks" {
		t.Fatalf("second add: %+v hooks=%d forge=%v", second, len(e.cp.hooks), e.got)
	}
}

func TestAddRejectsBadInputBeforeAnyCall(t *testing.T) {
	e := newEnv(t)
	for _, c := range [][2]string{{"gitlab", "a/b"}, {"github", "ab"}, {"github", "a/b/c"}, {"github", "../x"}} {
		if _, err := e.m.Add(context.Background(), c[0], c[1]); err == nil {
			t.Fatalf("%v accepted", c)
		}
	}
	if len(e.cp.calls) != 0 {
		t.Fatal(e.cp.calls)
	}
}

func TestRemoveDeletesAtForgeThenControlPlane(t *testing.T) {
	e := newEnv(t)
	res, _ := e.m.Add(context.Background(), "github", "acme/priv")
	e.got = nil
	e.listing = `[{"id":7,"config":{"url":"https://other/x"}},{"id":42,"config":{"url":"` + res.URL + `"}}]`
	rm, err := e.m.Remove(context.Background(), res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !rm.ForgeRemoved || rm.Warning != "" || len(e.cp.hooks) != 0 {
		t.Fatalf("%+v hooks=%v", rm, e.cp.hooks)
	}
	if len(e.got) != 2 || e.got[1] != "DELETE /repos/acme/priv/hooks/42" {
		t.Fatalf("forge calls: %v", e.got)
	}
}

func TestRemoveBitbucketFindsTheHookByURL(t *testing.T) {
	e := newEnv(t)
	res, _ := e.m.Add(context.Background(), "bitbucket", "ws/repo")
	e.got = nil
	e.listing = `{"values":[{"uuid":"{abc}","url":"` + res.URL + `"}]}`
	rm, err := e.m.Remove(context.Background(), res.ID)
	if err != nil || !rm.ForgeRemoved || len(e.cp.hooks) != 0 {
		t.Fatalf("%+v %v", rm, err)
	}
	if !strings.HasPrefix(e.got[1], "DELETE /repositories/ws/repo/hooks/") {
		t.Fatalf("forge calls: %v", e.got)
	}
}

func TestRemoveStillRetiresTheControlPlaneHookWhenTheForgeRefuses(t *testing.T) {
	e := newEnv(t)
	res, _ := e.m.Add(context.Background(), "github", "acme/priv")
	e.m.Creds = func(context.Context, string) (Cred, error) { return Cred{}, io.EOF }
	rm, err := e.m.Remove(context.Background(), res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rm.ForgeRemoved || !strings.Contains(rm.Warning, res.URL) || len(e.cp.hooks) != 0 {
		t.Fatalf("%+v hooks=%v", rm, e.cp.hooks)
	}
}

func TestRemoveUnknownHookIs404(t *testing.T) {
	e := newEnv(t)
	_, err := e.m.Remove(context.Background(), "nope")
	var ue *Error
	if !asErr(err, &ue) || ue.Status != 404 {
		t.Fatalf("%v", err)
	}
}

func TestNoBridgeIsAClearError(t *testing.T) {
	m := &Manager{Creds: func(context.Context, string) (Cred, error) { return Cred{Token: "t"}, nil }}
	_, err := m.Add(context.Background(), "github", "a/b")
	var ue *Error
	if !asErr(err, &ue) || ue.Code != "no_bridge" {
		t.Fatalf("%v", err)
	}
}

func TestCLIAgainstHandlers(t *testing.T) {
	e := newEnv(t)
	mux := http.NewServeMux()
	h := Handlers{M: e.m}
	mux.HandleFunc("GET /watch/hooks", h.List)
	mux.HandleFunc("POST /watch/hooks", h.Add)
	mux.HandleFunc("DELETE /watch/hooks/{id}", h.Remove)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &Client{Base: srv.URL, HTTP: srv.Client()}

	var out, errw bytes.Buffer
	if rc := Main(c, []string{"add", "github", "acme/priv"}, &out, &errw); rc != 0 {
		t.Fatalf("add rc=%d %s", rc, errw.String())
	}
	if strings.Contains(out.String()+errw.String(), testSecret) {
		t.Fatal("secret printed")
	}
	out.Reset()
	if rc := Main(c, []string{"list"}, &out, &errw); rc != 0 || !strings.Contains(out.String(), "acme/priv") {
		t.Fatalf("list rc=%d %q", rc, out.String())
	}
	e.status, e.msg = 403, "nope"
	errw.Reset()
	if rc := Main(c, []string{"add", "github", "acme/other"}, &out, &errw); rc != 1 || !strings.Contains(errw.String(), "admin") {
		t.Fatalf("refused add rc=%d %q", rc, errw.String())
	}
	if rc := Main(c, []string{"add", "github"}, &out, &errw); rc != 2 {
		t.Fatalf("usage rc=%d", rc)
	}
}

func asErr(err error, target **Error) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestAddSendsTheRightAuthorization(t *testing.T) {
	e := newEnv(t)
	if _, err := e.m.Add(context.Background(), "github", "a/b"); err != nil {
		t.Fatal(err)
	}
	e.m.Creds = func(context.Context, string) (Cred, error) {
		return Cred{User: "me@example.com", Token: "apitok", Basic: true}, nil
	}
	if _, err := e.m.Add(context.Background(), "bitbucket", "ws/r"); err != nil {
		t.Fatal(err)
	}
	if e.auths[0] != "Bearer forge-token" {
		t.Fatalf("github: %q", e.auths[0])
	}
	req, _ := http.NewRequest("GET", "http://x", nil)
	req.SetBasicAuth("me@example.com", "apitok")
	if e.auths[1] != req.Header.Get("Authorization") {
		t.Fatalf("bitbucket api token must be Basic email:token, got %q", e.auths[1])
	}
}

func TestRemoveFindsAGitHubHookOnLaterPages(t *testing.T) {
	e := newEnv(t)
	res, _ := e.m.Add(context.Background(), "github", "acme/priv")
	e.got = nil
	page1 := "[" + strings.Repeat(`{"id":1,"config":{"url":"https://other"}},`, 99) + `{"id":1,"config":{"url":"https://other"}}]`
	e.pages = map[string]string{"1": page1, "2": `[{"id":99,"config":{"url":"` + res.URL + `"}}]`}
	rm, err := e.m.Remove(context.Background(), res.ID)
	if err != nil || !rm.ForgeRemoved || e.got[len(e.got)-1] != "DELETE /repos/acme/priv/hooks/99" {
		t.Fatalf("%+v %v %v", rm, err, e.got)
	}
}

func TestAddAgainWarnsWhenTheForgeWebhookIsGone(t *testing.T) {
	e := newEnv(t)
	res, _ := e.m.Add(context.Background(), "github", "acme/priv")
	e.listing = `[]`
	again, err := e.m.Add(context.Background(), "github", "acme/priv")
	if err != nil || !again.Existing || !strings.Contains(again.Note, "missing at github.com") || !strings.Contains(again.Note, res.ID) {
		t.Fatalf("%+v %v", again, err)
	}
	e.listing = `[{"id":1,"config":{"url":"` + res.URL + `"}}]`
	again, _ = e.m.Add(context.Background(), "github", "acme/priv")
	if strings.Contains(again.Note, "missing") {
		t.Fatalf("healthy hook reported missing: %s", again.Note)
	}
}

func TestLostInstallAnswerCleansUpAForgeHookThatMayExist(t *testing.T) {
	e := newEnv(t)
	e.drop = true
	e.listing = `[{"id":5,"config":{"url":"https://af.example/webhooks/hooks/h1"}}]`
	_, err := e.m.Add(context.Background(), "github", "acme/priv")
	if err == nil || len(e.cp.hooks) != 0 {
		t.Fatalf("err=%v hooks=%v", err, e.cp.hooks)
	}
	if e.got[len(e.got)-1] != "DELETE /repos/acme/priv/hooks/5" {
		t.Fatalf("the possibly created forge hook was left: %v", e.got)
	}
}

func TestRemoveOfAnUnknownSourceOnlyWarns(t *testing.T) {
	e := newEnv(t)
	e.cp.hooks["x"] = Hook{ID: "x", Source: "gitlab", Repo: "a/b", URL: "https://af.example/webhooks/hooks/x"}
	rm, err := e.m.Remove(context.Background(), "x")
	if err != nil || rm.ForgeRemoved || rm.Warning == "" || len(e.got) != 0 {
		t.Fatalf("%+v %v %v", rm, err, e.got)
	}
}
